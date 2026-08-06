package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/identity/application"
	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/idgen"
	"video-processor/internal/identity/infrastructure/jwtauth"
	"video-processor/internal/identity/infrastructure/password"
	"video-processor/internal/identity/infrastructure/postgres"
)

// identityJWTSigningKeyEnv is the environment variable holding the JWT
// signing key, alongside postgres.Config's own IDENTITY_POSTGRES_DSN.
const identityJWTSigningKeyEnv = "IDENTITY_JWT_SIGNING_KEY"

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// identityModule wires the Identity bounded context's use cases to the HTTP layer.
type identityModule struct {
	registerUser     *application.RegisterUser
	authenticateUser *application.AuthenticateUser
}

func newIdentityModule(registerUser *application.RegisterUser, authenticateUser *application.AuthenticateUser) *identityModule {
	return &identityModule{registerUser: registerUser, authenticateUser: authenticateUser}
}

// setupIdentity builds the production Identity module from environment
// configuration. It returns a nil module (identity disabled) only when
// neither IDENTITY_POSTGRES_DSN nor IDENTITY_JWT_SIGNING_KEY is set at all,
// preserving video-processing-only startup for local/Docker runs that
// haven't opted into identity yet. Any other configuration state — partial,
// or present but invalid — fails startup clearly rather than silently
// running with unsafe defaults.
func setupIdentity(ctx context.Context) (*identityModule, *sql.DB, error) {
	pgConfig, pgErr := postgres.LoadConfigFromEnv()
	signingKey := os.Getenv(identityJWTSigningKeyEnv)

	if errors.Is(pgErr, postgres.ErrDSNRequired) && signingKey == "" {
		return nil, nil, nil
	}
	if pgErr != nil {
		return nil, nil, fmt.Errorf("identity: %w", pgErr)
	}
	if signingKey == "" {
		return nil, nil, fmt.Errorf("identity: %s environment variable is required", identityJWTSigningKeyEnv)
	}

	db, err := postgres.Open(pgConfig)
	if err != nil {
		return nil, nil, err
	}
	if err := postgres.Migrate(ctx, db); err != nil {
		closeDB(db)
		return nil, nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		closeDB(db)
		return nil, nil, fmt.Errorf("identity: connect to postgres: %w", err)
	}

	tokens, err := jwtauth.New(signingKey)
	if err != nil {
		closeDB(db)
		return nil, nil, err
	}

	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	passwords := password.New()
	clock := systemClock{}

	module := newIdentityModule(
		application.NewRegisterUser(repo, ids, passwords, clock),
		application.NewAuthenticateUser(repo, passwords, tokens, clock),
	)
	return module, db, nil
}

// closeDB closes db, logging any failure — used on setup-failure paths where
// a different, more relevant error is already being returned to the caller.
func closeDB(db *sql.DB) {
	if err := db.Close(); err != nil {
		log.Printf("identity: close postgres: %v", err)
	}
}

func (m *identityModule) registerRoutes(router *gin.Engine) {
	auth := router.Group("/api/auth")
	auth.POST("/register", m.handleRegister)
	auth.POST("/login", m.handleLogin)
}

type registerUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type registerUserResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

type authenticateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authenticateUserResponse struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type identityErrorResponse struct {
	Error string `json:"error"`
}

func (m *identityModule) handleRegister(c *gin.Context) {
	var req registerUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, identityErrorResponse{Error: "invalid request body"})
		return
	}

	result, err := m.registerUser.Execute(c.Request.Context(), application.RegisterUserInput{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidEmail), errors.Is(err, application.ErrPasswordTooShort):
			c.JSON(http.StatusBadRequest, identityErrorResponse{Error: "invalid email or password"})
		case errors.Is(err, domain.ErrUserAlreadyExists):
			c.JSON(http.StatusConflict, identityErrorResponse{Error: "an account with this email already exists"})
		default:
			log.Printf("register user: %v", err)
			c.JSON(http.StatusInternalServerError, identityErrorResponse{Error: "internal server error"})
		}
		return
	}

	c.JSON(http.StatusCreated, registerUserResponse{
		ID:        result.UserID,
		Email:     result.Email,
		CreatedAt: result.CreatedAt,
	})
}

func (m *identityModule) handleLogin(c *gin.Context) {
	var req authenticateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, identityErrorResponse{Error: "invalid request body"})
		return
	}

	result, err := m.authenticateUser.Execute(c.Request.Context(), application.AuthenticateUserInput{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		if errors.Is(err, application.ErrAuthenticationFailed) {
			c.JSON(http.StatusUnauthorized, identityErrorResponse{Error: "invalid email or password"})
			return
		}
		log.Printf("authenticate user: %v", err)
		c.JSON(http.StatusInternalServerError, identityErrorResponse{Error: "internal server error"})
		return
	}

	c.JSON(http.StatusOK, authenticateUserResponse{
		AccessToken: result.AccessToken,
		ExpiresAt:   result.ExpiresAt,
	})
}

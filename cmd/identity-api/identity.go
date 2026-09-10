package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/identity/application"
	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/idgen"
	"video-processor/internal/identity/infrastructure/jwtauth"
	"video-processor/internal/identity/infrastructure/password"
	"video-processor/internal/identity/infrastructure/postgres"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// identityModule wires the Identity bounded context's use cases to the HTTP
// layer. It holds no token verifier: this service registers no authenticated
// route, and the verifier setupIdentity builds exists only to check that the
// key pair it was handed is one pair.
type identityModule struct {
	registerUser     *application.RegisterUser
	authenticateUser *application.AuthenticateUser
}

func newIdentityModule(registerUser *application.RegisterUser, authenticateUser *application.AuthenticateUser) *identityModule {
	return &identityModule{registerUser: registerUser, authenticateUser: authenticateUser}
}

// setupIdentity builds the production Identity module from environment
// configuration. Identity configuration is always required: any missing or
// invalid piece — including every variable being entirely absent — fails
// startup clearly rather than running with unsafe defaults.
//
// This is the one process in the deployment that constructs a token issuer,
// and the only one whose environment carries a private key.
func setupIdentity(ctx context.Context) (*identityModule, *sql.DB, error) {
	pgConfig, pgErr := postgres.LoadConfigFromEnv()
	if pgErr != nil {
		return nil, nil, fmt.Errorf("identity: %w", pgErr)
	}

	issuer, err := jwtauth.LoadIssuerFromEnv()
	if err != nil {
		return nil, nil, err
	}
	verifier, err := jwtauth.LoadVerifierFromEnv()
	if err != nil {
		return nil, nil, err
	}
	// This service registers no authenticated route and reads the public key
	// set only for the check below, which is why it is not dead configuration.
	// A mismatched pair is silent here and loud everywhere it cannot be
	// attributed: Identity mints successfully, every other service rejects
	// every token it mints, and the fault looks like it belongs to the
	// services that are behaving correctly.
	if err := jwtauth.CheckPair(issuer, verifier); err != nil {
		return nil, nil, err
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

	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	passwords := password.New()
	clock := systemClock{}

	module := newIdentityModule(
		application.NewRegisterUser(repo, ids, passwords, clock),
		application.NewAuthenticateUser(repo, passwords, issuer, clock),
	)
	return module, db, nil
}

// closeDB closes db, logging any failure — used on setup-failure paths where
// a different, more relevant error is already being returned to the caller.
func closeDB(db *sql.DB) {
	if err := db.Close(); err != nil {
		logger(componentProcessShutdown).Warn("closing the PostgreSQL pool failed", slog.String("error", err.Error()))
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
			logger(componentUserRegistration).Error("registering the user failed", slog.String("error", err.Error()))
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
		logger(componentUserAuthentication).Error("authenticating the user failed", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, identityErrorResponse{Error: "internal server error"})
		return
	}

	c.JSON(http.StatusOK, authenticateUserResponse{
		AccessToken: result.AccessToken,
		ExpiresAt:   result.ExpiresAt,
	})
}

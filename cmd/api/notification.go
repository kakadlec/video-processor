package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/notification/application"
	"video-processor/internal/notification/domain"
	"video-processor/internal/notification/infrastructure/postgres"
	"video-processor/internal/notification/infrastructure/webhook"
)

// notificationModule wires the Notification bounded context's use cases to
// the HTTP layer.
type notificationModule struct {
	setPreference   *application.SetPreference
	listPreferences *application.ListPreferences
}

func newNotificationModule(setPreference *application.SetPreference, listPreferences *application.ListPreferences) *notificationModule {
	return &notificationModule{setPreference: setPreference, listPreferences: listPreferences}
}

// setupNotification builds the production Notification module from
// environment configuration. Like Identity and Video, every piece is
// required: a missing NOTIFICATION_POSTGRES_DSN fails startup clearly rather
// than leaving a route that answers 500 on every call.
//
// The pool is this context's own even though it points at the same database
// as the other two: the bounded contexts share an instance, not a
// connection, and nothing here may reach a table another context owns.
func setupNotification(ctx context.Context) (*notificationModule, *sql.DB, error) {
	pgConfig, err := postgres.LoadConfigFromEnv()
	if err != nil {
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
		return nil, nil, fmt.Errorf("notification: connect to postgres: %w", err)
	}

	// The same variable and the same parser cmd/notifier reads. One policy
	// with two readers: this half refuses a destination when it is registered,
	// the notifier's half refuses the address when it is dialled, and a
	// deployment whose two processes disagree either stores destinations it
	// can never deliver to or refuses at dial what it accepted at write time.
	// A non-boolean value is fatal here for the reason it is fatal there — the
	// relaxation is a security posture, and neither way of guessing is safe.
	policy, err := webhook.LoadDestinationPolicyFromEnv()
	if err != nil {
		closeDB(db)
		return nil, nil, err
	}
	log.Printf("notification: destination policy: insecure destinations allowed=%t", policy.AllowsInsecure())

	repo := postgres.NewPreferenceRepository(db)
	clock := systemClock{}

	module := newNotificationModule(
		application.NewSetPreference(repo, clock, policy),
		application.NewListPreferences(repo),
	)
	return module, db, nil
}

func (m *notificationModule) registerRoutes(notificationRoutes *gin.RouterGroup) {
	preferences := notificationRoutes.Group("/api/notification-preferences")
	preferences.GET("", m.handleListPreferences)
	preferences.PUT("", m.handleSetPreference)
}

// setPreferenceRequest binds PUT /api/notification-preferences.
//
// Secret is a pointer because omitting it and sending it empty are different
// requests: an omission preserves the stored secret, while an explicit empty
// value is rejected. A plain string collapses the two before the domain can
// tell them apart.
//
// There is deliberately no UserID field. A user_id in the body is ignored
// rather than rejected — the owner is the authenticated caller and nothing
// else, and answering differently for a guessed identifier would report
// whether it exists.
type setPreferenceRequest struct {
	EventType   string  `json:"event_type"`
	Channel     string  `json:"channel"`
	Enabled     bool    `json:"enabled"`
	Destination string  `json:"destination"`
	Secret      *string `json:"secret"`
}

// notificationPreferenceResponse is one stored preference on the wire. It is
// built from application.PreferenceResult, which holds no secret, rather
// than from anything carrying a domain.Secret: that type's MarshalJSON
// returns an error by design, so a response struct holding one would fail at
// encode time and emit a half-written body.
type notificationPreferenceResponse struct {
	EventType   string    `json:"event_type"`
	Channel     string    `json:"channel"`
	Enabled     bool      `json:"enabled"`
	Destination string    `json:"destination"`
	HasSecret   bool      `json:"has_secret"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type listPreferencesResponse struct {
	Preferences []notificationPreferenceResponse `json:"preferences"`
}

type notificationErrorResponse struct {
	Error string `json:"error"`
}

func newNotificationPreferenceResponse(result application.PreferenceResult) notificationPreferenceResponse {
	return notificationPreferenceResponse{
		EventType:   result.EventType,
		Channel:     result.Channel,
		Enabled:     result.Enabled,
		Destination: result.Destination,
		HasSecret:   result.HasSecret,
		CreatedAt:   result.CreatedAt,
		UpdatedAt:   result.UpdatedAt,
	}
}

func (m *notificationModule) handleListPreferences(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, notificationErrorResponse{Error: "missing or malformed authorization header"})
		return
	}

	results, err := m.listPreferences.Execute(c.Request.Context(), userID.String())
	if err != nil {
		log.Printf("list notification preferences: %v", err)
		c.JSON(http.StatusInternalServerError, notificationErrorResponse{Error: "internal server error"})
		return
	}

	// Allocated rather than declared nil: an empty set is 200 with an empty
	// array, and a nil slice serializes to null.
	preferences := make([]notificationPreferenceResponse, 0, len(results))
	for _, result := range results {
		preferences = append(preferences, newNotificationPreferenceResponse(result))
	}
	c.JSON(http.StatusOK, listPreferencesResponse{Preferences: preferences})
}

func (m *notificationModule) handleSetPreference(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, notificationErrorResponse{Error: "missing or malformed authorization header"})
		return
	}

	var req setPreferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, notificationErrorResponse{Error: "invalid request body"})
		return
	}

	result, err := m.setPreference.Execute(c.Request.Context(), application.SetPreferenceInput{
		UserID:      userID.String(),
		EventType:   req.EventType,
		Channel:     req.Channel,
		Enabled:     req.Enabled,
		Destination: req.Destination,
		Secret:      req.Secret,
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidEventType):
			c.JSON(http.StatusBadRequest, notificationErrorResponse{Error: "invalid event type"})
		case errors.Is(err, domain.ErrInvalidChannel):
			c.JSON(http.StatusBadRequest, notificationErrorResponse{Error: "invalid channel"})
		case errors.Is(err, domain.ErrInvalidDestination):
			c.JSON(http.StatusBadRequest, notificationErrorResponse{Error: "invalid destination"})
		// Refused without naming the rule that caught it. The rules enumerate
		// this deployment's internal address space, so a caller who could tell
		// them apart by resubmitting would have been handed a probe.
		case errors.Is(err, domain.ErrDestinationRefused):
			c.JSON(http.StatusBadRequest, notificationErrorResponse{Error: "the destination was refused"})
		case errors.Is(err, domain.ErrInvalidSecret):
			c.JSON(http.StatusBadRequest, notificationErrorResponse{Error: "invalid signing secret"})
		case errors.Is(err, domain.ErrSecretRequired):
			c.JSON(http.StatusBadRequest, notificationErrorResponse{Error: "a signing secret is required to create a preference"})
		default:
			log.Printf("set notification preference: %v", err)
			c.JSON(http.StatusInternalServerError, notificationErrorResponse{Error: "internal server error"})
		}
		return
	}

	c.JSON(http.StatusOK, newNotificationPreferenceResponse(result))
}

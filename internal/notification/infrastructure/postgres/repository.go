package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"video-processor/internal/notification/domain"
)

var _ domain.PreferenceRepository = (*PreferenceRepository)(nil)

// The two statements Set chooses between. Which one runs is decided by the
// intent — did the caller send a secret? — and is therefore knowable before
// any statement runs, so neither branch reads a row first.
//
// Both project whether the secret column is non-empty, never the secret
// itself, so the value is not loadable on any read path — which is what
// makes domain.PreferenceView's missing secret field a guarantee rather
// than a convention. The projection is spelled out in the statements below
// and deliberately not repeated here: gofmt reads a pair of apostrophes in
// a doc comment as a typographic closing quote and rewrites it.
const (
	// A secret was submitted: one upsert whose conflict clause overwrites
	// the stored secret with the submitted one.
	upsertPreferenceQuery = `
		INSERT INTO notification_preferences
		       (user_id, event_type, channel, enabled, destination, secret, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		ON CONFLICT (user_id, event_type, channel) DO UPDATE
		   SET enabled     = EXCLUDED.enabled,
		       destination = EXCLUDED.destination,
		       secret      = EXCLUDED.secret,
		       updated_at  = EXCLUDED.updated_at
		RETURNING enabled, destination, secret <> '' AS has_secret, created_at, updated_at
	`

	// No secret was submitted: one UPDATE that never names the secret
	// column, so there is nothing for it to be preserved *from*. Affecting
	// zero rows is the create-with-no-secret case, and the only signal
	// needed to refuse it.
	//
	// Encoding the omission as '' in an inserted tuple instead would not
	// work: PostgreSQL evaluates NOT NULL and CHECK against the proposed row
	// before it detects the uniqueness conflict, so such a statement aborts
	// on the CHECK and the DO UPDATE branch never runs.
	updatePreferenceQuery = `
		UPDATE notification_preferences
		   SET enabled = $4, destination = $5, updated_at = $6
		 WHERE user_id = $1 AND event_type = $2 AND channel = $3
		RETURNING enabled, destination, secret <> '' AS has_secret, created_at, updated_at
	`

	listPreferencesByUserQuery = `
		SELECT event_type, channel, enabled, destination, secret <> '' AS has_secret, created_at, updated_at
		  FROM notification_preferences
		 WHERE user_id = $1
		 ORDER BY event_type, channel
	`
)

// PreferenceRepository implements domain.PreferenceRepository against
// PostgreSQL using parameterized queries.
type PreferenceRepository struct {
	db *sql.DB
}

// NewPreferenceRepository wires a PreferenceRepository to an already-open
// database handle.
func NewPreferenceRepository(db *sql.DB) *PreferenceRepository {
	return &PreferenceRepository{db: db}
}

// Set stores the preference the intent names in a single atomic statement
// that reads no row beforehand, and returns what is now stored. It returns
// domain.ErrSecretRequired when the intent carried no secret and no
// preference existed to update, having stored nothing.
func (r *PreferenceRepository) Set(ctx context.Context, intent domain.PreferenceIntent, now time.Time) (domain.PreferenceView, error) {
	userID := intent.UserID().String()
	eventType := intent.EventType().String()
	channel := intent.Channel().String()

	secret, submitted := intent.Secret()

	var row *sql.Row
	if submitted {
		row = r.db.QueryRowContext(ctx, upsertPreferenceQuery,
			userID, eventType, channel, intent.Enabled(), intent.Destination().String(), secret.Reveal(), now)
	} else {
		row = r.db.QueryRowContext(ctx, updatePreferenceQuery,
			userID, eventType, channel, intent.Enabled(), intent.Destination().String(), now)
	}

	var (
		enabled          bool
		destinationValue string
		hasSecret        bool
		createdAt        time.Time
		updatedAt        time.Time
	)
	if err := row.Scan(&enabled, &destinationValue, &hasSecret, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if submitted {
				// The upsert always returns a row on both of its branches,
				// so no row here is an anomaly rather than a refusal.
				return domain.PreferenceView{}, fmt.Errorf("notification: set preference: upsert returned no row")
			}
			return domain.PreferenceView{}, domain.ErrSecretRequired
		}
		return domain.PreferenceView{}, fmt.Errorf("notification: set preference: %w", err)
	}

	destination, err := domain.NewDestination(destinationValue)
	if err != nil {
		return domain.PreferenceView{}, fmt.Errorf("notification: stored destination is invalid: %w", err)
	}

	return domain.PreferenceView{
		UserID:      intent.UserID(),
		EventType:   intent.EventType(),
		Channel:     intent.Channel(),
		Enabled:     enabled,
		Destination: destination,
		HasSecret:   hasSecret,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}, nil
}

// ListByUser returns every preference owned by userID, ordered by event type
// then channel. A user who has registered nothing yields an empty slice and
// no error.
func (r *PreferenceRepository) ListByUser(ctx context.Context, userID domain.UserID) ([]domain.PreferenceView, error) {
	rows, err := r.db.QueryContext(ctx, listPreferencesByUserQuery, userID.String())
	if err != nil {
		return nil, fmt.Errorf("notification: list preferences: %w", err)
	}
	defer func() { _ = rows.Close() }()

	views := make([]domain.PreferenceView, 0)
	for rows.Next() {
		var (
			eventTypeValue   string
			channelValue     string
			enabled          bool
			destinationValue string
			hasSecret        bool
			createdAt        time.Time
			updatedAt        time.Time
		)
		if err := rows.Scan(&eventTypeValue, &channelValue, &enabled, &destinationValue, &hasSecret, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("notification: scan preference: %w", err)
		}

		// Every stored value is re-parsed through the domain, so a row
		// written by a future generation — or corrupted by hand — surfaces
		// as an error rather than entering the domain as a valid view.
		eventType, err := domain.ParseEventType(eventTypeValue)
		if err != nil {
			return nil, fmt.Errorf("notification: stored event type is invalid: %w", err)
		}
		channel, err := domain.ParseChannel(channelValue)
		if err != nil {
			return nil, fmt.Errorf("notification: stored channel is invalid: %w", err)
		}
		destination, err := domain.NewDestination(destinationValue)
		if err != nil {
			return nil, fmt.Errorf("notification: stored destination is invalid: %w", err)
		}

		views = append(views, domain.PreferenceView{
			UserID:      userID,
			EventType:   eventType,
			Channel:     channel,
			Enabled:     enabled,
			Destination: destination,
			HasSecret:   hasSecret,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notification: list preferences: %w", err)
	}

	return views, nil
}

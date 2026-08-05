package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"video-processor/internal/identity/domain"
)

var _ domain.UserRepository = (*Repository)(nil)

const uniqueViolationCode = "23505"

// Repository implements domain.UserRepository against PostgreSQL using
// parameterized queries.
type Repository struct {
	db       *sql.DB
	idParser domain.UserIDParser
}

// NewRepository wires a Repository to an already-open database handle and a
// UserIDParser used to reconstruct UserIDs read back from storage.
func NewRepository(db *sql.DB, idParser domain.UserIDParser) *Repository {
	return &Repository{db: db, idParser: idParser}
}

// Create persists a new user. It returns domain.ErrUserAlreadyExists if the
// normalized email is already taken, including under concurrent writes
// (enforced by the unique constraint, not just the application-level check).
func (r *Repository) Create(ctx context.Context, user *domain.User) error {
	const query = `
		INSERT INTO identity_users (id, email, email_normalized, password_hash, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err := r.db.ExecContext(ctx, query,
		user.ID().String(),
		user.Email().String(),
		user.Email().NormalizedForLookup(),
		user.PasswordHash().String(),
		user.CreatedAt(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrUserAlreadyExists
		}
		return fmt.Errorf("identity: create user: %w", err)
	}
	return nil
}

// FindByID looks up a user by ID, returning domain.ErrUserNotFound if none exists.
func (r *Repository) FindByID(ctx context.Context, id domain.UserID) (*domain.User, error) {
	const query = `SELECT id, email, password_hash, created_at FROM identity_users WHERE id = $1`
	return r.scanUser(r.db.QueryRowContext(ctx, query, id.String()))
}

// FindByNormalizedEmail looks up a user by its case-insensitive email key,
// returning domain.ErrUserNotFound if none exists.
func (r *Repository) FindByNormalizedEmail(ctx context.Context, normalizedEmail string) (*domain.User, error) {
	const query = `SELECT id, email, password_hash, created_at FROM identity_users WHERE email_normalized = $1`
	return r.scanUser(r.db.QueryRowContext(ctx, query, normalizedEmail))
}

func (r *Repository) scanUser(row *sql.Row) (*domain.User, error) {
	var (
		idValue    string
		emailValue string
		hashValue  string
		createdAt  time.Time
	)
	if err := row.Scan(&idValue, &emailValue, &hashValue, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, fmt.Errorf("identity: scan user: %w", err)
	}

	id, err := r.idParser.ParseUserID(idValue)
	if err != nil {
		return nil, fmt.Errorf("identity: stored user id is invalid: %w", err)
	}
	email, err := domain.NewEmail(emailValue)
	if err != nil {
		return nil, fmt.Errorf("identity: stored email is invalid: %w", err)
	}
	hash, err := domain.NewPasswordHash(hashValue)
	if err != nil {
		return nil, fmt.Errorf("identity: stored password hash is invalid: %w", err)
	}

	return domain.RestoreUser(id, email, hash, createdAt)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

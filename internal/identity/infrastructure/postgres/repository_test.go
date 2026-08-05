package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/idgen"
	"video-processor/internal/identity/infrastructure/postgres"
)

// testDB skips the test unless IDENTITY_POSTGRES_TEST_DSN is explicitly set,
// per design.md: the default unit-test path must not require a live external
// service. Set the env var and provision a real PostgreSQL instance to
// exercise this adapter end-to-end.
func testDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("IDENTITY_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_TEST_DSN not set; skipping PostgreSQL integration test")
	}

	db, err := postgres.Open(postgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("unexpected error opening database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("unexpected error migrating schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE identity_users"); err != nil {
		t.Fatalf("unexpected error truncating table: %v", err)
	}

	return db
}

func newTestUser(t *testing.T, ids domain.UserIDGenerator, email, password string, createdAt time.Time) *domain.User {
	t.Helper()

	e, err := domain.NewEmail(email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hash, err := domain.NewPasswordHash("hashed:" + password)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	user, err := domain.NewUser(ids, e, hash, createdAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return user
}

func TestRepository_CreateAndFindByID(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	user := newTestUser(t, ids, "user@example.com", "correct-horse", now)

	if err := repo.Create(ctx, user); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, user.ID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found.ID().Equal(user.ID()) {
		t.Fatalf("ID = %v, want %v", found.ID(), user.ID())
	}
	if !found.Email().Equal(user.Email()) {
		t.Fatalf("Email = %v, want %v", found.Email(), user.Email())
	}
	if !found.CreatedAt().Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", found.CreatedAt(), now)
	}
}

func TestRepository_FindByNormalizedEmail_CaseInsensitive(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	user := newTestUser(t, ids, "User@Example.com", "correct-horse", time.Now().UTC())
	if err := repo.Create(ctx, user); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found, err := repo.FindByNormalizedEmail(ctx, "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found.ID().Equal(user.ID()) {
		t.Fatalf("ID = %v, want %v", found.ID(), user.ID())
	}
}

func TestRepository_FindByID_NotFound(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewRepository(db, idgen.New())

	id, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = repo.FindByID(context.Background(), id)
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrUserNotFound)
	}
}

func TestRepository_FindByNormalizedEmail_NotFound(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewRepository(db, idgen.New())

	_, err := repo.FindByNormalizedEmail(context.Background(), "nobody@example.com")
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrUserNotFound)
	}
}

func TestRepository_Create_DuplicateEmail(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	first := newTestUser(t, ids, "dup@example.com", "correct-horse", time.Now().UTC())
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	second := newTestUser(t, ids, "DUP@EXAMPLE.COM", "another-password", time.Now().UTC())
	err := repo.Create(ctx, second)
	if !errors.Is(err, domain.ErrUserAlreadyExists) {
		t.Fatalf("error = %v, want %v", err, domain.ErrUserAlreadyExists)
	}
}

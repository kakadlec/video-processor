package uuid

import (
	"fmt"

	"github.com/google/uuid"

	"video-processor/internal/identity/domain"
)

// Generator is the infrastructure implementation of domain.UserIDGenerator.
type Generator struct{}

func NewGenerator() Generator { return Generator{} }

func (Generator) Generate() (domain.UserID, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate user ID: %w", err)
	}
	return domain.UserID(id.String()), nil
}

// Parse validates an externally supplied ID before it crosses into the domain.
func Parse(value string) (domain.UserID, error) {
	id, err := uuid.Parse(value)
	if err != nil || id.Version() != uuid.Version(4) {
		return "", domain.ErrInvalidUserID
	}
	return domain.UserID(id.String()), nil
}

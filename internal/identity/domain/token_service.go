package domain

import "context"

// TokenService issues and verifies access tokens without coupling Identity to a token format.
type TokenService interface {
	Issue(ctx context.Context, userID UserID) (string, error)
	Verify(ctx context.Context, token string) (UserID, error)
}

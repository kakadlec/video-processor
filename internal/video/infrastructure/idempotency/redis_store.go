// Package idempotency implements domain.IdempotencyStore against a shared
// Redis client, backing POST /upload's content-hash idempotency mechanism.
package idempotency

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"video-processor/internal/video/domain"
)

const (
	// reservationTTL bounds how long an in-flight reservation blocks a
	// second identical request before it's eligible to be reclaimed —
	// comfortably longer than CreateVideoJob's own latency (a single
	// PostgreSQL write), which is the only work that happens while a
	// reservation is held.
	reservationTTL = 5 * time.Minute
	// finalizedTTL is the idempotency window once a key is finalized to a
	// real VideoJobID (design.md Decision 4: fixed 24h, not configurable).
	finalizedTTL = 24 * time.Hour

	// Both states share one string key per IdempotencyKey, and both
	// encode the owning token, so Clear can validate ownership regardless
	// of whether it's called before or after Finalize.
	reservedPrefix = "reserved:"
	finalPrefix    = "final:"
)

// finalizeScript atomically replaces a reservation with its finalized
// VideoJobID only if the key still holds the expected reservation value —
// otherwise it's a no-op (the reservation was reclaimed by someone else).
var finalizeScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current == ARGV[1] then
	redis.call("SET", KEYS[1], ARGV[2], "EX", ARGV[3])
	return 1
end
return 0
`)

// clearScript atomically deletes a key only if its current value (in either
// the reserved or finalized state) was written under the given token.
var clearScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current == false then
	return 0
end
if current == ARGV[1] then
	redis.call("DEL", KEYS[1])
	return 1
end
if string.sub(current, 1, string.len(ARGV[2])) == ARGV[2] then
	redis.call("DEL", KEYS[1])
	return 1
end
return 0
`)

// clearByJobScript atomically deletes a key only if it holds a finalized
// value (ARGV[1] is finalPrefix) that names the given job (ARGV[2] is
// ":<jobID>", the finalized value's trailing component).
//
// Both checks are load-bearing. The suffix alone would be a probabilistic
// test — a reservation token is a UUID, and nothing in the format stops some
// other value from happening to end in the same characters — whereas
// requiring the finalized prefix makes "an in-flight reservation is never
// removed" a property of the format rather than of luck. That matters
// because a reservation belongs to a concurrent request that has not
// finished yet; deleting it would let a duplicate submission through.
var clearByJobScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current == false then
	return 0
end
if string.sub(current, 1, string.len(ARGV[1])) ~= ARGV[1] then
	return 0
end
if string.sub(current, -string.len(ARGV[2])) ~= ARGV[2] then
	return 0
end
redis.call("DEL", KEYS[1])
return 1
`)

// RedisStore implements domain.IdempotencyStore against a shared
// *redis.Client (internal/platform/redis).
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore wraps client as a domain.IdempotencyStore.
func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

// Reserve implements domain.IdempotencyStore.
func (s *RedisStore) Reserve(ctx context.Context, key domain.IdempotencyKey) (string, bool, error) {
	token := uuid.NewString()
	ok, err := s.client.SetNX(ctx, key.String(), reservedPrefix+token, reservationTTL).Result()
	if err != nil {
		return "", false, fmt.Errorf("idempotency: reserve: %w", err)
	}
	if !ok {
		return "", false, nil
	}
	return token, true, nil
}

// Finalize implements domain.IdempotencyStore.
func (s *RedisStore) Finalize(ctx context.Context, key domain.IdempotencyKey, token string, jobID domain.VideoJobID) (bool, error) {
	expected := reservedPrefix + token
	newValue := finalPrefix + token + ":" + jobID.String()
	res, err := finalizeScript.Run(ctx, s.client, []string{key.String()}, expected, newValue, strconv.Itoa(int(finalizedTTL.Seconds()))).Int()
	if err != nil {
		return false, fmt.Errorf("idempotency: finalize: %w", err)
	}
	return res == 1, nil
}

// Lookup implements domain.IdempotencyStore.
func (s *RedisStore) Lookup(ctx context.Context, key domain.IdempotencyKey) (domain.VideoJobID, bool, error) {
	val, err := s.client.Get(ctx, key.String()).Result()
	if errors.Is(err, redis.Nil) {
		return domain.VideoJobID{}, false, nil
	}
	if err != nil {
		return domain.VideoJobID{}, false, fmt.Errorf("idempotency: lookup: %w", err)
	}
	if !strings.HasPrefix(val, finalPrefix) {
		// Either "reserved:..." (in-flight, not yet a real job) or an
		// unrecognized value — either way, nothing to return.
		return domain.VideoJobID{}, false, nil
	}
	rest := strings.TrimPrefix(val, finalPrefix)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return domain.VideoJobID{}, false, nil
	}
	jobID, err := domain.NewVideoJobID(parts[1])
	if err != nil {
		return domain.VideoJobID{}, false, fmt.Errorf("idempotency: lookup: stored job id: %w", err)
	}
	return jobID, true, nil
}

// Clear implements domain.IdempotencyStore.
func (s *RedisStore) Clear(ctx context.Context, key domain.IdempotencyKey, token string) (bool, error) {
	reservedValue := reservedPrefix + token
	finalValuePrefix := finalPrefix + token + ":"
	res, err := clearScript.Run(ctx, s.client, []string{key.String()}, reservedValue, finalValuePrefix).Int()
	if err != nil {
		return false, fmt.Errorf("idempotency: clear: %w", err)
	}
	return res == 1, nil
}

// ClearByJob implements domain.IdempotencyStore.
func (s *RedisStore) ClearByJob(ctx context.Context, key domain.IdempotencyKey, jobID domain.VideoJobID) (bool, error) {
	res, err := clearByJobScript.Run(ctx, s.client, []string{key.String()}, finalPrefix, ":"+jobID.String()).Int()
	if err != nil {
		return false, fmt.Errorf("idempotency: clear by job: %w", err)
	}
	return res == 1, nil
}

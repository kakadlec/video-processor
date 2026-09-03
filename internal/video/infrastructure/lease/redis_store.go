// Package lease implements domain.JobLeaseStore against a shared Redis
// client, backing the worker's liveness signal — the fourth Redis
// responsibility in this service, after idempotency keys, rate limiting, and
// the job-status cache.
package lease

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"video-processor/internal/video/domain"
)

// TTL is how long a lease survives without renewal, and therefore how long a
// job goes unprotected after its worker dies before the sweep can see it as
// abandoned. It is exported because the renewal period on the other side —
// internal/video/application's leaseRenewInterval — has to stay comfortably
// below it, and the two are only meaningful as a pair.
//
// Deliberately a constant rather than an environment variable, on the same
// reasoning as the status cache's fixed entry TTL: the value is a correctness
// margin between renewal and expiry, not a deployment preference, and a
// misconfigured pair would sweep live jobs.
const TTL = 90 * time.Second

// keyPrefix namespaces this package's Redis keys in the
// "<domain>:<purpose>:<id>" shape the rest of the codebase uses.
const keyPrefix = "videojob:lease:"

// acquireScript writes the lease only when the stored value is absent, not a
// lease this package wrote, or names an epoch that is not newer than the
// caller's.
//
// "Not newer" is the predicate, and neither simpler option is correct. SET NX
// would also refuse an *older* stored epoch, leaving a legitimate new holder
// leaseless — and so invisible to the sweep — for that key's remaining TTL.
// An unconditional SET would let a claimant that stalled between winning the
// claim and setting its lease overwrite the lease of the holder that
// superseded it, stopping the rightful holder's renewals and getting a live
// job requeued a second time.
var acquireScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
local stored = tonumber(current)
if current == false or stored == nil or stored <= tonumber(ARGV[1]) then
	redis.call("SET", KEYS[1], ARGV[1], "EX", ARGV[2])
	return 1
end
return 0
`)

// renewScript extends the lease only while the stored value still names the
// caller's epoch, so a superseded holder cannot keep its successor's lease
// alive.
var renewScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current == ARGV[1] then
	redis.call("SET", KEYS[1], ARGV[1], "EX", ARGV[2])
	return 1
end
return 0
`)

// releaseScript drops the lease only while the stored value still names the
// caller's epoch, so a superseded holder cannot release its successor's.
var releaseScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current == ARGV[1] then
	redis.call("DEL", KEYS[1])
	return 1
end
return 0
`)

// RedisStore implements domain.JobLeaseStore against a shared *redis.Client
// (internal/platform/redis).
type RedisStore struct {
	client *redis.Client
}

var _ domain.JobLeaseStore = (*RedisStore)(nil)

// NewRedisStore wraps client as a domain.JobLeaseStore.
func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

func leaseKey(jobID domain.VideoJobID) string {
	return keyPrefix + jobID.String()
}

func epochValue(epoch int64) string {
	return strconv.FormatInt(epoch, 10)
}

// Acquire implements domain.JobLeaseStore. A refusal — the stored value names
// a newer epoch, so this caller has already been superseded — is reported as
// acquired = false, not as an error.
func (s *RedisStore) Acquire(ctx context.Context, jobID domain.VideoJobID, epoch int64) (bool, error) {
	res, err := acquireScript.Run(ctx, s.client, []string{leaseKey(jobID)}, epochValue(epoch), ttlSeconds()).Int()
	if err != nil {
		return false, fmt.Errorf("lease: acquire: %w", err)
	}
	return res == 1, nil
}

// Renew implements domain.JobLeaseStore.
func (s *RedisStore) Renew(ctx context.Context, jobID domain.VideoJobID, epoch int64) (bool, error) {
	res, err := renewScript.Run(ctx, s.client, []string{leaseKey(jobID)}, epochValue(epoch), ttlSeconds()).Int()
	if err != nil {
		return false, fmt.Errorf("lease: renew: %w", err)
	}
	return res == 1, nil
}

// Release implements domain.JobLeaseStore.
func (s *RedisStore) Release(ctx context.Context, jobID domain.VideoJobID, epoch int64) error {
	if err := releaseScript.Run(ctx, s.client, []string{leaseKey(jobID)}, epochValue(epoch)).Err(); err != nil {
		return fmt.Errorf("lease: release: %w", err)
	}
	return nil
}

// Held implements domain.JobLeaseStore: held only when the stored value names
// exactly epoch. A value naming any other epoch belongs to a different run of
// the job and is reported as not held.
//
// A Redis failure is returned as an error and never folded into held = false.
// The sweep's whole posture rests on that distinction: "cannot reach Redis"
// is not evidence a lease expired, and collapsing the two would turn an
// outage into a licence to take over every running job at once.
func (s *RedisStore) Held(ctx context.Context, jobID domain.VideoJobID, epoch int64) (bool, error) {
	val, err := s.client.Get(ctx, leaseKey(jobID)).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lease: held: %w", err)
	}
	return val == epochValue(epoch), nil
}

func ttlSeconds() string {
	return strconv.Itoa(int(TTL.Seconds()))
}

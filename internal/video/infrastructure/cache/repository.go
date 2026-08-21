// Package cache implements domain.VideoJobRepository as a cache-aside/
// write-through decorator around a real (PostgreSQL) repository, backed by
// a shared Redis client — the third and final Phase 4 Redis responsibility,
// after idempotency keys and rate limiting.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"video-processor/internal/video/domain"
)

// keyPrefix namespaces this package's Redis keys, matching the
// "<domain>:<purpose>:<id>" shape already used elsewhere in this codebase
// (e.g. internal/platform/ratelimit's "ratelimit:" + userID.String()).
const keyPrefix = "videojob:status:"

// entryTTL bounds how long any cache entry survives on its own. It is a
// safety net, not the source of correctness: every write to a VideoJob
// already goes through Update's write-through path below, so the cache is
// kept consistent with PostgreSQL on every transition regardless of TTL.
// Deliberately a fixed constant rather than environment-configurable (see
// design.md's Non-Goals) — there is no correctness trade-off to tune.
const entryTTL = 5 * time.Minute

// CachedVideoJobRepository wraps an inner domain.VideoJobRepository (the
// real PostgreSQL adapter) with a Redis-backed cache for FindByID lookups.
// FindByUserID and Create pass straight through, uncached.
type CachedVideoJobRepository struct {
	inner    domain.VideoJobRepository
	client   *redis.Client
	idParser domain.VideoJobIDParser
}

// NewCachedVideoJobRepository wires the decorator to the repository it
// wraps, the shared Redis client, and the VideoJobIDParser used to
// re-validate a job ID read back from the cache — the same parser the
// wrapped repository itself uses to reconstruct rows from PostgreSQL.
func NewCachedVideoJobRepository(inner domain.VideoJobRepository, client *redis.Client, idParser domain.VideoJobIDParser) *CachedVideoJobRepository {
	return &CachedVideoJobRepository{inner: inner, client: client, idParser: idParser}
}

// cachedJobRecord is the JSON-serializable shape written to Redis. It
// mirrors postgres.Repository's own column set exactly: this is a
// repository-level cache of the full aggregate, not a DTO-level cache of
// just a "status" field, since the same cache entry also serves the
// FindByID call every state-transition use case makes before it writes.
type cachedJobRecord struct {
	ID               string    `json:"id"`
	UserID           string    `json:"user_id"`
	OriginalFilename string    `json:"original_filename"`
	StorageKey       string    `json:"storage_key,omitempty"`
	FrameCount       int       `json:"frame_count"`
	ErrorReason      string    `json:"error_reason,omitempty"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
}

func newCachedJobRecord(job *domain.VideoJob) cachedJobRecord {
	return cachedJobRecord{
		ID:               job.ID().String(),
		UserID:           job.UserID().String(),
		OriginalFilename: job.OriginalFilename().String(),
		StorageKey:       job.StorageKey().String(),
		FrameCount:       job.FrameCount(),
		ErrorReason:      job.ErrorReason(),
		Status:           string(job.Status()),
		CreatedAt:        job.CreatedAt(),
	}
}

// toVideoJob reconstructs a *domain.VideoJob from rec, re-validating every
// field through its domain constructor first — exactly mirroring
// postgres.Repository.scanJobRow's own reconstruction discipline, so a
// cache hit can never produce an aggregate that bypasses domain invariants
// (e.g. a corrupted or hand-edited Redis value).
func (rec cachedJobRecord) toVideoJob(idParser domain.VideoJobIDParser) (*domain.VideoJob, error) {
	id, err := idParser.ParseVideoJobID(rec.ID)
	if err != nil {
		return nil, fmt.Errorf("cache: stored video job id is invalid: %w", err)
	}
	userID, err := domain.NewUserID(rec.UserID)
	if err != nil {
		return nil, fmt.Errorf("cache: stored user id is invalid: %w", err)
	}
	filename, err := domain.NewOriginalFilename(rec.OriginalFilename)
	if err != nil {
		return nil, fmt.Errorf("cache: stored original filename is invalid: %w", err)
	}
	var storageKey domain.StorageKey
	if rec.StorageKey != "" {
		storageKey, err = domain.NewStorageKey(rec.StorageKey)
		if err != nil {
			return nil, fmt.Errorf("cache: stored storage key is invalid: %w", err)
		}
	}
	return domain.RestoreVideoJob(id, userID, filename, storageKey, rec.FrameCount, rec.ErrorReason, domain.JobStatus(rec.Status), rec.CreatedAt)
}

func cacheKey(id domain.VideoJobID) string {
	return keyPrefix + id.String()
}

// FindByID implements domain.VideoJobRepository as cache-aside: a cache hit
// is deserialized and returned without querying the inner repository; a
// miss, a Redis error, or a deserialization error all fall back to the
// inner repository (PostgreSQL remains authoritative) and best-effort
// repopulate the cache with its result.
func (r *CachedVideoJobRepository) FindByID(ctx context.Context, id domain.VideoJobID) (*domain.VideoJob, error) {
	if job, ok := r.readCache(ctx, id); ok {
		return job, nil
	}

	job, err := r.inner.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	r.writeCacheIfAbsent(ctx, job)
	return job, nil
}

// deleteMalformedIfUnchangedScript removes key only if its current value
// still matches the malformed one just read (ARGV[1]) — a compare-and-
// delete, mirroring internal/video/infrastructure/idempotency's own
// Lua-script atomicity pattern (finalizeScript/clearScript). This is what
// lets a malformed entry self-heal without reopening the same clobber race
// SET NX was introduced to close: if a concurrent write-through already
// replaced the malformed value with a legitimate one by the time this runs,
// the compare fails and the fresh value is left untouched; only a value
// that's still exactly the malformed one gets deleted, clearing the way for
// this call's own SET NX repopulation just afterward.
var deleteMalformedIfUnchangedScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current == ARGV[1] then
	redis.call("DEL", KEYS[1])
end
return 1
`)

// readCache returns (job, true) on a cache hit that deserializes cleanly.
// Any other outcome (miss, Redis error, corrupted value) returns (nil,
// false) and, for the latter two, logs the error — the caller always falls
// back to the inner repository rather than surfacing a cache-layer problem
// as a lookup failure. A corrupted value (fails to unmarshal or to
// reconstruct into a valid aggregate) is also best-effort removed via
// deleteMalformedIfUnchangedScript, so the caller's subsequent SET NX
// repopulation isn't blocked by a key that will never deserialize.
func (r *CachedVideoJobRepository) readCache(ctx context.Context, id domain.VideoJobID) (*domain.VideoJob, bool) {
	key := cacheKey(id)
	raw, err := r.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false
	}
	if err != nil {
		log.Printf("video: cache: get %s: %v", id.String(), err)
		return nil, false
	}

	var rec cachedJobRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		log.Printf("video: cache: unmarshal %s: %v", id.String(), err)
		r.deleteMalformedIfUnchanged(ctx, key, raw)
		return nil, false
	}
	job, err := rec.toVideoJob(r.idParser)
	if err != nil {
		log.Printf("video: cache: reconstruct %s: %v", id.String(), err)
		r.deleteMalformedIfUnchanged(ctx, key, raw)
		return nil, false
	}
	return job, true
}

func (r *CachedVideoJobRepository) deleteMalformedIfUnchanged(ctx context.Context, key, malformedValue string) {
	if err := deleteMalformedIfUnchangedScript.Run(ctx, r.client, []string{key}, malformedValue).Err(); err != nil {
		log.Printf("video: cache: delete malformed entry %s: %v", key, err)
	}
}

// writeCacheIfAbsent best-effort SET NXs job's current state with entryTTL
// — used only by the miss-repopulation path (FindByID), never by
// write-through (Update). This is deliberately non-destructive: a plain SET
// here would race with a concurrent write-through — a slow FindByID that
// missed and read a job's pre-transition state from the inner repository
// could otherwise overwrite a newer value Update already wrote through,
// letting a subsequent read observe a stale status (caught during review,
// Copilot PR #154; see design.md Decision 2). SET NX can only populate a
// genuinely empty slot, so it can never clobber an entry a concurrent
// write-through already placed; if no such entry exists, it behaves exactly
// like a plain SET would. A failure is logged, never returned — the caller
// already has a valid result from PostgreSQL regardless of whether the
// cache write succeeds.
func (r *CachedVideoJobRepository) writeCacheIfAbsent(ctx context.Context, job *domain.VideoJob) {
	data, err := json.Marshal(newCachedJobRecord(job))
	if err != nil {
		log.Printf("video: cache: marshal %s: %v", job.ID().String(), err)
		return
	}
	if err := r.client.SetNX(ctx, cacheKey(job.ID()), data, entryTTL).Err(); err != nil {
		log.Printf("video: cache: setnx %s: %v", job.ID().String(), err)
	}
}

// Update implements domain.VideoJobRepository as write-through: the inner
// repository's write must succeed first (PostgreSQL is the authority), and
// only then is the cache entry overwritten with job's new state. A cache
// write failure falls back to a best-effort delete, so the entry degrades
// to a miss (falls back to PostgreSQL next time) rather than staying stale;
// either failure is logged, and Update still reports success, since the
// PostgreSQL write it's responsible for already committed.
func (r *CachedVideoJobRepository) Update(ctx context.Context, job *domain.VideoJob) error {
	if err := r.inner.Update(ctx, job); err != nil {
		return err
	}

	data, err := json.Marshal(newCachedJobRecord(job))
	if err != nil {
		log.Printf("video: cache: marshal %s: %v", job.ID().String(), err)
		return nil
	}
	if err := r.client.Set(ctx, cacheKey(job.ID()), data, entryTTL).Err(); err != nil {
		log.Printf("video: cache: write-through set %s: %v", job.ID().String(), err)
		if delErr := r.client.Del(ctx, cacheKey(job.ID())).Err(); delErr != nil {
			log.Printf("video: cache: write-through fallback delete %s: %v", job.ID().String(), delErr)
		}
	}
	return nil
}

// Create passes straight through, uncached — the job's first FindByID call
// naturally cache-aside-populates it.
func (r *CachedVideoJobRepository) Create(ctx context.Context, job *domain.VideoJob) error {
	return r.inner.Create(ctx, job)
}

// FindByUserID passes straight through, uncached — see design.md's
// Non-Goals: the list endpoint is explicitly out of scope for this change.
func (r *CachedVideoJobRepository) FindByUserID(ctx context.Context, userID domain.UserID, offset, limit int) ([]*domain.VideoJob, error) {
	return r.inner.FindByUserID(ctx, userID, offset, limit)
}

package lease_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/lease"
)

// newTestClient skips rather than fails without a reachable Redis, matching
// internal/video/infrastructure/messaging's posture for its broker: the
// suite still passes on a machine with no Redis, covering none of this
// package.
func newTestClient(t *testing.T) *redis.Client {
	t.Helper()

	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; skipping Redis integration test")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

var jobIDCounter int

func newTestJobID(t *testing.T) domain.VideoJobID {
	t.Helper()

	jobIDCounter++
	id, err := domain.NewVideoJobID(fmt.Sprintf("videojob-lease-test-%d-%d", time.Now().UnixNano(), jobIDCounter))
	if err != nil {
		t.Fatalf("NewVideoJobID: %v", err)
	}
	return id
}

func newStore(t *testing.T) (*lease.RedisStore, *redis.Client) {
	t.Helper()

	client := newTestClient(t)
	return lease.NewRedisStore(client), client
}

// TestRedisStore_HeldIsScopedToTheEpoch is the assertion that separates this
// implementation from one that only checks whether the key exists. A lease
// naming a different epoch belongs to a different run of the same job, and
// reading it as "held" would make the sweep skip a job nobody is working on
// — forever, since the successor renews the key on its own epoch.
func TestRedisStore_HeldIsScopedToTheEpoch(t *testing.T) {
	store, client := newStore(t)
	ctx := context.Background()
	jobID := newTestJobID(t)
	t.Cleanup(func() { _ = client.Del(ctx, "videojob:lease:"+jobID.String()).Err() })

	acquired, err := store.Acquire(ctx, jobID, 2)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !acquired {
		t.Fatal("acquired = false, want true for an unheld job")
	}

	held, err := store.Held(ctx, jobID, 2)
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if !held {
		t.Fatal("held = false at the epoch that acquired it, want true")
	}

	for _, other := range []int64{0, 1, 3} {
		held, err := store.Held(ctx, jobID, other)
		if err != nil {
			t.Fatalf("Held at epoch %d: %v", other, err)
		}
		if held {
			t.Fatalf("held = true at epoch %d, want false — the lease names epoch 2", other)
		}
	}
}

func TestRedisStore_AnExpiredLeaseIsNotHeld(t *testing.T) {
	store, client := newStore(t)
	ctx := context.Background()
	jobID := newTestJobID(t)

	if _, err := store.Acquire(ctx, jobID, 0); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Expiry is forced rather than waited out: the production TTL is 90
	// seconds, and a test that slept for it would be the slowest in the
	// suite while asserting nothing more than this does.
	if err := client.PExpire(ctx, "videojob:lease:"+jobID.String(), 20*time.Millisecond).Err(); err != nil {
		t.Fatalf("PExpire: %v", err)
	}
	time.Sleep(60 * time.Millisecond)

	held, err := store.Held(ctx, jobID, 0)
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if held {
		t.Fatal("held = true after the lease expired, want false")
	}
}

// TestRedisStore_AcquireCarriesTheTTL guards the renewal margin from the
// other end: a lease written with no expiry never lapses, so a crashed
// worker's job would stay invisible to the sweep for the life of the Redis
// instance.
func TestRedisStore_AcquireCarriesTheTTL(t *testing.T) {
	store, client := newStore(t)
	ctx := context.Background()
	jobID := newTestJobID(t)
	key := "videojob:lease:" + jobID.String()
	t.Cleanup(func() { _ = client.Del(ctx, key).Err() })

	if _, err := store.Acquire(ctx, jobID, 0); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	ttl, err := client.TTL(ctx, key).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl <= 0 || ttl > lease.TTL {
		t.Fatalf("TTL = %v, want a positive value no greater than %v", ttl, lease.TTL)
	}
}

// TestRedisStore_AcquireReplacesAnOlderEpochAndYieldsToANewerOne covers both
// halves of the "not newer" predicate, and both halves matter. Refusing an
// older stored epoch would leave a legitimate new holder leaseless — and so
// sweepable — for the remaining TTL; overwriting a newer one would let a
// stalled predecessor stop the rightful holder's renewals.
func TestRedisStore_AcquireReplacesAnOlderEpochAndYieldsToANewerOne(t *testing.T) {
	t.Run("replaces an older epoch", func(t *testing.T) {
		store, client := newStore(t)
		ctx := context.Background()
		jobID := newTestJobID(t)
		t.Cleanup(func() { _ = client.Del(ctx, "videojob:lease:"+jobID.String()).Err() })

		if _, err := store.Acquire(ctx, jobID, 1); err != nil {
			t.Fatalf("Acquire at epoch 1: %v", err)
		}
		acquired, err := store.Acquire(ctx, jobID, 2)
		if err != nil {
			t.Fatalf("Acquire at epoch 2: %v", err)
		}
		if !acquired {
			t.Fatal("acquired = false over an older epoch, want true")
		}
		held, err := store.Held(ctx, jobID, 2)
		if err != nil {
			t.Fatalf("Held: %v", err)
		}
		if !held {
			t.Fatal("held = false at epoch 2, want true")
		}
	})

	t.Run("yields to a newer epoch without erroring", func(t *testing.T) {
		store, client := newStore(t)
		ctx := context.Background()
		jobID := newTestJobID(t)
		t.Cleanup(func() { _ = client.Del(ctx, "videojob:lease:"+jobID.String()).Err() })

		if _, err := store.Acquire(ctx, jobID, 2); err != nil {
			t.Fatalf("Acquire at epoch 2: %v", err)
		}
		acquired, err := store.Acquire(ctx, jobID, 1)
		if err != nil {
			t.Fatalf("Acquire at epoch 1 must not error: %v", err)
		}
		if acquired {
			t.Fatal("acquired = true over a newer epoch, want false")
		}
		held, err := store.Held(ctx, jobID, 2)
		if err != nil {
			t.Fatalf("Held: %v", err)
		}
		if !held {
			t.Fatal("held = false at epoch 2, want the newer lease left standing")
		}
	})
}

func TestRedisStore_RenewAndReleaseAtASupersededEpochLeaveTheLeaseAlone(t *testing.T) {
	store, client := newStore(t)
	ctx := context.Background()
	jobID := newTestJobID(t)
	t.Cleanup(func() { _ = client.Del(ctx, "videojob:lease:"+jobID.String()).Err() })

	if _, err := store.Acquire(ctx, jobID, 2); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	renewed, err := store.Renew(ctx, jobID, 1)
	if err != nil {
		t.Fatalf("Renew at a superseded epoch must not error: %v", err)
	}
	if renewed {
		t.Fatal("renewed = true at a superseded epoch, want false")
	}

	if err := store.Release(ctx, jobID, 1); err != nil {
		t.Fatalf("Release at a superseded epoch must not error: %v", err)
	}

	held, err := store.Held(ctx, jobID, 2)
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if !held {
		t.Fatal("held = false at epoch 2, want the successor's lease untouched by a predecessor's release")
	}
}

func TestRedisStore_RenewAndReleaseAtTheHeldEpoch(t *testing.T) {
	store, client := newStore(t)
	ctx := context.Background()
	jobID := newTestJobID(t)
	key := "videojob:lease:" + jobID.String()
	t.Cleanup(func() { _ = client.Del(ctx, key).Err() })

	if _, err := store.Acquire(ctx, jobID, 1); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := client.PExpire(ctx, key, 500*time.Millisecond).Err(); err != nil {
		t.Fatalf("PExpire: %v", err)
	}

	renewed, err := store.Renew(ctx, jobID, 1)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if !renewed {
		t.Fatal("renewed = false at the held epoch, want true")
	}
	ttl, err := client.TTL(ctx, key).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl <= time.Second {
		t.Fatalf("TTL = %v after renewal, want the full lease restored", ttl)
	}

	if err := store.Release(ctx, jobID, 1); err != nil {
		t.Fatalf("Release: %v", err)
	}
	held, err := store.Held(ctx, jobID, 1)
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if held {
		t.Fatal("held = true after Release, want false")
	}
}

// TestRedisStore_AStoreFailureIsReportedAsAnErrorNotAsNotHeld is the
// fail-closed half of the lease posture. The sweep takes a job away from its
// holder on the strength of a "not held", so an unreachable Redis reported
// that way would hand every running job to a second worker at once.
func TestRedisStore_AStoreFailureIsReportedAsAnErrorNotAsNotHeld(t *testing.T) {
	client := newTestClient(t)
	store := lease.NewRedisStore(client)
	jobID := newTestJobID(t)

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	held, err := store.Held(context.Background(), jobID, 0)
	if err == nil {
		t.Fatal("Held returned no error against a closed client, want one")
	}
	if held {
		t.Fatal("held = true alongside an error, want false")
	}
}

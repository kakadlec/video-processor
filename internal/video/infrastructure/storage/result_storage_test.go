package storage_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/storage"
)

// presignTTL is what these tests ask for unless the case is about the TTL
// itself. Any value inside minio-go's accepted [1s, 7d] range works; this one
// is long enough that no assertion races the clock.
const presignTTL = time.Minute

// newResultStorage wires a ResultStorage against the configured test
// instance, provisioning its own bucket so these tests never touch one
// another's objects.
func newResultStorage(t *testing.T) *storage.ResultStorage {
	t.Helper()

	client, cfg := testClient(t)
	bucket := uniqueBucket(t, "result")
	if err := storage.EnsureBucket(context.Background(), client, bucket); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	// Registered before the first Put: these run against a shared MinIO
	// instance whose data directory is a named volume, so a bucket left
	// behind here accumulates across every future suite run. Cleanup runs
	// on failed tests too, which is why it is a t.Cleanup and not a defer
	// after the assertions.
	t.Cleanup(func() { removeBucket(t, client, bucket) })

	return storage.NewResultStorage(client, newPresigner(t, cfg, client, bucket), bucket)
}

// newPresigner builds the presign-only client the way setupVideo does:
// region discovered on the reachable client, then handed in explicitly.
func newPresigner(t *testing.T, cfg storage.Config, client *minio.Client, bucket string) *minio.Client {
	t.Helper()

	region, err := storage.BucketRegion(context.Background(), client, bucket)
	if err != nil {
		t.Fatalf("bucket region: %v", err)
	}
	presigner, err := storage.OpenPresigner(cfg, region)
	if err != nil {
		t.Fatalf("open presigner: %v", err)
	}
	return presigner
}

// fetchUnauthenticated follows an issued URL with a client that sends no
// Authorization header, which is the whole point of the grant: the browser
// redeeming it holds no bearer token and no MinIO credentials.
func fetchUnauthenticated(t *testing.T, signedURL string) *http.Response {
	t.Helper()

	resp, err := http.Get(signedURL)
	if err != nil {
		t.Fatalf("follow issued url: %v", err)
	}
	return resp
}

func testKey(t *testing.T, value string) domain.StorageKey {
	t.Helper()
	key, err := domain.NewStorageKey(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return key
}

func writeLocalZip(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frames.zip")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatalf("write local zip: %v", err)
	}
	return path
}

// TestResultStorage_PresignGet_IssuedURLServesTheStoredBytes is the
// load-bearing test for this adapter. Assertions on the query string pass
// happily against a URL the server answers with 403, so the only thing that
// proves the grant works is following it — with no Authorization header —
// and comparing what comes back to what was stored.
func TestResultStorage_PresignGet_IssuedURLServesTheStoredBytes(t *testing.T) {
	results := newResultStorage(t)
	key := testKey(t, "frames_round-trip.zip")
	content := []byte("PK\x03\x04 pretend this is a zip")

	if err := results.Put(context.Background(), key, writeLocalZip(t, content)); err != nil {
		t.Fatalf("put: %v", err)
	}

	signedURL, _, err := results.PresignGet(context.Background(), key, presignTTL, key.String())
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	resp := fetchUnauthenticated(t, signedURL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("read %q, want %q", got, content)
	}
}

// The signed disposition is what makes a cross-origin result a download: the
// HTML download attribute is ignored for a URL on another origin.
func TestResultStorage_PresignGet_ReturnsAttachmentDispositionNamingTheKey(t *testing.T) {
	results := newResultStorage(t)
	key := testKey(t, "frames_disposition.zip")

	if err := results.Put(context.Background(), key, writeLocalZip(t, []byte("bytes"))); err != nil {
		t.Fatalf("put: %v", err)
	}

	signedURL, _, err := results.PresignGet(context.Background(), key, presignTTL, key.String())
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	resp := fetchUnauthenticated(t, signedURL)
	defer resp.Body.Close()
	disposition := resp.Header.Get("Content-Disposition")
	if !strings.Contains(disposition, "attachment") {
		t.Fatalf("Content-Disposition = %q, want it to request an attachment", disposition)
	}
	if !strings.Contains(disposition, key.String()) {
		t.Fatalf("Content-Disposition = %q, want it to name %q", disposition, key.String())
	}
}

// The disposition is covered by the signature, not merely appended to the
// URL — so a holder cannot re-point the grant at a different filename.
func TestResultStorage_PresignGet_AlteredDispositionIsRefused(t *testing.T) {
	results := newResultStorage(t)
	key := testKey(t, "frames_tampered.zip")

	if err := results.Put(context.Background(), key, writeLocalZip(t, []byte("bytes"))); err != nil {
		t.Fatalf("put: %v", err)
	}

	signedURL, _, err := results.PresignGet(context.Background(), key, presignTTL, key.String())
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	tampered, err := url.Parse(signedURL)
	if err != nil {
		t.Fatalf("parse issued url: %v", err)
	}
	query := tampered.Query()
	query.Set("response-content-disposition", `attachment; filename="somethingelse.zip"`)
	tampered.RawQuery = query.Encode()

	resp := fetchUnauthenticated(t, tampered.String())
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("altering the signed disposition was accepted; the signature does not cover it")
	}
}

// Signed with the library's one-second minimum rather than sleeping out the
// handler's five-minute constant. What is asserted is admission: a request
// that *arrives* after the deadline is refused.
func TestResultStorage_PresignGet_RequestArrivingAfterExpiryIsRefused(t *testing.T) {
	results := newResultStorage(t)
	key := testKey(t, "frames_expired.zip")

	if err := results.Put(context.Background(), key, writeLocalZip(t, []byte("bytes"))); err != nil {
		t.Fatalf("put: %v", err)
	}

	signedURL, expiresAt, err := results.PresignGet(context.Background(), key, time.Second, key.String())
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	time.Sleep(time.Until(expiresAt) + time.Second)

	resp := fetchUnauthenticated(t, signedURL)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a request arriving after the expiry instant was accepted")
	}
}

// TestResultStorage_PresignGet_ReportsTheInstantEncodedInTheSignature is
// deliberately an equality assertion, not a tolerance around now+ttl: a
// tolerance passes against exactly the naive time.Now().Add(ttl) that the
// implementation exists to avoid, because the two differ by well under a
// second.
func TestResultStorage_PresignGet_ReportsTheInstantEncodedInTheSignature(t *testing.T) {
	results := newResultStorage(t)
	key := testKey(t, "frames_expiry.zip")

	if err := results.Put(context.Background(), key, writeLocalZip(t, []byte("bytes"))); err != nil {
		t.Fatalf("put: %v", err)
	}

	// A fractional TTL: minio-go truncates X-Amz-Expires to whole seconds,
	// so the naive computation is wrong by the dropped remainder too.
	signedURL, expiresAt, err := results.PresignGet(context.Background(), key, presignTTL+500*time.Millisecond, key.String())
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	issued, err := url.Parse(signedURL)
	if err != nil {
		t.Fatalf("parse issued url: %v", err)
	}
	signedAt, err := time.Parse("20060102T150405Z", issued.Query().Get("X-Amz-Date"))
	if err != nil {
		t.Fatalf("parse X-Amz-Date: %v", err)
	}
	seconds, err := strconv.Atoi(issued.Query().Get("X-Amz-Expires"))
	if err != nil {
		t.Fatalf("parse X-Amz-Expires: %v", err)
	}

	want := signedAt.Add(time.Duration(seconds) * time.Second)
	if !expiresAt.Equal(want) {
		t.Fatalf("expiresAt = %v, want the signed instant %v", expiresAt.UTC(), want)
	}
}

// The case region discovery exists for. Without a configured region,
// PresignedGetObject reaches for GetBucketLocation against its own endpoint
// first, so a regression that drops the region turns signing into a network
// failure — and this is the deployment shape that makes it visible, since
// the public host is by design one the server cannot reach.
func TestResultStorage_PresignGet_SignsAgainstAnUnresolvablePublicEndpoint(t *testing.T) {
	client, cfg := testClient(t)
	bucket := uniqueBucket(t, "result")
	if err := storage.EnsureBucket(context.Background(), client, bucket); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	t.Cleanup(func() { removeBucket(t, client, bucket) })

	const publicHost = "downloads.invalid:9000"
	cfg.PublicEndpoint = publicHost
	results := storage.NewResultStorage(client, newPresigner(t, cfg, client, bucket), bucket)

	key := testKey(t, "frames_unresolvable.zip")
	signedURL, _, err := results.PresignGet(context.Background(), key, presignTTL, key.String())
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	issued, err := url.Parse(signedURL)
	if err != nil {
		t.Fatalf("parse issued url: %v", err)
	}
	if issued.Host != publicHost {
		t.Fatalf("host = %q, want the configured public endpoint %q", issued.Host, publicHost)
	}
}

func TestResultStorage_Stat_ReportsSizeAndModifiedTime(t *testing.T) {
	results := newResultStorage(t)
	key := testKey(t, "frames_stat.zip")
	content := []byte("some bytes")

	before := time.Now().Add(-1 * time.Minute)
	if err := results.Put(context.Background(), key, writeLocalZip(t, content)); err != nil {
		t.Fatalf("put: %v", err)
	}

	size, modifiedAt, err := results.Stat(context.Background(), key)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", size, len(content))
	}
	if modifiedAt.Before(before) {
		t.Fatalf("modifiedAt = %v, want a time at or after %v", modifiedAt, before)
	}
}

func TestResultStorage_Stat_MissingKeyReportsNotFound(t *testing.T) {
	results := newResultStorage(t)

	if _, _, err := results.Stat(context.Background(), testKey(t, "frames_absent.zip")); !errors.Is(err, domain.ErrResultNotFound) {
		t.Fatalf("error = %v, want it to match domain.ErrResultNotFound", err)
	}
}

// TestResultStorage_UnreachableEndpoint_IsNotReportedAsNotFound is the
// discriminating case for the sentinel: a caller maps ErrResultNotFound to a
// 404, so an outage must never take that path and report someone's existing
// artifact as gone.
func TestResultStorage_UnreachableEndpoint_IsNotReportedAsNotFound(t *testing.T) {
	unreachable := unreachableClient(t)
	results := storage.NewResultStorage(unreachable, unreachable, "bucket")
	key := testKey(t, "frames_unreachable.zip")

	if _, _, err := results.Stat(context.Background(), key); err == nil {
		t.Fatal("expected an error against an unreachable endpoint")
	} else if errors.Is(err, domain.ErrResultNotFound) {
		t.Fatalf("error = %v, must not be reported as not-found", err)
	}

	if err := results.Put(context.Background(), key, writeLocalZip(t, []byte("x"))); err == nil {
		t.Fatal("expected an error against an unreachable endpoint")
	}
}

func TestResultStorage_Put_OverwritesAnExistingObject(t *testing.T) {
	results := newResultStorage(t)
	key := testKey(t, "frames_overwrite.zip")

	if err := results.Put(context.Background(), key, writeLocalZip(t, []byte("first"))); err != nil {
		t.Fatalf("first put: %v", err)
	}
	if err := results.Put(context.Background(), key, writeLocalZip(t, []byte("second-longer"))); err != nil {
		t.Fatalf("second put: %v", err)
	}

	size, _, err := results.Stat(context.Background(), key)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if size != int64(len("second-longer")) {
		t.Fatalf("size = %d, want %d", size, len("second-longer"))
	}
}

func TestResultStorage_Put_MissingLocalFileReturnsError(t *testing.T) {
	results := newResultStorage(t)

	if err := results.Put(context.Background(), testKey(t, "frames_nofile.zip"), filepath.Join(t.TempDir(), "absent.zip")); err == nil {
		t.Fatal("expected an error when the local file does not exist")
	}
}

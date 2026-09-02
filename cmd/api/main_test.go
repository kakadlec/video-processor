package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	platformrabbitmq "video-processor/internal/platform/rabbitmq"
	videoapplication "video-processor/internal/video/application"

	videodomain "video-processor/internal/video/domain"
	videostorage "video-processor/internal/video/infrastructure/storage"
)

// TestMain requires a real ffmpeg on PATH — the same hard dependency the app
// has at runtime. If ffmpeg is absent, the suite exits with code 1 rather
// than skipping silently; use the Docker fallback documented in CLAUDE.md.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Fprintln(os.Stderr, "FATAL: ffmpeg not found in PATH — integration tests require ffmpeg; see CLAUDE.md for the Docker fallback.")
		os.Exit(1)
	}
	// MinIO is as hard a dependency of these tests as ffmpeg is: POST
	// /upload stores its result in a bucket, and GET /download and GET
	// /api/status read it back from one. Failing loudly here beats a suite
	// that silently skips its own core coverage on an unconfigured machine.
	if _, err := videostorage.LoadConfigFromEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v — integration tests require MinIO; see CLAUDE.md for the Docker fallback.\n", err)
		os.Exit(1)
	}
	// RABBITMQ_URL is required to be set, and deliberately not required to
	// be reachable: setupVideo loads the config but never dials, so a suite
	// that demanded a live broker would assert a stronger contract than
	// cmd/api actually has. The relay's behavior against a real broker is
	// covered by internal/video/infrastructure/messaging's own tests.
	if _, err := platformrabbitmq.LoadConfigFromEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v — integration tests require it to be set; see CLAUDE.md for the Docker fallback.\n", err)
		os.Exit(1)
	}
	// go test sets the working directory to this package's own directory
	// (cmd/api), while the app resolves every relative path against the
	// repo root — chdir so tests see the same layout the running binary
	// does, not a shadow copy under cmd/api.
	if err := os.Chdir("../.."); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: failed to chdir to repo root: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// generateTestVideo creates a short synthetic video with ffmpeg's built-in
// testsrc source, so no binary fixture needs to be committed to the repo.
func generateTestVideo(t *testing.T, durationSeconds int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.mp4")
	cmd := exec.Command("ffmpeg",
		"-f", "lavfi",
		"-i", fmt.Sprintf("testsrc=duration=%d:size=320x240:rate=1", durationSeconds),
		"-y", path,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to generate test video: %v\n%s", err, output)
	}
	return path
}

// generateUndecodableVideo writes a file with a valid video extension but
// content ffmpeg cannot decode, to reliably trigger a processing failure
// without relying on a specific ffmpeg error message.
func generateUndecodableVideo(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("isso não é um vídeo de verdade"), 0644); err != nil {
		t.Fatalf("failed to write undecodable video file: %v", err)
	}
	return path
}

// startTestServer spins up the real router (real handlers, real ffmpeg calls,
// real filesystem) on an in-process httptest server, backed by a configured
// (in-memory) identity module, and returns a bearer token valid for that
// server's fixed test user.
func startTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv, token, _ := startTestServerWithModule(t)
	return srv, token
}

// testStatusUserID is the user startTestServer mints its token for, needed
// by callers that seed a job for that same caller.
const testStatusUserID = "3fa85f64-5717-4562-b3fc-2c963f66afa6"

// startTestServerWithModule is startTestServer plus the videoModule it wired.
// Callers that need a job in a terminal state need the module: POST /upload
// only queues now, so a test of GET /download/:filename or GET /api/status
// has to reach past the HTTP surface and drive the job there itself.
func startTestServerWithModule(t *testing.T) (*httptest.Server, string, *videoModule) {
	t.Helper()
	identity, tokens := newTestIdentityModuleWithTokens(t)
	video := newTestVideoModule(t)
	srv := httptest.NewServer(setupRouter(identity, video, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)

	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	return srv, token, video
}

// uploadVideo performs an authenticated multipart upload of the file at
// videoPath under the given filename (the filename controls which extension
// the server sees).
func uploadVideo(t *testing.T, baseURL, token, videoPath, filename string) (*http.Response, ProcessingResult) {
	t.Helper()
	body, contentType := videoFormBody(t, videoPath, filename)
	return doUpload(t, baseURL, token, body, contentType)
}

// videoFormBody builds the multipart body uploadVideo sends, separately from
// sending it, so the two response shapes POST /upload can answer with — the
// 202 acknowledgement and the rejection — each get their own decoder over
// the same request.
func videoFormBody(t *testing.T, videoPath, filename string) (*bytes.Buffer, string) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("video", filename)
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	file, err := os.Open(videoPath)
	if err != nil {
		t.Fatalf("failed to open test video: %v", err)
	}
	defer file.Close()
	if _, err := io.Copy(part, file); err != nil {
		t.Fatalf("failed to copy video into form: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}
	return body, writer.FormDataContentType()
}

// uploadEmptyForm sends an authenticated multipart form with no "video"
// field, simulating a client that forgot to attach a file.
func uploadEmptyForm(t *testing.T, baseURL, token string) (*http.Response, ProcessingResult) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	return doUpload(t, baseURL, token, body, writer.FormDataContentType())
}

func doUpload(t *testing.T, baseURL, token string, body *bytes.Buffer, contentType string) (*http.Response, ProcessingResult) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/upload", body)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload request failed: %v", err)
	}
	defer resp.Body.Close()

	var result ProcessingResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	return resp, result
}

// doUploadRaw is doUpload without a decoder, for callers that know which of
// the two response shapes they expect.
func doUploadRaw(t *testing.T, baseURL, token string, body *bytes.Buffer, contentType string) (*http.Response, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/upload", body)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload request failed: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return resp, raw
}

// uploadVideoAccepted is uploadVideo for the success path, which no longer
// speaks ProcessingResult: POST /upload queues the job and answers 202 with
// the acknowledgement. It fails the test on any other status, so a caller
// asserting on the acknowledgement never reads zero values out of a body
// that was actually a rejection.
func uploadVideoAccepted(t *testing.T, baseURL, token, videoPath, filename string) uploadAcceptedResponse {
	t.Helper()

	body, contentType := videoFormBody(t, videoPath, filename)
	resp, raw := doUploadRaw(t, baseURL, token, body, contentType)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload status = %d, want %d (body: %s)", resp.StatusCode, http.StatusAccepted, raw)
	}

	var accepted uploadAcceptedResponse
	if err := json.Unmarshal(raw, &accepted); err != nil {
		t.Fatalf("failed to decode the acknowledgement %s: %v", raw, err)
	}
	if accepted.JobID == "" {
		t.Fatalf("the 202 carries no job_id: %s", raw)
	}
	if want := videoJobStatusPath(accepted.JobID); accepted.StatusURL != want {
		t.Fatalf("status_url = %q, want %q", accepted.StatusURL, want)
	}
	return accepted
}

// seedCompletedJob drives a job to completed and stores a small result
// object for it, returning the result's storage key.
//
// It stands in for cmd/worker, which owns that half of the lifecycle now,
// and deliberately runs no ffmpeg: GET /download/:filename and GET
// /api/status care about entitlement, the presigned-URL shape, and the
// object's own size and creation time — none of which depend on the bytes
// having come from a frame extraction. The real pipeline is covered where it
// lives, by internal/video/infrastructure/ffmpeg's and
// internal/video/application's tests, and end to end by cmd/worker's.
func seedCompletedJob(t *testing.T, m *videoModule, userID string) string {
	t.Helper()

	ctx := context.Background()
	created, err := m.createVideoJob.Execute(ctx, videoapplication.CreateVideoJobInput{
		UserID:           userID,
		OriginalFilename: "seeded.mp4",
		SourceKey:        "uploads/" + uuid.NewString() + "_seeded.mp4",
		ContentHash:      strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("seed: create job: %v", err)
	}
	if _, err := m.enqueueVideoJob.Execute(ctx, created.JobID); err != nil {
		t.Fatalf("seed: enqueue job: %v", err)
	}
	if _, err := videoapplication.NewStartProcessing(m.jobs, m.idsFor).Execute(ctx, created.JobID); err != nil {
		t.Fatalf("seed: start processing: %v", err)
	}

	key := "frames_" + created.JobID + ".zip"
	storageKey, err := videodomain.NewStorageKey(key)
	if err != nil {
		t.Fatalf("seed: storage key: %v", err)
	}
	if err := m.results.Put(ctx, storageKey, writeSeedZip(t)); err != nil {
		t.Fatalf("seed: store result: %v", err)
	}
	if _, err := videoapplication.NewCompleteJob(m.jobs, m.idsFor).Execute(ctx, videoapplication.CompleteJobInput{
		JobID:      created.JobID,
		StorageKey: key,
		FrameCount: seedFrameCount,
	}); err != nil {
		t.Fatalf("seed: complete job: %v", err)
	}
	return key
}

// seedFrameCount is what seedCompletedJob records on the job and how many
// entries writeSeedZip puts in the archive, so the two agree.
const seedFrameCount = 2

// writeSeedZip writes a real (tiny) zip to a temp file and returns its path,
// so the seeded result is a valid archive a download test can open.
func writeSeedZip(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frames.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create seed zip: %v", err)
	}
	w := zip.NewWriter(f)
	for i := 1; i <= seedFrameCount; i++ {
		entry, err := w.Create(fmt.Sprintf("frame_%04d.png", i))
		if err != nil {
			t.Fatalf("create seed zip entry: %v", err)
		}
		if _, err := entry.Write([]byte("not really a png")); err != nil {
			t.Fatalf("write seed zip entry: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close seed zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close seed zip: %v", err)
	}
	return path
}

// TestUpload_ValidVideo_QueuesTheJobAndAnswers202 is what POST /upload
// promises now: the request ends at the queue. The acknowledgement names the
// job and where to poll it, the job is queued rather than finished, and
// nothing was extracted — the frames, the zip, and the completed job are
// cmd/worker's, and are asserted there.
func TestUpload_ValidVideo_QueuesTheJobAndAnswers202(t *testing.T) {
	srv, token := startTestServer(t)
	videoPath := generateTestVideo(t, 3)

	accepted := uploadVideoAccepted(t, srv.URL, token, videoPath, "test-video.mp4")

	if accepted.Status != string(videodomain.JobStatusQueued) {
		t.Fatalf("status = %q, want %q", accepted.Status, videodomain.JobStatusQueued)
	}

	// The status URL the response handed out has to resolve, or the client
	// has an acknowledgement it cannot act on.
	resp := getWithAuthorization(t, srv.URL+accepted.StatusURL, "Bearer "+token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want %d", accepted.StatusURL, resp.StatusCode, http.StatusOK)
	}
	var job videoJobStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		t.Fatalf("failed to decode job: %v", err)
	}
	if job.JobID != accepted.JobID {
		t.Fatalf("job id = %q, want %q", job.JobID, accepted.JobID)
	}
	if job.Status != string(videodomain.JobStatusQueued) {
		t.Fatalf("job status = %q, want %q — POST /upload must not process", job.Status, videodomain.JobStatusQueued)
	}

	// Nothing was extracted for this job: no downloaded source copy
	// (temp/<jobID>_source), no per-job frame directory (temp/<jobID>),
	// no zip (temp/<jobID>.zip) — one glob covers all three.
	//
	// Scoped to this job's id rather than to the whole directory. temp/ is
	// resolved relative to the repository root, which cmd/worker's TestMain
	// chdirs to as well, and `go test ./...` runs the two packages'
	// binaries in parallel — so an unscoped glob reports that package's
	// in-flight scratch as this handler's leftovers.
	strays, err := filepath.Glob(filepath.Join("temp", accepted.JobID+"*"))
	if err != nil {
		t.Fatalf("failed to glob temp dir: %v", err)
	}
	if len(strays) > 0 {
		t.Fatalf("POST /upload left %v under temp/ — the API extracts nothing", strays)
	}
}

// TestDownload_EveryRejectionIsByteIdentical is the discriminating test for
// the endpoint's information-leak property: a caller must not be able to
// tell "no such artifact" from "someone else's artifact" from "not a key at
// all". Comparing status codes alone would pass even if the bodies differed.
func TestDownload_EveryRejectionIsByteIdentical(t *testing.T) {
	identity, tokens := newTestIdentityModuleWithTokens(t)
	video := newTestVideoModule(t)
	srv := httptest.NewServer(setupRouter(identity, video, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)

	userA, tokenA := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, tokenB := issueTestToken(t, tokens, "550e8400-e29b-41d4-a716-446655440000")

	key := seedCompletedJob(t, video, userA.String())

	cases := []struct {
		name  string
		path  string
		token string
	}{
		{"another user's artifact", "/download/" + key, tokenB},
		{"a key belonging to no job", "/download/frames_3fa85f64-5717-4562-b3fc-2c963f66afff.zip", tokenA},
		{"not a result key at all", "/download/whatever.txt", tokenA},
		{"a key whose embedded id is malformed", "/download/frames_not-a-uuid.zip", tokenA},
	}

	var fingerprints []rejectionFingerprint
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := getWithAuthorization(t, srv.URL+tc.path, "Bearer "+tc.token)
			defer resp.Body.Close()
			fingerprints = append(fingerprints, fingerprintRejection(t, resp))
		})
	}

	for i := 1; i < len(fingerprints); i++ {
		if fingerprints[i] != fingerprints[0] {
			t.Fatalf("rejections differ: %+v vs %+v — a caller could distinguish these cases", fingerprints[0], fingerprints[i])
		}
	}
}

// rejectionFingerprint is everything a caller could read off a rejected
// download to tell one cause from another. Named fields rather than the whole
// header map: Date and Content-Length legitimately vary, and comparing them
// would fail for reasons that have nothing to do with the leak this guards.
type rejectionFingerprint struct {
	status       int
	body         string
	contentType  string
	cacheControl string
}

func fingerprintRejection(t *testing.T, resp *http.Response) rejectionFingerprint {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, http.StatusNotFound, body)
	}
	return rejectionFingerprint{
		status:       resp.StatusCode,
		body:         string(body),
		contentType:  resp.Header.Get("Content-Type"),
		cacheControl: resp.Header.Get("Cache-Control"),
	}
}

// downloadIssuance is what GET /download/:filename returns now that it grants
// access instead of serving it.
type downloadIssuance struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

func issueDownload(t *testing.T, baseURL, token, filename string) downloadIssuance {
	t.Helper()

	resp := getWithAuthorization(t, baseURL+"/download/"+filename, "Bearer "+token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("issuance status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q — the body is a credential", cc, "no-store")
	}

	var issued downloadIssuance
	if err := json.NewDecoder(resp.Body).Decode(&issued); err != nil {
		t.Fatalf("decode issuance: %v", err)
	}
	if issued.URL == "" {
		t.Fatal("issuance carries no url")
	}
	if _, err := time.Parse(time.RFC3339, issued.ExpiresAt); err != nil {
		t.Fatalf("expires_at %q is not RFC3339: %v", issued.ExpiresAt, err)
	}
	return issued
}

// followIssuedURL redeems a grant the way a browser does: no Authorization
// header, straight to storage.
func followIssuedURL(t *testing.T, signedURL string) []byte {
	t.Helper()

	resp, err := http.Get(signedURL)
	if err != nil {
		t.Fatalf("follow issued url: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("following the issued url: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read issued url body: %v", err)
	}
	return body
}

// TestDownload_MissingObjectIsRejectedLikeEveryOtherCase covers what the
// pre-issuance Stat exists for. Signing is offline, so without that Stat this
// request would succeed and the failure would surface as MinIO's own 404 —
// a different origin, an XML body — instead of this endpoint's rejection.
func TestDownload_MissingObjectIsRejectedLikeEveryOtherCase(t *testing.T) {
	srv, token, userID, module, _, inspector := startSourceStorageTestServer(t)

	key := seedCompletedJob(t, module, userID)

	// Entitlement still passes: the VideoJob row is untouched and still
	// records this key. Only the object is gone.
	inspector.removeObject(t, key)

	missing := getWithAuthorization(t, srv.URL+"/download/"+key, "Bearer "+token)
	defer missing.Body.Close()

	baseline := getWithAuthorization(t, srv.URL+"/download/whatever.txt", "Bearer "+token)
	defer baseline.Body.Close()

	if got, want := fingerprintRejection(t, missing), fingerprintRejection(t, baseline); got != want {
		t.Fatalf("deleted-object rejection %+v differs from %+v", got, want)
	}
}

// TestDownload_StorageFailuresAreRejectedLikeEveryOtherCase drives the two
// branches an outage reaches. Both are hard to provoke against a real bucket
// and easy to get wrong: they are the paths whose underlying error names the
// endpoint and the bucket, so they are exactly the ones that must render as
// the same opaque rejection as a malformed key.
func TestDownload_StorageFailuresAreRejectedLikeEveryOtherCase(t *testing.T) {
	identity, tokens := newTestIdentityModuleWithTokens(t)
	repo := newInMemoryVideoJobRepository()
	results := newFakeResultStorage()
	module, _ := newIdempotencyTestVideoModuleWithRepoAndStorage(repo, results)
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)

	userID, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	key := seedCompletedJob(t, module, userID.String())

	baseline := getWithAuthorization(t, srv.URL+"/download/whatever.txt", "Bearer "+token)
	defer baseline.Body.Close()
	want := fingerprintRejection(t, baseline)

	// Deliberately not ErrResultNotFound: this is the "storage is broken"
	// leg, the one that logs. The not-found leg has its own test against a
	// real bucket.
	outage := errors.New("stat failed against https://minio.internal:9000/some-bucket")

	cases := []struct {
		name string
		set  func()
	}{
		{"stat fails", func() { results.statErr = outage }},
		// Unreachable in production — the TTL is a constant inside the
		// library's accepted range and the key is validated before it — but
		// it must not be the one path that leaks an endpoint name into a
		// body.
		{"presigning fails", func() { results.presignErr = outage }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results.mu.Lock()
			results.statErr = nil
			results.presignErr = nil
			tc.set()
			results.mu.Unlock()
			t.Cleanup(func() {
				results.mu.Lock()
				results.statErr = nil
				results.presignErr = nil
				results.mu.Unlock()
			})

			failed := getWithAuthorization(t, srv.URL+"/download/"+key, "Bearer "+token)
			defer failed.Body.Close()

			if got := fingerprintRejection(t, failed); got != want {
				t.Fatalf("rejection %+v differs from %+v", got, want)
			}
		})
	}
}

// TestDownload_NonOwnerReceivesNoGrant is the complement to the fingerprint
// comparison: it is not enough that a rejection looks like the others, it
// must also not have minted anything the caller could use.
func TestDownload_NonOwnerReceivesNoGrant(t *testing.T) {
	identity, tokens := newTestIdentityModuleWithTokens(t)
	video := newTestVideoModule(t)
	srv := httptest.NewServer(setupRouter(identity, video, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)

	userA, tokenA := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, tokenB := issueTestToken(t, tokens, "550e8400-e29b-41d4-a716-446655440000")

	key := seedCompletedJob(t, video, userA.String())

	resp := getWithAuthorization(t, srv.URL+"/download/"+key, "Bearer "+tokenB)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	var issued downloadIssuance
	if err := json.Unmarshal(body, &issued); err == nil && issued.URL != "" {
		t.Fatalf("a rejected request produced a usable url: %q", issued.URL)
	}
	if strings.Contains(string(body), "X-Amz-Signature") {
		t.Fatalf("rejection body %q carries signature material", body)
	}

	// And the owner still gets one that actually redeems, so the assertion
	// above is not passing because issuance is broken for everyone.
	owner := issueDownload(t, srv.URL, tokenA, key)
	if owner.URL == "" {
		t.Fatal("owner issuance carries no url")
	}
	zipBytes := followIssuedURL(t, owner.URL)
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("the redeemed grant did not serve a valid zip: %v", err)
	}
	if len(zr.File) != seedFrameCount {
		t.Fatalf("redeemed zip has %d entries, want %d", len(zr.File), seedFrameCount)
	}
}

func TestUpload_UnsupportedExtension_Rejected(t *testing.T) {
	srv, token := startTestServer(t)

	tmpFile := filepath.Join(t.TempDir(), "not-a-video.txt")
	if err := os.WriteFile(tmpFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	resp, result := uploadVideo(t, srv.URL, token, tmpFile, "not-a-video.txt")

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d", resp.StatusCode)
	}
	if result.Success {
		t.Fatal("expected success=false for an unsupported extension")
	}
	const wantMessage = "Formato de arquivo não suportado. Use: mp4, avi, mov, mkv"
	if result.Message != wantMessage {
		t.Fatalf("expected message %q, got %q", wantMessage, result.Message)
	}
	if result.ZipPath != "" {
		t.Fatal("expected no zip to be created for a rejected upload")
	}
}

// TestUpload_VideoFieldNotFirst_StillFound proves the "video" part doesn't
// need to be the first field in the multipart body: videoFilePart's
// NextPart loop must skip (drain and close) any preceding non-matching
// part rather than assuming the file is the very first one.
func TestUpload_VideoFieldNotFirst_StillFound(t *testing.T) {
	srv, token := startTestServer(t)
	videoPath := generateTestVideo(t, 1)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("note", "this field comes before the video part"); err != nil {
		t.Fatalf("failed to write leading field: %v", err)
	}
	part, err := writer.CreateFormFile("video", "test-video.mp4")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	file, err := os.Open(videoPath)
	if err != nil {
		t.Fatalf("failed to open test video: %v", err)
	}
	defer file.Close()
	if _, err := io.Copy(part, file); err != nil {
		t.Fatalf("failed to copy video into form: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	resp, raw := doUploadRaw(t, srv.URL, token, body, writer.FormDataContentType())

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected HTTP 202 when the video part follows another field, got %d (body: %s)", resp.StatusCode, raw)
	}
}

// countingReader wraps an io.Reader and records the total bytes actually
// read from it, so a test can assert on how much of a request body a
// handler consumed rather than just on the response it produced.
type countingReader struct {
	r     io.Reader
	total atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.total.Add(int64(n))
	return n, err
}

// TestUpload_LargeInvalidExtension_RejectsWithoutReadingFullBody proves the
// fix for the bug where a large, invalid-extension upload took as long to
// reject as a real upload takes to process: c.Request.FormFile calls
// net/http's own ParseMultipartForm internally (bypassing Gin's own
// FormFile/MultipartForm wrapper, so Gin's MaxMultipartMemory setting is
// never consulted), which reads the entire body up front — spilling past
// net/http's own 32MiB defaultMaxMemory to a temp file — before the
// filename is even available. A test asserting only on the response status
// would pass identically whether or not that full read happens, so this
// asserts on bytes actually consumed from the request body instead — the
// discriminating signal.
func TestUpload_LargeInvalidExtension_RejectsWithoutReadingFullBody(t *testing.T) {
	identity, tokens := newTestIdentityModuleWithTokens(t)
	router := setupRouter(identity, newTestVideoModule(t), alwaysAllowRateLimiter{})
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	var headerBuf bytes.Buffer
	mw := multipart.NewWriter(&headerBuf)
	if _, err := mw.CreateFormFile("video", "not-a-video.txt"); err != nil {
		t.Fatalf("failed to write multipart part header: %v", err)
	}
	// headerBuf now holds the multipart preamble plus the "video" part's
	// Content-Disposition/Content-Type headers, with none of the part's
	// body written yet (and the multipart writer deliberately never
	// closed) — a fixed handler should never need to read past this.

	const fillerSize = 64 << 20 // far past net/http's own 32MiB defaultMaxMemory
	body := io.MultiReader(bytes.NewReader(headerBuf.Bytes()), io.LimitReader(zeroReader{}, fillerSize))
	counting := &countingReader{r: body}

	req := httptest.NewRequest(http.MethodPost, "/upload", counting)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	const readBudget = 1 << 20 // generous allowance for header/bufio read-ahead
	if got := counting.total.Load(); got >= readBudget {
		t.Fatalf("expected rejection before reading the %d-byte filler body, but the handler read %d bytes", fillerSize, got)
	}
}

// zeroReader yields an endless stream of zero bytes, standing in for a
// large file body that a fixed handler should never actually read.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestUpload_MissingFileField_Rejected(t *testing.T) {
	srv, token := startTestServer(t)

	resp, result := uploadEmptyForm(t, srv.URL, token)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d", resp.StatusCode)
	}
	if result.Success {
		t.Fatal("expected success=false when no video field is sent")
	}
	// Locks in that a terminal NextPart io.EOF (no matching part found) is
	// mapped to http.ErrMissingFile, matching FormFile's own error exactly
	// — not passed through as a raw "EOF", which would silently change
	// this response's wording.
	const wantMessage = "Erro ao receber arquivo: http: no such file"
	if result.Message != wantMessage {
		t.Fatalf("expected message %q, got %q", wantMessage, result.Message)
	}
}

// TestStatus_ListsCompletedResults pins that GET /api/status lists a
// caller's finished artifacts — and, by consequence, that it does not list a
// job POST /upload merely queued: the upload below is the negative half, and
// only the seeded completed job may appear.
func TestStatus_ListsCompletedResults(t *testing.T) {
	srv, token, video := startTestServerWithModule(t)
	videoPath := generateTestVideo(t, 1)

	queued := uploadVideoAccepted(t, srv.URL, token, videoPath, "status-check.mp4")
	key := seedCompletedJob(t, video, testStatusUserID)

	resp := getWithAuthorization(t, srv.URL+"/api/status", "Bearer "+token)
	defer resp.Body.Close()

	var status struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}

	found := false
	for _, f := range status.Files {
		if f.Filename == key {
			found = true
		}
		if f.Filename == "frames_"+queued.JobID+".zip" {
			t.Fatalf("status listed %q for a job that is only queued", f.Filename)
		}
	}
	if !found {
		t.Fatalf("expected %q in status listing, got %+v", key, status.Files)
	}
}

func TestFrontend_StaticRoutes_ServeExpectedContent(t *testing.T) {
	srv, _ := startTestServer(t)

	cases := []struct {
		path                string
		wantContentType     string
		wantContentContains string
	}{
		{"/", "text/html", "FIAP X - Processador de Vídeos"},
		{"/styles.css", "text/css", ".upload-form"},
		{"/app.js", "application/javascript", "function loadFilesList"},
	}

	for _, tc := range cases {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s failed: %v", tc.path, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: expected HTTP 200, got %d", tc.path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !bytes.Contains([]byte(ct), []byte(tc.wantContentType)) {
			t.Fatalf("GET %s: expected Content-Type containing %q, got %q", tc.path, tc.wantContentType, ct)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("GET %s: failed to read body: %v", tc.path, err)
		}
		if !bytes.Contains(body, []byte(tc.wantContentContains)) {
			t.Fatalf("GET %s: expected body to contain %q", tc.path, tc.wantContentContains)
		}
	}
}

func TestDownload_NonexistentFile_Returns404(t *testing.T) {
	srv, token := startTestServer(t)

	resp := getWithAuthorization(t, srv.URL+"/download/this-file-does-not-exist.zip", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected HTTP 404, got %d", resp.StatusCode)
	}
}

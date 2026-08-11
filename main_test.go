package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMain requires a real ffmpeg on PATH — the same hard dependency the app
// has at runtime. If ffmpeg is absent, the suite exits with code 1 rather
// than skipping silently; use the Docker fallback documented in CLAUDE.md.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Fprintln(os.Stderr, "FATAL: ffmpeg not found in PATH — integration tests require ffmpeg; see CLAUDE.md for the Docker fallback.")
		os.Exit(1)
	}
	createDirs()
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

// assertTempDirClean fails the test if temp/ has any leftover per-request
// directories, which would mean the defer os.RemoveAll cleanup didn't run.
func assertTempDirClean(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir("temp")
	if err != nil {
		t.Fatalf("failed to read temp dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == ".gitkeep" {
			continue
		}
		t.Fatalf("expected temp/ to have no leftover entries, found: %s", e.Name())
	}
}

// startTestServer spins up the real router (real handlers, real ffmpeg calls,
// real filesystem) on an in-process httptest server, backed by a configured
// (in-memory) identity module, and returns a bearer token valid for that
// server's fixed test user.
func startTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	module, tokens := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouterWithIdentity(module))
	t.Cleanup(srv.Close)

	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	return srv, token
}

// uploadVideo performs an authenticated multipart upload of the file at
// videoPath under the given filename (the filename controls which extension
// the server sees).
func uploadVideo(t *testing.T, baseURL, token, videoPath, filename string) (*http.Response, ProcessingResult) {
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

	return doUpload(t, baseURL, token, body, writer.FormDataContentType())
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

// cleanupOutputZip removes a zip (and its ownership sidecar, if any) created
// under outputs/ during a test, so repeated `go test` runs don't accumulate
// files.
func cleanupOutputZip(t *testing.T, zipFilename string) {
	t.Helper()
	if zipFilename == "" {
		return
	}
	t.Cleanup(func() {
		os.Remove(filepath.Join("outputs", zipFilename))
		os.Remove(filepath.Join("outputs", zipFilename+artifactOwnerSuffix))
	})
}

func TestUpload_ValidVideo_ExtractsFramesAndZip(t *testing.T) {
	srv, token := startTestServer(t)
	videoPath := generateTestVideo(t, 3)

	resp, result := uploadVideo(t, srv.URL, token, videoPath, "test-video.mp4")
	cleanupOutputZip(t, result.ZipPath)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", resp.StatusCode)
	}
	if !result.Success {
		t.Fatalf("expected success=true, got message: %s", result.Message)
	}
	if result.FrameCount != 3 {
		t.Fatalf("expected frame_count=3 for a 3s video, got %d", result.FrameCount)
	}
	if result.ZipPath == "" {
		t.Fatal("expected a non-empty zip_path")
	}

	// The zip must be downloadable and contain exactly the reported frames.
	downloadResp := getWithAuthorization(t, srv.URL+"/download/"+result.ZipPath, "Bearer "+token)
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 downloading zip, got %d", downloadResp.StatusCode)
	}

	zipBytes, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		t.Fatalf("failed to read zip body: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("downloaded file is not a valid zip: %v", err)
	}
	if len(zr.File) != result.FrameCount {
		t.Fatalf("expected zip to contain %d frames, got %d", result.FrameCount, len(zr.File))
	}

	// The original upload must be cleaned up after a successful run.
	leftovers, err := filepath.Glob(filepath.Join("uploads", "*_test-video.mp4"))
	if err != nil {
		t.Fatalf("failed to glob uploads dir: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("expected uploaded file to be removed, found: %v", leftovers)
	}
}

func TestUpload_UnsupportedExtension_Rejected(t *testing.T) {
	srv, token := startTestServer(t)

	tmpFile := filepath.Join(t.TempDir(), "not-a-video.txt")
	if err := os.WriteFile(tmpFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	resp, result := uploadVideo(t, srv.URL, token, tmpFile, "not-a-video.txt")
	cleanupOutputZip(t, result.ZipPath)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d", resp.StatusCode)
	}
	if result.Success {
		t.Fatal("expected success=false for an unsupported extension")
	}
	if result.ZipPath != "" {
		t.Fatal("expected no zip to be created for a rejected upload")
	}
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
}

func TestStatus_ListsProcessedZip(t *testing.T) {
	srv, token := startTestServer(t)
	videoPath := generateTestVideo(t, 1)

	_, result := uploadVideo(t, srv.URL, token, videoPath, "status-check.mp4")
	cleanupOutputZip(t, result.ZipPath)
	if !result.Success {
		t.Fatalf("setup upload failed: %s", result.Message)
	}

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
		if f.Filename == result.ZipPath {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %q in status listing, got %+v", result.ZipPath, status.Files)
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

func TestProcessing_Failure_CleansTempDir(t *testing.T) {
	srv, token := startTestServer(t)
	videoPath := generateUndecodableVideo(t, "corrupt-temp-check.mp4")

	_, result := uploadVideo(t, srv.URL, token, videoPath, "corrupt-temp-check.mp4")
	cleanupOutputZip(t, result.ZipPath)

	if result.Success {
		t.Fatalf("expected processing to fail for undecodable content, got success")
	}

	// Processing failure leaves the uploaded file (and, since this request is
	// authenticated, its ownership sidecar) behind — see
	// TestProcessing_Failure_LeavesUploadedFileBehind for why. Clean both up
	// so this test doesn't accumulate files in the workspace on every run.
	leftovers, err := filepath.Glob(filepath.Join("uploads", "*_corrupt-temp-check.mp4"))
	if err != nil {
		t.Fatalf("failed to glob uploads dir: %v", err)
	}
	for _, leftover := range leftovers {
		t.Cleanup(func() {
			os.Remove(leftover)
			os.Remove(leftover + artifactOwnerSuffix)
		})
	}

	assertTempDirClean(t)
}

func TestProcessing_Success_CleansTempDir(t *testing.T) {
	srv, token := startTestServer(t)
	videoPath := generateTestVideo(t, 1)

	_, result := uploadVideo(t, srv.URL, token, videoPath, "success-temp-check.mp4")
	cleanupOutputZip(t, result.ZipPath)
	if !result.Success {
		t.Fatalf("setup upload failed: %s", result.Message)
	}

	assertTempDirClean(t)
}

// TestProcessing_Failure_LeavesUploadedFileBehind documents current (not
// fixed by this change) behavior: handleVideoUpload only removes the
// uploaded file under uploads/ when processing succeeds, so a failed run
// leaks the original upload indefinitely. See
// openspec/specs/video-frame-extraction/spec.md, "Uploaded File Retained On
// Processing Failure".
func TestProcessing_Failure_LeavesUploadedFileBehind(t *testing.T) {
	srv, token := startTestServer(t)
	videoPath := generateUndecodableVideo(t, "leftover-check.mp4")

	_, result := uploadVideo(t, srv.URL, token, videoPath, "leftover-check.mp4")
	if result.Success {
		t.Fatalf("expected processing to fail for undecodable content, got success")
	}

	leftovers, err := filepath.Glob(filepath.Join("uploads", "*_leftover-check.mp4"))
	if err != nil {
		t.Fatalf("failed to glob uploads dir: %v", err)
	}
	if len(leftovers) != 1 {
		t.Fatalf("expected exactly one leftover upload file (known cleanup gap on failure), found: %v", leftovers)
	}
	t.Cleanup(func() {
		os.Remove(leftovers[0])
		os.Remove(leftovers[0] + artifactOwnerSuffix)
	})
}

// TestStaticOutputs_NeverServesOwnerSidecarFiles guards against a sidecar
// file leaking which UserID owns which artifact, even to the authenticated
// user it names as owner: rejectOwnerSidecarRequests must reject the
// .owner suffix outright rather than deferring to ownership matching.
func TestStaticOutputs_NeverServesOwnerSidecarFiles(t *testing.T) {
	srv, token := startTestServer(t)

	sidecarPath := filepath.Join("outputs", "fake-artifact.zip.owner")
	if err := os.WriteFile(sidecarPath, []byte("3fa85f64-5717-4562-b3fc-2c963f66afa6"), 0600); err != nil {
		t.Fatalf("failed to write fake sidecar: %v", err)
	}
	t.Cleanup(func() { os.Remove(sidecarPath) })

	resp := getWithAuthorization(t, srv.URL+"/outputs/fake-artifact.zip.owner", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (ownership sidecar files must never be served)", resp.StatusCode, http.StatusNotFound)
	}
}

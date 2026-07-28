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

// TestMain requires a real ffmpeg on PATH, same as the app itself needs to
// run at all. If it's missing, we skip the whole suite with a clear message
// instead of failing every test with a confusing error.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Println("SKIP: ffmpeg não encontrado no PATH — pulando testes de integração (main_test.go precisa dele, assim como o app em produção).")
		os.Exit(0)
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

// startTestServer spins up the real router (real handlers, real ffmpeg calls,
// real filesystem) on an in-process httptest server.
func startTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(setupRouter())
	t.Cleanup(srv.Close)
	return srv
}

// uploadVideo performs a multipart upload of the file at videoPath under the
// given filename (the filename controls which extension the server sees).
func uploadVideo(t *testing.T, baseURL, videoPath, filename string) (*http.Response, ProcessingResult) {
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

	return doUpload(t, baseURL, body, writer.FormDataContentType())
}

// uploadEmptyForm sends a multipart form with no "video" field, simulating a
// client that forgot to attach a file.
func uploadEmptyForm(t *testing.T, baseURL string) (*http.Response, ProcessingResult) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	return doUpload(t, baseURL, body, writer.FormDataContentType())
}

func doUpload(t *testing.T, baseURL string, body *bytes.Buffer, contentType string) (*http.Response, ProcessingResult) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/upload", body)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)

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

// cleanupOutputZip removes a zip created under outputs/ during a test, so
// repeated `go test` runs don't accumulate files.
func cleanupOutputZip(t *testing.T, zipFilename string) {
	t.Helper()
	if zipFilename == "" {
		return
	}
	t.Cleanup(func() {
		os.Remove(filepath.Join("outputs", zipFilename))
	})
}

func TestUpload_ValidVideo_ExtractsFramesAndZip(t *testing.T) {
	srv := startTestServer(t)
	videoPath := generateTestVideo(t, 3)

	resp, result := uploadVideo(t, srv.URL, videoPath, "test-video.mp4")
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
	downloadResp, err := http.Get(srv.URL + "/download/" + result.ZipPath)
	if err != nil {
		t.Fatalf("download request failed: %v", err)
	}
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
	srv := startTestServer(t)

	tmpFile := filepath.Join(t.TempDir(), "not-a-video.txt")
	if err := os.WriteFile(tmpFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	resp, result := uploadVideo(t, srv.URL, tmpFile, "not-a-video.txt")
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
	srv := startTestServer(t)

	resp, result := uploadEmptyForm(t, srv.URL)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d", resp.StatusCode)
	}
	if result.Success {
		t.Fatal("expected success=false when no video field is sent")
	}
}

func TestStatus_ListsProcessedZip(t *testing.T) {
	srv := startTestServer(t)
	videoPath := generateTestVideo(t, 1)

	_, result := uploadVideo(t, srv.URL, videoPath, "status-check.mp4")
	cleanupOutputZip(t, result.ZipPath)
	if !result.Success {
		t.Fatalf("setup upload failed: %s", result.Message)
	}

	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatalf("status request failed: %v", err)
	}
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

func TestDownload_NonexistentFile_Returns404(t *testing.T) {
	srv := startTestServer(t)

	resp, err := http.Get(srv.URL + "/download/this-file-does-not-exist.zip")
	if err != nil {
		t.Fatalf("download request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected HTTP 404, got %d", resp.StatusCode)
	}
}

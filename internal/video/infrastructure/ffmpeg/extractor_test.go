package ffmpeg_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/ffmpeg"
)

func requireFfmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH; skipping")
	}
}

// prepareOutputDirs creates the temp/ and outputs/ directories Extractor
// expects relative to the test's working directory, and schedules their
// removal.
func prepareOutputDirs(t *testing.T) {
	t.Helper()
	for _, dir := range []string{"temp", "outputs"} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatalf("unexpected error creating %s: %v", dir, err)
		}
	}
	t.Cleanup(func() {
		os.RemoveAll("temp")
		os.RemoveAll("outputs")
	})
}

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

func generateUndecodableVideo(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "not-a-video.mp4")
	if err := os.WriteFile(path, []byte("not a real video"), 0644); err != nil {
		t.Fatalf("failed to write undecodable video file: %v", err)
	}
	return path
}

func testJobID(t *testing.T) domain.VideoJobID {
	t.Helper()
	id, err := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

func TestExtractor_ExtractFrames_Success(t *testing.T) {
	requireFfmpeg(t)
	prepareOutputDirs(t)

	videoPath := generateTestVideo(t, 3)
	jobID := testJobID(t)

	e := ffmpeg.New()
	storageKey, frameCount, imageNames, err := e.ExtractFrames(context.Background(), jobID, videoPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if storageKey.IsZero() {
		t.Fatalf("expected a non-zero StorageKey")
	}
	if frameCount != 3 {
		t.Fatalf("frameCount = %d, want 3", frameCount)
	}
	if len(imageNames) != 3 {
		t.Fatalf("len(imageNames) = %d, want 3", len(imageNames))
	}

	zipPath := filepath.Join("outputs", storageKey.String())
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatalf("expected zip file to exist at %s: %v", zipPath, err)
	}
}

func TestExtractor_ExtractFrames_UndecodableVideo_ReturnsError(t *testing.T) {
	requireFfmpeg(t)
	prepareOutputDirs(t)

	videoPath := generateUndecodableVideo(t)
	jobID := testJobID(t)

	e := ffmpeg.New()
	_, _, _, err := e.ExtractFrames(context.Background(), jobID, videoPath)
	if err == nil {
		t.Fatalf("expected an error for an undecodable video, got nil")
	}
}

func TestExtractor_ExtractFrames_AlwaysRemovesTempDir(t *testing.T) {
	requireFfmpeg(t)
	prepareOutputDirs(t)
	jobID := testJobID(t)
	e := ffmpeg.New()

	t.Run("success", func(t *testing.T) {
		videoPath := generateTestVideo(t, 1)
		if _, _, _, err := e.ExtractFrames(context.Background(), jobID, videoPath); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertNoLeftoverTempDir(t, jobID)
	})

	t.Run("failure", func(t *testing.T) {
		videoPath := generateUndecodableVideo(t)
		if _, _, _, err := e.ExtractFrames(context.Background(), jobID, videoPath); err == nil {
			t.Fatalf("expected an error for an undecodable video, got nil")
		}
		assertNoLeftoverTempDir(t, jobID)
	})
}

func assertNoLeftoverTempDir(t *testing.T, jobID domain.VideoJobID) {
	t.Helper()
	if _, err := os.Stat(filepath.Join("temp", jobID.String())); !os.IsNotExist(err) {
		t.Fatalf("expected temp/%s to be removed, stat err: %v", jobID.String(), err)
	}
}

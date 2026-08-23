package ffmpeg_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/ffmpeg"
)

func requireFfmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH; skipping")
	}
}

// prepareTempDir creates the temp/ directory Extractor expects relative to
// the test's working directory, and schedules its removal. There is no
// outputs/ counterpart: the extractor writes everything, frames and zip
// alike, under temp/.
func prepareTempDir(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll("temp", 0750); err != nil {
		t.Fatalf("unexpected error creating temp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll("temp") })
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
	prepareTempDir(t)

	videoPath := generateTestVideo(t, 3)
	jobID := testJobID(t)

	e := ffmpeg.New()
	zipPath, frameCount, imageNames, err := e.ExtractFrames(context.Background(), jobID, videoPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { os.Remove(zipPath) })

	if frameCount != 3 {
		t.Fatalf("frameCount = %d, want 3", frameCount)
	}
	if len(imageNames) != 3 {
		t.Fatalf("len(imageNames) = %d, want 3", len(imageNames))
	}

	if _, err := os.Stat(zipPath); err != nil {
		t.Fatalf("expected zip file to exist at %s: %v", zipPath, err)
	}
	// The zip belongs to the caller and must outlive the per-job frame
	// directory the extractor deletes on its way out.
	if filepath.Dir(zipPath) != "temp" {
		t.Fatalf("zipPath = %q, want a file directly under temp/", zipPath)
	}
	if _, err := os.Stat("outputs"); !os.IsNotExist(err) {
		t.Fatalf("expected no outputs/ directory to be created, stat err = %v", err)
	}
}

func TestExtractor_ExtractFrames_UndecodableVideo_ReturnsError(t *testing.T) {
	requireFfmpeg(t)
	prepareTempDir(t)

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
	prepareTempDir(t)
	jobID := testJobID(t)
	e := ffmpeg.New()

	t.Run("success", func(t *testing.T) {
		videoPath := generateTestVideo(t, 1)
		zipPath, _, _, err := e.ExtractFrames(context.Background(), jobID, videoPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		t.Cleanup(func() { os.Remove(zipPath) })
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

// TestExtractor_ExtractFrames_ContextCancellation_StopsFfmpegAndCleansUp
// guards the exec.CommandContext wiring: a canceled context must kill the
// ffmpeg subprocess (not just abandon it running in the background) and
// still clean up the temp directory, so an abandoned upload can't keep
// consuming CPU/disk for the full video duration.
func TestExtractor_ExtractFrames_ContextCancellation_StopsFfmpegAndCleansUp(t *testing.T) {
	requireFfmpeg(t)
	prepareTempDir(t)

	// A synthetic video (e.g. testsrc) decodes too fast to reliably still
	// be running when a short-lived context is canceled, regardless of
	// duration/resolution — timing against real decode speed would be
	// flaky across machines. A named pipe with no data written makes
	// ffmpeg block in read() indefinitely, deterministically guaranteeing
	// it's still running at cancellation time on any hardware.
	fifoPath := filepath.Join(t.TempDir(), "blocking-input.mp4")
	if err := syscall.Mkfifo(fifoPath, 0600); err != nil {
		t.Fatalf("failed to create fifo: %v", err)
	}
	// Opening O_RDWR never blocks and satisfies the "a writer exists"
	// condition for ffmpeg's own open(O_RDONLY) call, so ffmpeg proceeds
	// straight to read() and blocks there forever, since nothing is ever
	// written to the pipe.
	keepAlive, err := os.OpenFile(fifoPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("failed to open fifo: %v", err)
	}
	t.Cleanup(func() { keepAlive.Close() })

	jobID := testJobID(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	e := ffmpeg.New()
	done := make(chan error, 1)
	go func() {
		_, _, _, err := e.ExtractFrames(ctx, jobID, fifoPath)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected an error from a canceled context, got nil")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("ExtractFrames did not return after context cancellation; ffmpeg was not stopped")
	}

	assertNoLeftoverTempDir(t, jobID)
}

func assertNoLeftoverTempDir(t *testing.T, jobID domain.VideoJobID) {
	t.Helper()
	if _, err := os.Stat(filepath.Join("temp", jobID.String())); !os.IsNotExist(err) {
		t.Fatalf("expected temp/%s to be removed, stat err: %v", jobID.String(), err)
	}
}

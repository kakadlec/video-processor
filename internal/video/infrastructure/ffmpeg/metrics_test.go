package ffmpeg_test

import (
	"context"
	"os"
	"testing"

	"video-processor/internal/platform/metrics"
	"video-processor/internal/video/infrastructure/ffmpeg"
)

// extractionSampleCount reads fiapx_video_extraction_duration_seconds'
// sample count for outcome directly out of the process registry, the same
// process-global state every other test that reads a recorded family reads
// -- and, like them, this test is not parallel-safe.
//
// It calls metrics.Gatherer().Gather() rather than scraping an HTTP
// exposition and reparsing it with expfmt (the pattern the three HTTP
// services' own metrics_test.go files use): this package serves no
// endpoint, so there is no exposition to scrape, and the client library's
// own parsed type is available in-process. No new import is needed to reach
// it -- every field below is read through a method call whose result's type
// this file never has to name.
func extractionSampleCount(t *testing.T, outcome string) uint64 {
	t.Helper()

	families, err := metrics.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "fiapx_video_extraction_duration_seconds" {
			continue
		}
		for _, sample := range family.GetMetric() {
			for _, label := range sample.GetLabel() {
				if label.GetName() == "outcome" && label.GetValue() == outcome {
					return sample.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return 0
}

// TestExtractFrames_RecordsDurationByOutcome pins the one instrumentation
// point this package carries: docs/roadmap.md's
// expose-worker-and-notifier-metrics row names "no extraction-duration
// histogram" as the first gap in the worker's own observability, since no
// HTTP service ever calls ExtractFrames.
func TestExtractFrames_RecordsDurationByOutcome(t *testing.T) {
	requireFfmpeg(t)
	prepareTempDir(t)

	before := extractionSampleCount(t, "success")

	videoPath := generateTestVideo(t, 1)
	e := ffmpeg.New()
	zipPath, _, _, err := e.ExtractFrames(context.Background(), testJobID(t), videoPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { os.Remove(zipPath) })

	if got := extractionSampleCount(t, "success"); got != before+1 {
		t.Fatalf("success sample count = %d, want %d", got, before+1)
	}
}

func TestExtractFrames_RecordsDurationOnFailure(t *testing.T) {
	requireFfmpeg(t)
	prepareTempDir(t)

	before := extractionSampleCount(t, "failure")

	videoPath := generateUndecodableVideo(t)
	e := ffmpeg.New()
	if _, _, _, err := e.ExtractFrames(context.Background(), testJobID(t), videoPath); err == nil {
		t.Fatal("expected an error for an undecodable video, got nil")
	}

	if got := extractionSampleCount(t, "failure"); got != before+1 {
		t.Fatalf("failure sample count = %d, want %d", got, before+1)
	}
}

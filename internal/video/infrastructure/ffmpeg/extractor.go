// Package ffmpeg implements domain.FrameExtractor by shelling out to the
// ffmpeg binary, replacing the extraction logic that used to live inline in
// cmd/api/main.go.
package ffmpeg

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"video-processor/internal/video/domain"
)

const (
	tempDirName    = "temp"
	outputsDirName = "outputs"
)

// ErrFfmpegExecFailed, ErrNoFramesExtracted, and ErrZipCreationFailed
// classify ExtractFrames' failure modes so a caller can map each to its own
// user-facing message (e.g. cmd/api/video.go's handleVideoUpload) instead of
// exposing this package's own (English, per the repo's language policy)
// error text directly.
var (
	ErrFfmpegExecFailed  = errors.New("ffmpeg execution failed")
	ErrNoFramesExtracted = errors.New("no frames were extracted from the video")
	ErrZipCreationFailed = errors.New("failed to create zip file")
)

var _ domain.FrameExtractor = (*Extractor)(nil)

// Extractor implements domain.FrameExtractor against a local ffmpeg
// installation, extracting frames into tempDirName and packaging them into
// outputsDirName — the same two directories cmd/api's legacy pipeline used.
type Extractor struct{}

// New constructs an Extractor.
func New() *Extractor {
	return &Extractor{}
}

// ExtractFrames extracts one frame per second from the video at videoPath
// into a per-job temporary directory, packages the frames into a zip under
// outputsDirName, and always removes the temporary directory before
// returning.
func (e *Extractor) ExtractFrames(ctx context.Context, jobID domain.VideoJobID, videoPath string) (domain.StorageKey, int, []string, error) {
	tempDir := filepath.Join(tempDirName, jobID.String())
	if err := os.MkdirAll(tempDir, 0750); err != nil {
		return domain.StorageKey{}, 0, nil, fmt.Errorf("video: create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	framePattern := filepath.Join(tempDir, "frame_%04d.png")

	cmd := exec.CommandContext(ctx, "ffmpeg", // #nosec G204
		"-i", videoPath,
		"-vf", "fps=1",
		"-y",
		framePattern,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return domain.StorageKey{}, 0, nil, fmt.Errorf("%w: %s\noutput: %s", ErrFfmpegExecFailed, err.Error(), string(output))
	}

	frames, err := filepath.Glob(filepath.Join(tempDir, "*.png"))
	if err != nil {
		return domain.StorageKey{}, 0, nil, fmt.Errorf("video: glob extracted frames: %w", err)
	}
	if len(frames) == 0 {
		return domain.StorageKey{}, 0, nil, ErrNoFramesExtracted
	}

	zipFilename := fmt.Sprintf("frames_%s.zip", jobID.String())
	zipPath := filepath.Join(outputsDirName, zipFilename)

	if err := createZipFile(frames, zipPath); err != nil {
		return domain.StorageKey{}, 0, nil, fmt.Errorf("%w: %s", ErrZipCreationFailed, err.Error())
	}

	imageNames := make([]string, len(frames))
	for i, frame := range frames {
		imageNames[i] = filepath.Base(frame)
	}

	storageKey, err := domain.NewStorageKey(zipFilename)
	if err != nil {
		return domain.StorageKey{}, 0, nil, fmt.Errorf("video: build storage key: %w", err)
	}

	return storageKey, len(frames), imageNames, nil
}

// createZipFile writes files into a new zip archive at zipPath. zip.Writer's
// Close writes the archive's central directory — the only point a
// disk/write failure can surface for an otherwise successful write — so its
// error is propagated explicitly rather than discarded via a bare defer,
// which would otherwise let a truncated/invalid zip be reported as success.
func createZipFile(files []string, zipPath string) (err error) {
	zipPath = filepath.Clean(zipPath)
	if !strings.HasPrefix(zipPath, outputsDirName+string(os.PathSeparator)) {
		return fmt.Errorf("invalid zip path: %s", zipPath)
	}

	zipFile, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := zipFile.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	zipWriter := zip.NewWriter(zipFile)

	for _, file := range files {
		if ferr := addFileToZip(zipWriter, file); ferr != nil {
			_ = zipWriter.Close()
			return ferr
		}
	}

	return zipWriter.Close()
}

func addFileToZip(zipWriter *zip.Writer, filename string) error {
	filename = filepath.Clean(filename)
	if !strings.HasPrefix(filename, tempDirName+string(os.PathSeparator)) {
		return fmt.Errorf("invalid frame path: %s", filename)
	}

	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}

	header.Name = filepath.Base(filename)
	header.Method = zip.Deflate

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(writer, file)
	return err
}

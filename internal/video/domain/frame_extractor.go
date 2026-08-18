package domain

import "context"

// FrameExtractor is the port through which a VideoJob's source video is
// turned into extracted frames, packaged for download. The domain depends
// on this interface; infrastructure supplies the concrete implementation
// (shelling out to ffmpeg).
type FrameExtractor interface {
	// ExtractFrames extracts one frame per second from the video at
	// videoPath, packages the frames for download, and returns a
	// StorageKey identifying the package, the number of frames extracted,
	// and their filenames. jobID scopes any per-job scratch state the
	// implementation uses (e.g. a temporary directory).
	ExtractFrames(ctx context.Context, jobID VideoJobID, videoPath string) (storageKey StorageKey, frameCount int, imageNames []string, err error)
}

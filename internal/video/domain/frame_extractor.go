package domain

import "context"

// FrameExtractor is the port through which a VideoJob's source video is
// turned into extracted frames, packaged for download. The domain depends
// on this interface; infrastructure supplies the concrete implementation
// (shelling out to ffmpeg).
type FrameExtractor interface {
	// ExtractFrames extracts one frame per second from the video at
	// videoPath, packages the frames into a zip on the local filesystem,
	// and returns that zip's path, the number of frames extracted, and
	// their filenames. jobID scopes any per-job scratch state the
	// implementation uses (e.g. a temporary directory).
	//
	// The returned path is a local file the caller is responsible for:
	// making the result durable is ResultStorage's job, not this port's,
	// so an extractor knows nothing about where the artifact ends up.
	ExtractFrames(ctx context.Context, jobID VideoJobID, videoPath string) (zipPath string, frameCount int, imageNames []string, err error)
}

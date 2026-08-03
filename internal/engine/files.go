package engine

import (
	"context"
	"time"
)

// UploadRequest is one file-input upload (issue #63). Files are resolved
// against AllowedDirs; anything outside (traversal, symlink escapes,
// absolute paths outside the roots) is rejected by the engine.
type UploadRequest struct {
	Selector    string   `json:"selector"`
	Files       []string `json:"files"`
	AllowedDirs []string `json:"allowed_dirs,omitempty"`
}

// UploadResult reports which files were handed to the input element.
type UploadResult struct {
	Uploaded []string `json:"uploaded"`
}

// DownloadConfig configures the browser download behavior (issue #63).
type DownloadConfig struct {
	// Dir is the directory downloads are written to. Empty denies.
	Dir string `json:"dir"`
}

// DownloadEvent is one browser download lifecycle event (issue #63). For
// completed downloads SHA256 is the file checksum computed by the engine.
type DownloadEvent struct {
	GUID          string    `json:"guid"`
	URL           string    `json:"url"`
	Filename      string    `json:"filename"`
	State         string    `json:"state"` // inProgress | completed | canceled
	ReceivedBytes int64     `json:"received_bytes"`
	TotalBytes    int64     `json:"total_bytes,omitempty"`
	SHA256        string    `json:"sha256,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
}

// FileTransfer is the optional engine capability behind upload and
// download commands (issue #63).
type FileTransfer interface {
	UploadFiles(context.Context, Page, UploadRequest) (UploadResult, error)
	SetDownloadBehavior(context.Context, Page, DownloadConfig) error
	DownloadEvents(Page) []DownloadEvent
}

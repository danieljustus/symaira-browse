package daemon

import (
	"context"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// handleUploadFrame uploads files into the selected file input. Every file
// is path-guarded against the runtime's allowed upload directories
// (issue #63 AC: traversal, symlink escapes and outside paths fail).
func (r *NavigationRuntime) handleUploadFrame(ctx context.Context, frame Frame) (any, error) {
	var request engine.UploadRequest
	if err := decodeArgs(frame, &request); err != nil {
		return nil, err
	}
	if len(request.AllowedDirs) == 0 {
		request.AllowedDirs = r.uploadDirs
	}
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	browser := r.engines[frame.Session]
	r.mu.Unlock()
	transfer, ok := browser.(engine.FileTransfer)
	if !ok {
		return nil, fmt.Errorf("browser engine does not support uploads")
	}
	result, err := transfer.UploadFiles(ctx, service.Page(), request)
	if err != nil {
		return nil, err
	}
	return map[string]any{"uploaded": result.Uploaded}, nil
}

// handleDownloadFrame serves the downloads.list and download.setdir frames
// (issue #63). Download events carry origin URL, size and checksum.
func (r *NavigationRuntime) handleDownloadFrame(ctx context.Context, frame Frame) (any, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	browser := r.engines[frame.Session]
	r.mu.Unlock()
	transfer, ok := browser.(engine.FileTransfer)
	if !ok {
		return nil, fmt.Errorf("browser engine does not support downloads")
	}
	switch frame.Cmd {
	case "download.setdir":
		var request struct {
			Dir string `json:"dir"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := transfer.SetDownloadBehavior(ctx, service.Page(), engine.DownloadConfig{Dir: request.Dir}); err != nil {
			return nil, err
		}
		return map[string]any{"download_dir": request.Dir}, nil
	case "downloads.list":
		events := transfer.DownloadEvents(service.Page())
		return map[string]any{"downloads": events, "count": len(events)}, nil
	default:
		return nil, fmt.Errorf("unknown download command %q", frame.Cmd)
	}
}

package daemon

import (
	"context"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// handleStorageFrame serves storage.list, storage.set and storage.clear.
func (r *NavigationRuntime) handleStorageFrame(ctx context.Context, frame Frame) (any, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	switch frame.Cmd {
	case "storage.list":
		var request struct {
			Kind engine.StorageKind `json:"kind"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		items, err := service.Storage().StorageItems(ctx, request.Kind)
		if err != nil {
			return nil, err
		}
		origin, originErr := service.Origin(ctx)
		if originErr != nil {
			origin = ""
		}
		return map[string]any{"origin": origin, "kind": request.Kind, "items": items}, nil
	case "storage.set":
		var request struct {
			Kind  engine.StorageKind `json:"kind"`
			Key   string             `json:"key"`
			Value string             `json:"value"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.Storage().SetStorageItem(ctx, request.Kind, request.Key, request.Value); err != nil {
			return nil, err
		}
		return map[string]any{"set": request.Key}, nil
	case "storage.clear":
		var request struct {
			Kind engine.StorageKind `json:"kind"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.Storage().ClearStorage(ctx, request.Kind); err != nil {
			return nil, err
		}
		return map[string]any{"cleared": request.Kind}, nil
	default:
		return nil, fmt.Errorf("unknown storage command %q", frame.Cmd)
	}
}

package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/danieljustus/symaira-browse/internal/budget"
)

// CacheRuntime exposes unified output-cache retrieval over the daemon socket.
type CacheRuntime struct {
	cache *budget.Cache
}

// NewCacheRuntime creates a cache retrieval runtime rooted at dir.
func NewCacheRuntime(dir string, ttl time.Duration) *CacheRuntime {
	return &CacheRuntime{cache: budget.NewCache(dir, ttl)}
}

func (r *CacheRuntime) Handle(_ context.Context, frame Frame) (any, []Warning, error) {
	if frame.Cmd != "cache.get" {
		return nil, nil, NewError(ErrorUnknownCommand, fmt.Sprintf("command %q is not implemented by the cache runtime", frame.Cmd))
	}
	var request struct {
		CacheID string `json:"cache_id"`
		Range   string `json:"range,omitempty"`
	}
	if err := decodeOptionalArgs(frame, &request); err != nil {
		return nil, nil, err
	}
	if request.CacheID == "" {
		return nil, nil, NewError(ErrorMalformedRequest, "cache.get requires a cache_id argument")
	}
	content, err := r.cache.Load(request.CacheID)
	if err != nil {
		if errors.Is(err, budget.ErrNotFound) || errors.Is(err, budget.ErrExpired) {
			return nil, nil, NewError(ErrorOperationFailed, err.Error())
		}
		return nil, nil, NewError(ErrorOperationFailed, fmt.Sprintf("load cache entry: %v", err))
	}
	result := map[string]any{
		"cache_id": request.CacheID,
		"content":  string(content),
	}
	if strings.TrimSpace(request.Range) != "" {
		start, end, err := parseCacheRange(request.Range)
		if err != nil {
			return nil, nil, NewError(ErrorMalformedRequest, err.Error())
		}
		result["range"] = request.Range
		result["content"] = budget.LineRange(content, start, end)
	}
	return result, nil, nil
}

func parseCacheRange(spec string) (int, int, error) {
	spec = strings.TrimSpace(spec)
	parts := strings.SplitN(spec, "-", 2)
	start, end := 0, 0
	if parts[0] != "" {
		parsed, err := strconv.Atoi(parts[0])
		if err != nil || parsed < 1 {
			return 0, 0, fmt.Errorf("invalid range %q: start must be a positive line number", spec)
		}
		start = parsed
	}
	if len(parts) == 2 && parts[1] != "" {
		parsed, err := strconv.Atoi(parts[1])
		if err != nil || parsed < start {
			return 0, 0, fmt.Errorf("invalid range %q: end must be >= start", spec)
		}
		end = parsed
	}
	return start, end, nil
}

package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// requestIDCounter generates unique request IDs across concurrent frames.
// time.Now().UnixNano() can collide when two frames execute in the same
// nanosecond; a mutex-protected counter avoids that.
var (
	requestMu    sync.Mutex
	requestCount uint64
)

func nextRequestID() string {
	requestMu.Lock()
	requestCount++
	id := requestCount
	requestMu.Unlock()
	return fmt.Sprintf("%d", id)
}

// request sends one daemon frame with the standard session resolution.
func request(ctx context.Context, session, command string, args []byte) (daemon.Response, error) {
	return requestBudget(ctx, session, command, args, nil)
}

// requestBudget is request with an optional token budget: the daemon
// truncates oversized payloads and returns a cache handle.
func requestBudget(ctx context.Context, session, command string, args []byte, maxTokens *int) (daemon.Response, error) {
	path, err := daemon.SocketPath(session)
	if err != nil {
		return daemon.Response{}, err
	}
	client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
	return client.Request(ctx, daemon.Frame{
		Cmd:       command,
		Args:      args,
		Session:   session,
		RequestID: nextRequestID(),
		MaxTokens: maxTokens,
	})
}

// requestNoAutostart is request() without daemon autostart (used by status,
// stop, and session inspection so those commands do not create a daemon).
func requestNoAutostart(ctx context.Context, session, command string) (daemon.Response, error) {
	path, err := daemon.SocketPath(session)
	if err != nil {
		return daemon.Response{}, err
	}
	client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
	return client.RequestWithoutAutostart(ctx, daemon.Frame{
		Cmd:       command,
		Session:   session,
		RequestID: nextRequestID(),
	})
}

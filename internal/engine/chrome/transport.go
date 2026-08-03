package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type rpcRequest struct {
	ID        uint64 `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type rpcResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func dial(ctx context.Context, endpoint string, timeout time.Duration) (*rpcConnection, error) {
	dialer := websocket.Dialer{HandshakeTimeout: timeout}
	ws, response, err := dialer.DialContext(ctx, endpoint, http.Header{})
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	return &rpcConnection{ws: ws}, nil
}

func (c *rpcConnection) Execute(ctx context.Context, method string, params, result any) error {
	return c.execute(ctx, "", method, params, result)
}

func (c *rpcConnection) execute(ctx context.Context, sessionID, method string, params, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.ws == nil {
		return errors.New("CDP connection is closed")
	}
	id := atomic.AddUint64(&c.next, 1)
	request := rpcRequest{ID: id, Method: method, Params: params, SessionID: sessionID}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.ws.SetWriteDeadline(deadline)
		_ = c.ws.SetReadDeadline(deadline)
	}
	if err := c.ws.WriteJSON(request); err != nil {
		return fmt.Errorf("write CDP request %s: %w", method, err)
	}
	for {
		var response rpcResponse
		if err := c.ws.ReadJSON(&response); err != nil {
			return fmt.Errorf("read CDP response %s: %w", method, err)
		}
		if response.ID != id {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("CDP %s failed (%d): %s", method, response.Error.Code, response.Error.Message)
		}
		if result != nil && len(response.Result) > 0 && string(response.Result) != "null" {
			if err := json.Unmarshal(response.Result, result); err != nil {
				return fmt.Errorf("decode CDP response %s: %w", method, err)
			}
		}
		return nil
	}
}

func (c *rpcConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.ws == nil {
		return nil
	}
	return c.ws.Close()
}

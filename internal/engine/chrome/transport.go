package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
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

// rpcMessage is one frame read from the socket: either a response to an
// in-flight request (ID != 0) or an event (ID == 0, Method set).
type rpcMessage struct {
	ID        uint64          `json:"id"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *rpcError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// eventHandler receives protocol events. sessionID is the flat-protocol
// session that emitted the event and must be echoed back on any command
// responding to it. Handlers run on their own goroutine so a handler may
// itself issue CDP commands without deadlocking the read loop.
type eventHandler func(sessionID, method string, params json.RawMessage)

func dial(ctx context.Context, endpoint string, timeout time.Duration) (*rpcConnection, error) {
	dialer := websocket.Dialer{HandshakeTimeout: timeout}
	ws, response, err := dialer.DialContext(ctx, endpoint, http.Header{})
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	conn := &rpcConnection{
		ws:       ws,
		pending:  make(map[uint64]chan rpcResponse),
		readDone: make(chan struct{}),
	}
	go conn.readLoop()
	return conn, nil
}

// rpcConnection serializes request/response pairs over a CDP WebSocket. A
// dedicated read loop pumps responses to their pending callers and dispatches
// events to registered handlers; events arriving between requests are no
// longer discarded.
type rpcConnection struct {
	ws        *websocket.Conn
	writeMu   sync.Mutex
	mu        sync.Mutex
	next      uint64
	pending   map[uint64]chan rpcResponse
	handlers  []eventHandler
	closed    bool
	closeOnce sync.Once
	readDone  chan struct{}
}

// addHandler registers a protocol event handler for the lifetime of the
// connection.
func (c *rpcConnection) addHandler(handler eventHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers = append(c.handlers, handler)
}

func (c *rpcConnection) Execute(ctx context.Context, method string, params, result any) error {
	return c.execute(ctx, "", method, params, result)
}

func (c *rpcConnection) execute(ctx context.Context, sessionID, method string, params, result any) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed || c.ws == nil {
		return errors.New("CDP connection is closed")
	}
	id := atomic.AddUint64(&c.next, 1)
	response := make(chan rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = response
	c.mu.Unlock()

	request := rpcRequest{ID: id, Method: method, Params: params, SessionID: sessionID}
	c.writeMu.Lock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.ws.SetWriteDeadline(deadline)
	}
	writeErr := c.ws.WriteJSON(request)
	c.writeMu.Unlock()
	if writeErr != nil {
		c.dropPending(id)
		return fmt.Errorf("write CDP request %s: %w", method, writeErr)
	}

	select {
	case reply := <-response:
		if reply.Error != nil {
			return fmt.Errorf("CDP %s failed (%d): %s", method, reply.Error.Code, reply.Error.Message)
		}
		if result != nil && len(reply.Result) > 0 && string(reply.Result) != "null" {
			if err := json.Unmarshal(reply.Result, result); err != nil {
				return fmt.Errorf("decode CDP response %s: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.dropPending(id)
		return ctx.Err()
	}
}

func (c *rpcConnection) dropPending(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// readLoop owns the socket reads for the connection's lifetime. Responses are
// routed to their pending caller; events are fanned out to handlers without
// blocking the loop.
func (c *rpcConnection) readLoop() {
	defer close(c.readDone)
	for {
		var message rpcMessage
		if err := c.ws.ReadJSON(&message); err != nil {
			c.failPending(err)
			return
		}
		if message.ID == 0 {
			if message.Method != "" {
				c.dispatchEvent(message)
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[message.ID]
		delete(c.pending, message.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- rpcResponse{ID: message.ID, Result: message.Result, Error: message.Error}
		}
	}
}

func (c *rpcConnection) dispatchEvent(message rpcMessage) {
	c.mu.Lock()
	handlers := make([]eventHandler, len(c.handlers))
	copy(handlers, c.handlers)
	c.mu.Unlock()
	if len(handlers) == 0 {
		return
	}
	// Handlers issue CDP commands (continue/fail), which need the read loop to
	// keep pumping responses, so they must never run on this goroutine.
	go func() {
		for _, handler := range handlers {
			handler(message.SessionID, message.Method, message.Params)
		}
	}()
}

func (c *rpcConnection) failPending(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[uint64]chan rpcResponse)
	c.mu.Unlock()
	reply := rpcResponse{Error: &rpcError{Code: -32000, Message: err.Error()}}
	for _, ch := range pending {
		ch <- reply
	}
}

func (c *rpcConnection) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		if c.ws != nil {
			_ = c.ws.Close()
		}
	})
	if c.readDone != nil {
		<-c.readDone
	}
	return nil
}

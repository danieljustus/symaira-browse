package safaribidi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// WebDriver BiDi frames a command as {id, method, params} and answers with a
// typed envelope: {"type":"success"|"error"|"event"}. The type discriminator
// is what separates this transport from the CDP one in
// internal/engine/chrome/transport.go, which distinguishes responses from
// events by a zero id. Events in BiDi carry no id at all, so the discriminator
// has to be read, not inferred.
type bidiRequest struct {
	ID     uint64 `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

// bidiMessage is one frame read from the socket.
type bidiMessage struct {
	Type    string          `json:"type"`
	ID      *uint64         `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
	Message string          `json:"message,omitempty"`
}

type bidiReply struct {
	Result json.RawMessage
	Err    *CommandError
}

// CommandError is a BiDi error response. Code is the W3C error code
// ("invalid argument", "no such element", "unsupported operation", …) and is
// stable enough to branch on; Message is Safari's free text.
type CommandError struct {
	Method  string
	Code    string
	Message string
}

func (e *CommandError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("bidi %s failed: %s", e.Method, e.Code)
	}
	return fmt.Sprintf("bidi %s failed (%s): %s", e.Method, e.Code, e.Message)
}

// eventHandler receives BiDi events. Handlers run on their own goroutine so a
// handler may itself issue BiDi commands without deadlocking the read loop.
type eventHandler func(method string, params json.RawMessage)

// dial opens the BiDi socket. Safari reports webSocketUrl in the session
// response before the listener always accepts, so the handshake is retried
// until the deadline rather than failing on the first refusal.
func dial(ctx context.Context, endpoint string, timeout time.Duration) (*connection, error) {
	dialer := websocket.Dialer{HandshakeTimeout: timeout}
	deadline := time.Now().Add(timeout)
	var (
		ws      *websocket.Conn
		lastErr error
	)
	for {
		conn, response, err := dialer.DialContext(ctx, endpoint, http.Header{})
		if response != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
			_ = response.Body.Close()
			if err != nil {
				err = fmt.Errorf("%w (HTTP %d: %s)", err, response.StatusCode, strings.TrimSpace(string(body)))
			}
		}
		if err == nil {
			ws = conn
			break
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("dial bidi socket %s: %w", endpoint, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	conn := &connection{
		ws:       ws,
		pending:  make(map[uint64]chan bidiReply),
		readDone: make(chan struct{}),
	}
	go conn.readLoop()
	return conn, nil
}

// connection serializes command/response pairs over a BiDi WebSocket.
type connection struct {
	ws        *websocket.Conn
	writeMu   sync.Mutex
	mu        sync.Mutex
	next      uint64
	pending   map[uint64]chan bidiReply
	handlers  []eventHandler
	closed    bool
	closeOnce sync.Once
	readDone  chan struct{}
}

func (c *connection) addHandler(handler eventHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers = append(c.handlers, handler)
}

// Execute sends one BiDi command and decodes its result. A protocol-level
// error is returned as *CommandError so callers can branch on the W3C code
// instead of matching Safari's message text.
func (c *connection) Execute(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed || c.ws == nil {
		return errors.New("bidi connection is closed")
	}
	if params == nil {
		params = struct{}{}
	}
	id := atomic.AddUint64(&c.next, 1)
	reply := make(chan bidiReply, 1)
	c.mu.Lock()
	c.pending[id] = reply
	c.mu.Unlock()

	c.writeMu.Lock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.ws.SetWriteDeadline(deadline)
	}
	writeErr := c.ws.WriteJSON(bidiRequest{ID: id, Method: method, Params: params})
	c.writeMu.Unlock()
	if writeErr != nil {
		c.dropPending(id)
		return fmt.Errorf("write bidi command %s: %w", method, writeErr)
	}

	select {
	case answer := <-reply:
		if answer.Err != nil {
			answer.Err.Method = method
			return answer.Err
		}
		if result != nil && len(answer.Result) > 0 && string(answer.Result) != "null" {
			if err := json.Unmarshal(answer.Result, result); err != nil {
				return fmt.Errorf("decode bidi response %s: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.dropPending(id)
		return ctx.Err()
	}
}

func (c *connection) dropPending(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *connection) readLoop() {
	defer close(c.readDone)
	for {
		var message bidiMessage
		if err := c.ws.ReadJSON(&message); err != nil {
			c.failPending(err)
			return
		}
		if message.Type == "event" || (message.ID == nil && message.Method != "") {
			c.dispatchEvent(message)
			continue
		}
		if message.ID == nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[*message.ID]
		delete(c.pending, *message.ID)
		c.mu.Unlock()
		if ch == nil {
			continue
		}
		if message.Type == "error" || message.Error != "" {
			ch <- bidiReply{Err: &CommandError{Code: message.Error, Message: message.Message}}
			continue
		}
		ch <- bidiReply{Result: message.Result}
	}
}

func (c *connection) dispatchEvent(message bidiMessage) {
	c.mu.Lock()
	handlers := make([]eventHandler, len(c.handlers))
	copy(handlers, c.handlers)
	c.mu.Unlock()
	if len(handlers) == 0 {
		return
	}
	// A network interception handler answers with network.continueRequest,
	// which needs the read loop to keep pumping, so handlers never run here.
	go func() {
		for _, handler := range handlers {
			handler(message.Method, message.Params)
		}
	}()
}

func (c *connection) failPending(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[uint64]chan bidiReply)
	c.closed = true
	c.mu.Unlock()
	reply := bidiReply{Err: &CommandError{Code: "connection closed", Message: err.Error()}}
	for _, ch := range pending {
		ch <- reply
	}
}

func (c *connection) Close() error {
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

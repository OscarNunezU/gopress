package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
)

// Client is a raw CDP client connected to a single browser target via WebSocket.
type Client struct {
	conn      net.Conn
	logger    *slog.Logger
	nextID    atomic.Int64
	mu        sync.Mutex
	pending   map[int]chan *Message
	listeners map[string][]chan Event
}

// Dial connects to a CDP WebSocket endpoint (e.g. ws://localhost:9222/devtools/page/XXX).
func Dial(ctx context.Context, wsURL string, logger *slog.Logger) (*Client, error) {
	// TODO: implement WebSocket handshake manually (HTTP upgrade)
	_ = wsURL
	_ = logger
	return nil, fmt.Errorf("not implemented")
}

// Send sends a CDP command and waits for its response.
func (c *Client) Send(ctx context.Context, method string, params any, result any) error {
	id := int(c.nextID.Add(1))
	msg := Message{ID: id, Method: method, Params: params}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal command: %w", err)
	}

	ch := make(chan *Message, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	// TODO: write frame to WebSocket conn
	_ = data

	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil {
			raw, err := json.Marshal(resp.Result)
			if err != nil {
				return fmt.Errorf("marshal result: %w", err)
			}
			return json.Unmarshal(raw, result)
		}
		return nil
	}
}

// Subscribe registers a channel to receive CDP events for the given method.
func (c *Client) Subscribe(method string) <-chan Event {
	ch := make(chan Event, 8)
	c.mu.Lock()
	c.listeners[method] = append(c.listeners[method], ch)
	c.mu.Unlock()
	return ch
}

// readLoop reads incoming WebSocket frames and dispatches to pending commands or event listeners.
func (c *Client) readLoop() {
	// TODO: implement WebSocket frame reading and dispatch
}

// newTargetURL queries the browser's /json/new endpoint and returns the WebSocket debugger URL.
func newTargetURL(host string) (string, error) {
	resp, err := http.Get("http://" + host + "/json/new")
	if err != nil {
		return "", fmt.Errorf("create target: %w", err)
	}
	defer resp.Body.Close()

	var target struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&target); err != nil {
		return "", fmt.Errorf("decode target: %w", err)
	}
	return target.WebSocketDebuggerURL, nil
}

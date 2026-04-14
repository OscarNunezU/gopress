// Package cdp implements a minimal Chrome DevTools Protocol client
// over WebSocket using only the Go standard library.
package cdp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
)

// Client is a raw CDP client connected to a Chrome browser process via WebSocket.
// One Client is created per Chrome process and lives for its entire lifetime.
// Use NewSession to obtain a tab-scoped session for each conversion.
type Client struct {
	conn      net.Conn
	reader    *bufio.Reader // shared between handshake and readLoop — no bytes lost
	logger    *slog.Logger
	nextID    atomic.Int64
	mu        sync.Mutex
	pending   map[int]chan *Message
	// listeners for browser-level events (sessionId == "").
	listeners map[string][]chan Event
	// sessionListeners routes events to the correct tab session.
	// Keyed by sessionId → method → channels.
	sessionListeners map[string]map[string][]chan Event
	writeMu          sync.Mutex
	done             chan struct{}
}

// Dial connects to a CDP WebSocket endpoint.
// For browser-level connections, use the webSocketDebuggerUrl from /json/version.
func Dial(ctx context.Context, wsURL string, logger *slog.Logger) (*Client, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("parse ws url: %w", err)
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("tcp dial %s: %w", u.Host, err)
	}

	// The bufio.Reader wraps conn for both the HTTP upgrade response and all
	// subsequent WebSocket frame reads. Reusing it ensures no bytes are lost
	// if the upgrade response and first frame arrive in the same TCP segment.
	br := bufio.NewReader(conn)
	if err := wsHandshake(conn, br, u); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ws handshake: %w", err)
	}

	c := &Client{
		conn:             conn,
		reader:           br,
		logger:           logger,
		pending:          make(map[int]chan *Message),
		listeners:        make(map[string][]chan Event),
		sessionListeners: make(map[string]map[string][]chan Event),
		done:             make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Send sends a browser-level CDP command and waits for its response.
func (c *Client) Send(ctx context.Context, method string, params any, result any) error {
	return c.send(ctx, "", method, params, result)
}

// Subscribe registers a channel to receive browser-level CDP events.
func (c *Client) Subscribe(method string) <-chan Event {
	return c.subscribe("", method)
}

// NewSession returns a Session scoped to the given CDP session ID.
// All commands and subscriptions through the Session are multiplexed over
// this Client's single WebSocket connection via the flat-session protocol.
func (c *Client) NewSession(sessionID string) *Session {
	return &Session{c: c, id: sessionID}
}

// Close shuts down the WebSocket connection.
func (c *Client) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return c.conn.Close()
}

// send is the shared implementation for both Client and Session commands.
func (c *Client) send(ctx context.Context, sessionID, method string, params any, result any) error {
	id := int(c.nextID.Add(1))
	msg := Message{ID: id, Method: method, Params: params, SessionID: sessionID}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal command: %w", err)
	}

	ch := make(chan *Message, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.writeFrame(data); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("write frame: %w", err)
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
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

// subscribe registers an event listener for either browser-level or session events.
func (c *Client) subscribe(sessionID, method string) <-chan Event {
	ch := make(chan Event, 8)
	c.mu.Lock()
	defer c.mu.Unlock()

	if sessionID == "" {
		c.listeners[method] = append(c.listeners[method], ch)
		return ch
	}

	if c.sessionListeners[sessionID] == nil {
		c.sessionListeners[sessionID] = make(map[string][]chan Event)
	}
	c.sessionListeners[sessionID][method] = append(c.sessionListeners[sessionID][method], ch)
	return ch
}

// readLoop reads incoming WebSocket frames and dispatches to pending commands
// or event listeners, routing by sessionId for tab-scoped events.
func (c *Client) readLoop() {
	defer func() {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}()

	for {
		data, err := readFrame(c.reader)
		if err != nil {
			select {
			case <-c.done:
			default:
				if err != io.EOF {
					c.logger.Error("cdp read frame", "err", err)
				}
			}
			return
		}
		if len(data) == 0 {
			continue // ping / pong control frame
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			c.logger.Warn("cdp unmarshal message", "err", err)
			continue
		}

		if msg.ID != 0 {
			// Response to a pending command (browser- or session-level).
			c.mu.Lock()
			ch, ok := c.pending[msg.ID]
			if ok {
				delete(c.pending, msg.ID)
			}
			c.mu.Unlock()
			if ok {
				ch <- &msg
			}
			continue
		}

		// Server-sent event — dispatch by sessionId.
		if msg.Method == "" {
			continue
		}
		evt := Event{Method: msg.Method}
		if p, ok := msg.Params.(map[string]any); ok {
			evt.Params = p
		}

		c.mu.Lock()
		var chs []chan Event
		if msg.SessionID != "" {
			chs = c.sessionListeners[msg.SessionID][msg.Method]
		} else {
			chs = c.listeners[msg.Method]
		}
		c.mu.Unlock()

		for _, ch := range chs {
			select {
			case ch <- evt:
			default: // drop if listener is not consuming fast enough
			}
		}
	}
}

// writeFrame sends a WebSocket text frame with the given payload.
// Frames sent by the client must be masked per RFC 6455.
func (c *Client) writeFrame(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	header := make([]byte, 2)
	header[0] = 0x81 // FIN=1, opcode=1 (text)

	length := len(payload)
	var extra []byte
	switch {
	case length < 126:
		header[1] = byte(length) | 0x80
	case length < 65536:
		header[1] = 126 | 0x80
		extra = make([]byte, 2)
		binary.BigEndian.PutUint16(extra, uint16(length))
	default:
		header[1] = 127 | 0x80
		extra = make([]byte, 8)
		binary.BigEndian.PutUint64(extra, uint64(length))
	}

	mask := make([]byte, 4)
	if _, err := io.ReadFull(rand.Reader, mask); err != nil {
		return fmt.Errorf("generate mask: %w", err)
	}

	masked := make([]byte, length)
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}

	frame := append(header, extra...)
	frame = append(frame, mask...)
	frame = append(frame, masked...)

	_, err := c.conn.Write(frame)
	return err
}

// readFrame reads one complete WebSocket frame from r and returns the payload.
// Returns (nil, io.EOF) for a close frame; (nil, nil) for ping/pong.
func readFrame(r io.Reader) ([]byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	opcode := header[0] & 0x0f
	masked := (header[1] & 0x80) != 0
	length := int(header[1] & 0x7f)

	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, fmt.Errorf("read 16-bit length: %w", err)
		}
		length = int(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, fmt.Errorf("read 64-bit length: %w", err)
		}
		length = int(binary.BigEndian.Uint64(ext))
	}

	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(r, mask); err != nil {
			return nil, fmt.Errorf("read mask: %w", err)
		}
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}

	if masked {
		for i, b := range payload {
			payload[i] = b ^ mask[i%4]
		}
	}

	switch opcode {
	case 0x8:
		return nil, io.EOF
	case 0x9, 0xA:
		return nil, nil
	}
	return payload, nil
}

// wsHandshake performs the HTTP→WebSocket upgrade handshake per RFC 6455.
// br must wrap conn so bytes buffered during http.ReadResponse are not lost.
func wsHandshake(conn net.Conn, br *bufio.Reader, u *url.URL) error {
	keyBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, keyBytes); err != nil {
		return fmt.Errorf("generate ws key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		u.RequestURI(), u.Host, key,
	)
	if _, err := fmt.Fprint(conn, req); err != nil {
		return fmt.Errorf("send handshake request: %w", err)
	}

	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		return fmt.Errorf("read handshake response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	expected := wsAcceptKey(key)
	if got := resp.Header.Get("Sec-Websocket-Accept"); got != expected {
		return fmt.Errorf("invalid Sec-WebSocket-Accept: got %q want %q", got, expected)
	}
	return nil
}

// wsAcceptKey computes the expected Sec-WebSocket-Accept value per RFC 6455 §4.2.2.
func wsAcceptKey(key string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.New()
	h.Write([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Session — tab-scoped CDP session multiplexed over a single Client connection.
// ---------------------------------------------------------------------------

// Session multiplexes CDP commands and events for one browser tab over the
// parent Client's WebSocket connection using flat-mode session IDs.
type Session struct {
	c  *Client
	id string
}

// Send sends a CDP command scoped to this session's tab.
func (s *Session) Send(ctx context.Context, method string, params any, result any) error {
	return s.c.send(ctx, s.id, method, params, result)
}

// Subscribe registers a channel to receive CDP events from this session's tab.
func (s *Session) Subscribe(method string) <-chan Event {
	return s.c.subscribe(s.id, method)
}

// Close removes all event listeners registered for this session.
func (s *Session) Close() {
	s.c.mu.Lock()
	delete(s.c.sessionListeners, s.id)
	s.c.mu.Unlock()
}

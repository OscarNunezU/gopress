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

// Client is a raw CDP client connected to a single browser target via WebSocket.
type Client struct {
	conn      net.Conn
	logger    *slog.Logger
	nextID    atomic.Int64
	mu        sync.Mutex
	pending   map[int]chan *Message
	listeners map[string][]chan Event
	writeMu   sync.Mutex
	done      chan struct{}
}

// Dial connects to a CDP WebSocket endpoint (e.g. ws://localhost:9222/devtools/page/XXX).
func Dial(ctx context.Context, wsURL string, logger *slog.Logger) (*Client, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("parse ws url: %w", err)
	}

	host := u.Host
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("tcp dial %s: %w", host, err)
	}

	if err := wsHandshake(conn, u); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ws handshake: %w", err)
	}

	c := &Client{
		conn:      conn,
		logger:    logger,
		pending:   make(map[int]chan *Message),
		listeners: make(map[string][]chan Event),
		done:      make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
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

// Subscribe registers a channel to receive CDP events for the given method.
func (c *Client) Subscribe(method string) <-chan Event {
	ch := make(chan Event, 8)
	c.mu.Lock()
	c.listeners[method] = append(c.listeners[method], ch)
	c.mu.Unlock()
	return ch
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

// readLoop reads incoming WebSocket frames and dispatches to pending commands or event listeners.
func (c *Client) readLoop() {
	defer func() {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}()

	for {
		data, err := readFrame(c.conn)
		if err != nil {
			select {
			case <-c.done:
			default:
				c.logger.Error("cdp read frame", "err", err)
			}
			return
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			c.logger.Warn("cdp unmarshal message", "err", err)
			continue
		}

		if msg.ID != 0 {
			// Response to a pending command.
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

		// Server-sent event.
		if msg.Method != "" {
			evt := Event{Method: msg.Method}
			if msg.Params != nil {
				if p, ok := msg.Params.(map[string]any); ok {
					evt.Params = p
				}
			}
			c.mu.Lock()
			chs := c.listeners[msg.Method]
			c.mu.Unlock()
			for _, ch := range chs {
				select {
				case ch <- evt:
				default:
					// Drop if listener is not consuming fast enough.
				}
			}
		}
	}
}

// writeFrame sends a WebSocket text frame with the given payload.
// Frames sent by the client must be masked per RFC 6455.
func (c *Client) writeFrame(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// Build frame header.
	// Byte 0: FIN=1, opcode=1 (text).
	// Byte 1: MASK=1, payload length.
	header := make([]byte, 2)
	header[0] = 0x81 // FIN + text frame

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

	// Generate masking key.
	mask := make([]byte, 4)
	if _, err := io.ReadFull(rand.Reader, mask); err != nil {
		return fmt.Errorf("generate mask: %w", err)
	}

	// Mask payload.
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

// readFrame reads one complete WebSocket frame and returns the unmasked payload.
func readFrame(conn net.Conn) ([]byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	// opcode := header[0] & 0x0f  (we accept any — server sends text or continuation)
	masked := (header[1] & 0x80) != 0
	length := int(header[1] & 0x7f)

	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(conn, ext); err != nil {
			return nil, fmt.Errorf("read 16-bit length: %w", err)
		}
		length = int(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(conn, ext); err != nil {
			return nil, fmt.Errorf("read 64-bit length: %w", err)
		}
		length = int(binary.BigEndian.Uint64(ext))
	}

	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(conn, mask); err != nil {
			return nil, fmt.Errorf("read mask: %w", err)
		}
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}

	if masked {
		for i, b := range payload {
			payload[i] = b ^ mask[i%4]
		}
	}

	return payload, nil
}

// wsHandshake performs the HTTP→WebSocket upgrade handshake per RFC 6455.
func wsHandshake(conn net.Conn, u *url.URL) error {
	// Generate Sec-WebSocket-Key.
	keyBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, keyBytes); err != nil {
		return fmt.Errorf("generate ws key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	path := u.RequestURI()
	host := u.Host

	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path, host, key,
	)
	if _, err := fmt.Fprint(conn, req); err != nil {
		return fmt.Errorf("send handshake request: %w", err)
	}

	// Read response (until blank line).
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
	if err != nil {
		return fmt.Errorf("read handshake response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	// Validate Sec-WebSocket-Accept.
	expected := wsAcceptKey(key)
	got := resp.Header.Get("Sec-Websocket-Accept")
	if got != expected {
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


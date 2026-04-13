package cdp

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// TestWsAcceptKey verifies the RFC 6455 §4.2.2 key derivation using the
// known test vector from the RFC itself (§1.3).
func TestWsAcceptKey(t *testing.T) {
	got := wsAcceptKey("dGhlIHNhbXBsZSBub25jZQ==")
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got != want {
		t.Errorf("wsAcceptKey = %q, want %q", got, want)
	}
}

// writeServerFrame writes an unmasked WebSocket text frame (server → client).
// Per RFC 6455 §5.1 servers MUST NOT mask frames.
func writeServerFrame(w io.Writer, payload []byte) error {
	header := make([]byte, 2)
	header[0] = 0x81 // FIN=1, opcode=1 (text)

	length := len(payload)
	var extra []byte
	switch {
	case length < 126:
		header[1] = byte(length)
	case length < 65536:
		header[1] = 126
		extra = make([]byte, 2)
		binary.BigEndian.PutUint16(extra, uint16(length))
	default:
		header[1] = 127
		extra = make([]byte, 8)
		binary.BigEndian.PutUint64(extra, uint64(length))
	}

	frame := append(header, extra...)
	frame = append(frame, payload...)
	_, err := w.Write(frame)
	return err
}

// newTestClient creates a Client backed by one end of a net.Pipe and starts
// its readLoop. The caller gets the other end to act as a fake CDP server.
func newTestClient(t *testing.T) (*Client, net.Conn) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		clientConn.Close()
		serverConn.Close()
	})

	c := &Client{
		conn:      clientConn,
		logger:    slog.Default(),
		pending:   make(map[int]chan *Message),
		listeners: make(map[string][]chan Event),
		done:      make(chan struct{}),
	}
	go c.readLoop()
	t.Cleanup(func() { c.Close() })

	return c, serverConn
}

func TestReadFrameSmall(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	payload := []byte(`{"method":"Page.loadEventFired"}`)
	go func() { _ = writeServerFrame(serverConn, payload) }()

	got, err := readFrame(clientConn)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("readFrame = %q, want %q", got, payload)
	}
}

// TestReadFrameLarge exercises the 16-bit extended length path (payload > 125 bytes).
func TestReadFrameLarge(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	payload := make([]byte, 300)
	for i := range payload {
		payload[i] = 'x'
	}
	go func() { _ = writeServerFrame(serverConn, payload) }()

	got, err := readFrame(clientConn)
	if err != nil {
		t.Fatalf("readFrame large: %v", err)
	}
	if len(got) != len(payload) {
		t.Errorf("readFrame large len = %d, want %d", len(got), len(payload))
	}
}

// TestClientSend verifies the full command → response round-trip:
// Send serialises the request, the fake server echoes a matching response,
// and the result is correctly deserialised.
func TestClientSend(t *testing.T) {
	c, server := newTestClient(t)

	go func() {
		// Read request from client (masked frame).
		data, err := readFrame(server)
		if err != nil {
			return
		}
		var req Message
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}
		// Reply with matching ID.
		resp := Message{ID: req.ID, Result: map[string]any{"value": float64(42)}}
		b, _ := json.Marshal(resp)
		_ = writeServerFrame(server, b)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var result map[string]any
	if err := c.Send(ctx, "Runtime.evaluate", map[string]any{"expression": "1+1"}, &result); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result["value"] != float64(42) {
		t.Errorf("result[value] = %v, want 42", result["value"])
	}
}

// TestClientSendError verifies that a CDP error response is surfaced as a Go error.
func TestClientSendError(t *testing.T) {
	c, server := newTestClient(t)

	go func() {
		data, err := readFrame(server)
		if err != nil {
			return
		}
		var req Message
		_ = json.Unmarshal(data, &req)
		resp := Message{ID: req.ID, Error: &Error{Code: -32601, Message: "method not found"}}
		b, _ := json.Marshal(resp)
		_ = writeServerFrame(server, b)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.Send(ctx, "Unknown.method", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "method not found" {
		t.Errorf("error = %q, want \"method not found\"", err.Error())
	}
}

// TestClientSubscribe verifies that server-sent events are delivered to
// channels registered with Subscribe.
func TestClientSubscribe(t *testing.T) {
	c, server := newTestClient(t)

	ch := c.Subscribe("Page.loadEventFired")

	go func() {
		evt := Message{Method: "Page.loadEventFired", Params: map[string]any{"timestamp": 1.0}}
		b, _ := json.Marshal(evt)
		_ = writeServerFrame(server, b)
	}()

	select {
	case e := <-ch:
		if e.Method != "Page.loadEventFired" {
			t.Errorf("event method = %q, want Page.loadEventFired", e.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for Page.loadEventFired event")
	}
}

// TestClientSendContextCancel verifies that Send returns ctx.Err() when the
// context is cancelled before a response arrives.
func TestClientSendContextCancel(t *testing.T) {
	c, server := newTestClient(t)

	// Drain frames so writeFrame doesn't block on net.Pipe, but never send a response.
	go func() {
		for {
			if _, err := readFrame(server); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Send so the select hits ctx.Done()

	err := c.Send(ctx, "Page.enable", nil, nil)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
}

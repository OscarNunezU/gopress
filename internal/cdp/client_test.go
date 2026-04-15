package cdp

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
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
		conn:             clientConn,
		reader:           bufio.NewReader(clientConn),
		logger:           slog.Default(),
		pending:          make(map[int]chan *Message),
		listeners:        make(map[string][]chan Event),
		sessionListeners: make(map[string]map[string][]chan Event),
		done:             make(chan struct{}),
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

	_, got, err := readFrame(clientConn)
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

	_, got, err := readFrame(clientConn)
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
		_, data, err := readFrame(server)
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
		_, data, err := readFrame(server)
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
			if _, _, err := readFrame(server); err != nil {
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

// TestReadFrame64BitLength exercises the 64-bit extended payload length path
// (frames whose payload exceeds 65 535 bytes).
func TestReadFrame64BitLength(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	payload := make([]byte, 65536) // triggers 64-bit length in writeServerFrame
	for i := range payload {
		payload[i] = 'y'
	}
	go func() { _ = writeServerFrame(serverConn, payload) }()

	_, got, err := readFrame(clientConn)
	if err != nil {
		t.Fatalf("readFrame 64-bit: %v", err)
	}
	if len(got) != len(payload) {
		t.Errorf("readFrame 64-bit len = %d, want %d", len(got), len(payload))
	}
}

// TestReadFrameMaxSizeExceeded verifies that readFrame rejects a frame whose
// declared length exceeds maxWSFrameSize without reading the payload.
func TestReadFrameMaxSizeExceeded(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	go func() {
		// Write a frame header declaring a payload larger than maxWSFrameSize.
		header := []byte{0x81, 127} // FIN=1 text, 64-bit extended length
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(maxWSFrameSize)+1)
		_, _ = serverConn.Write(header)
		_, _ = serverConn.Write(ext)
	}()

	_, _, err := readFrame(clientConn)
	if err == nil {
		t.Fatal("expected error for oversized frame, got nil")
	}
}

// TestReadLoopHandlesPing verifies that the readLoop responds to a WebSocket
// ping frame (opcode 0x9) with a pong frame (opcode 0xA).
func TestReadLoopHandlesPing(t *testing.T) {
	c, server := newTestClient(t)
	_ = c

	pongReceived := make(chan struct{})
	go func() {
		// Write a ping frame (server → client): FIN=1, opcode=9, length=0.
		ping := []byte{0x89, 0x00}
		if _, err := server.Write(ping); err != nil {
			return
		}
		// Read the masked pong that the readLoop sends back.
		buf := make([]byte, 16)
		n, err := server.Read(buf)
		if err != nil || n < 2 {
			return
		}
		if buf[0] == 0x8A { // FIN=1, opcode=0xA (pong)
			close(pongReceived)
		}
	}()

	select {
	case <-pongReceived:
		// pass
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for pong response to ping")
	}
}

// TestReadLoopIgnoresPong verifies that a pong frame (opcode 0xA) is silently
// discarded and the readLoop continues processing subsequent messages.
func TestReadLoopIgnoresPong(t *testing.T) {
	c, server := newTestClient(t)
	ch := c.Subscribe("Page.loadEventFired")

	go func() {
		// Send a pong frame first (server → client): FIN=1, opcode=10, length=0.
		pong := []byte{0x8A, 0x00}
		_, _ = server.Write(pong)
		// Then send a real event.
		evt := Message{Method: "Page.loadEventFired"}
		b, _ := json.Marshal(evt)
		_ = writeServerFrame(server, b)
	}()

	select {
	case e := <-ch:
		if e.Method != "Page.loadEventFired" {
			t.Errorf("event method = %q, want Page.loadEventFired", e.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event after pong frame")
	}
}

// TestReadLoopIgnoresInvalidJSON verifies that an unparseable frame is logged
// and discarded — the readLoop does not exit.
func TestReadLoopIgnoresInvalidJSON(t *testing.T) {
	c, server := newTestClient(t)
	ch := c.Subscribe("Page.loadEventFired")

	go func() {
		// First frame: invalid JSON.
		_ = writeServerFrame(server, []byte("{not valid json}"))
		// Second frame: well-formed event.
		evt := Message{Method: "Page.loadEventFired"}
		b, _ := json.Marshal(evt)
		_ = writeServerFrame(server, b)
	}()

	select {
	case <-ch:
		// pass: readLoop recovered from invalid JSON
	case <-time.After(2 * time.Second):
		t.Fatal("timeout — readLoop did not recover from invalid JSON frame")
	}
}

// TestReadLoopSkipsEmptyMethod verifies that a message with no method and no ID
// (neither an event nor a response) is silently skipped.
func TestReadLoopSkipsEmptyMethod(t *testing.T) {
	c, server := newTestClient(t)
	ch := c.Subscribe("Page.loadEventFired")

	go func() {
		// Message with params but no method and no ID.
		_ = writeServerFrame(server, []byte(`{"params":{}}`))
		// Real event follows.
		evt := Message{Method: "Page.loadEventFired"}
		b, _ := json.Marshal(evt)
		_ = writeServerFrame(server, b)
	}()

	select {
	case <-ch:
		// pass
	case <-time.After(2 * time.Second):
		t.Fatal("timeout — readLoop did not skip empty-method message")
	}
}

// TestSessionSubscribeEvent verifies that session-scoped events (those with a
// non-empty SessionID) are routed to the correct session's Subscribe channel.
func TestSessionSubscribeEvent(t *testing.T) {
	c, server := newTestClient(t)

	const sessionID = "session-abc"
	sess := c.NewSession(sessionID)
	ch := sess.Subscribe("Page.loadEventFired")

	go func() {
		evt := Message{Method: "Page.loadEventFired", SessionID: sessionID}
		b, _ := json.Marshal(evt)
		_ = writeServerFrame(server, b)
	}()

	select {
	case e := <-ch:
		if e.Method != "Page.loadEventFired" {
			t.Errorf("event method = %q, want Page.loadEventFired", e.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for session-scoped event")
	}
}

// TestWriteFrameLargePath verifies that Send correctly uses the 16-bit extended
// length field for payloads between 126 and 65 535 bytes.
func TestWriteFrameLargePath(t *testing.T) {
	c, server := newTestClient(t)

	// Build a command whose JSON serialisation exceeds 125 bytes so writeFrame
	// must use the 16-bit extended length (value 126 in the length octet).
	largeParam := strings.Repeat("a", 200)

	go func() {
		_, data, err := readFrame(server)
		if err != nil {
			return
		}
		var req Message
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}
		resp := Message{ID: req.ID}
		b, _ := json.Marshal(resp)
		_ = writeServerFrame(server, b)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.Send(ctx, "Runtime.evaluate", map[string]any{"expression": largeParam}, nil); err != nil {
		t.Fatalf("Send with large payload: %v", err)
	}
}

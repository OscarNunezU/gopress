// Package cdp implements a minimal Chrome DevTools Protocol client
// over WebSocket using only the Go standard library.
package cdp

// Message is the base JSON-RPC envelope used by the CDP protocol.
type Message struct {
	ID     int    `json:"id,omitempty"`
	Method string `json:"method,omitempty"`
	Params any    `json:"params,omitempty"`
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

// Error represents a CDP protocol error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return e.Message
}

// Event is a server-sent CDP event (no ID).
type Event struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

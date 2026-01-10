package eventsource

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"unicode/utf8"
)

// The FlushWriter interface groups basic Write and Flush methods.
type FlushWriter interface {
	io.Writer
	Flush()
}

// Adds a noop Flush method to a normal io.Writer.
type noopFlusher struct {
	io.Writer
}

func (noopFlusher) Flush() {}

// Encoder writes EventSource events to an output stream.
type Encoder struct {
	mu      sync.Mutex
	w       FlushWriter
	request *http.Request
	ctx     context.Context
	closed  bool
}

// NewEncoder returns a new encoder that writes to w.
func NewEncoder(w io.Writer) *Encoder {
	if fw, ok := w.(FlushWriter); ok {
		return &Encoder{w: fw}
	}

	return &Encoder{w: noopFlusher{w}}
}

// NewEncoderWithRequest returns a new encoder with associated HTTP request and context.
func NewEncoderWithRequest(w io.Writer, r *http.Request) *Encoder {
	enc := NewEncoder(w)
	enc.request = r
	if r != nil {
		enc.ctx = r.Context()
	}
	return enc
}

// Flush sends an empty line to signal event is complete, and flushes the
// writer.
func (e *Encoder) Flush() error {
	_, err := e.w.Write([]byte{'\n'})
	e.w.Flush()
	return err
}

// WriteField writes an event field to the connection. If the provided value
// contains newlines, multiple fields will be emitted. If the returned error is
// not nil, it will be either ErrInvalidEncoding or an error from the
// connection.
func (e *Encoder) WriteField(field string, value []byte) error {
	if !utf8.ValidString(field) || !utf8.Valid(value) {
		return ErrInvalidEncoding
	}

	lines := bytes.Split(value, []byte{'\n'})
	for i, line := range lines {
		// Skip empty lines except when they're part of multi-line data
		if len(line) == 0 && i > 0 && i < len(lines)-1 {
			continue
		}

		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}

		if err := e.writeField(field, line); err != nil {
			return err
		}
	}

	return nil
}

func (e *Encoder) writeField(field string, value []byte) (err error) {
	if len(value) == 0 {
		_, err = fmt.Fprintf(e.w, "%s\n", field)
	} else {
		_, err = fmt.Fprintf(e.w, "%s: %s\n", field, value)
	}

	return
}

// Encode writes an event to the connection.
func (e *Encoder) Encode(event Event) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrEncoderClosed
	}
	e.mu.Unlock()

	if event.ResetID {
		// Send "id:" with empty value to reset the last event ID
		if _, err := fmt.Fprintf(e.w, "id:\n"); err != nil {
			return e.handleEncodeError(err)
		}
	} else if len(event.ID) > 0 {
		if err := e.WriteField("id", []byte(event.ID)); err != nil {
			return e.handleEncodeError(err)
		}
	}

	if len(event.Retry) > 0 {
		if err := e.WriteField("retry", []byte(event.Retry)); err != nil {
			return e.handleEncodeError(err)
		}
	}

	if len(event.Type) > 0 {
		if err := e.WriteField("event", []byte(event.Type)); err != nil {
			return e.handleEncodeError(err)
		}
	}

	if err := e.WriteField("data", event.Data); err != nil {
		return e.handleEncodeError(err)
	}

	if err := e.Flush(); err != nil {
		return e.handleEncodeError(err)
	}

	return nil
}

func (e *Encoder) handleEncodeError(err error) error {
	if err == nil {
		return nil
	}

	// Check if this is a connection error
	if err == io.EOF || err == io.ErrClosedPipe {
		e.mu.Lock()
		e.closed = true
		e.mu.Unlock()
		return ErrConnectionClosed
	}

	return err
}

// SetRetry sets the retry timeout in milliseconds.
// Automatically sends "retry: <value>\n\n" and flushes.
func (e *Encoder) SetRetry(milliseconds int) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrEncoderClosed
	}
	e.mu.Unlock()

	value := []byte(fmt.Sprintf("%d", milliseconds))
	if err := e.WriteField("retry", value); err != nil {
		return e.handleEncodeError(err)
	}
	if err := e.Flush(); err != nil {
		return e.handleEncodeError(err)
	}
	return nil
}

// Close closes the encoder and the underlying connection.
func (e *Encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return nil
	}

	e.closed = true
	if e.w != nil {
		// Close underlying writer if possible
		if closer, ok := e.w.(io.Closer); ok {
			return closer.Close()
		}
	}
	return nil
}

// IsClosed checks if the encoder is closed.
func (e *Encoder) IsClosed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

// GetRequest returns the HTTP request associated with the encoder.
func (e *Encoder) GetRequest() *http.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.request
}

// Context returns the context associated with the connection.
func (e *Encoder) Context() context.Context {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx != nil {
		return e.ctx
	}
	if e.request != nil {
		return e.request.Context()
	}
	return context.Background()
}

// RemoteAddr returns the client address.
func (e *Encoder) RemoteAddr() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.request != nil {
		return e.request.RemoteAddr
	}
	return ""
}

// Path returns the request path.
func (e *Encoder) Path() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.request != nil && e.request.URL != nil {
		return e.request.URL.Path
	}
	return ""
}

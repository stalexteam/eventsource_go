// Package eventsource provides SSE client with auto-reconnect and error reporting.
package eventsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"sync"
	"time"
)

var (
	ErrClosed           = errors.New("closed")
	ErrConnectionFailed = errors.New("connection failed")
	ErrEmptyLine        = errors.New("empty line received")
	ErrInvalidEncoding  = errors.New("invalid UTF-8 sequence")
)

// Event represents a single SSE event.
type Event struct {
	Type    string
	ID      string
	Retry   string
	Data    []byte
	ResetID bool
}

// EventSource reads SSE events from a server with auto-reconnect and callbacks.
type EventSource struct {
	mu sync.RWMutex

	request     *http.Request
	r           io.ReadCloser
	dec         *Decoder
	lastEventID string

	// IdleTimeout is the read timeout for idle connections.
	// It can be set directly, but SetIdleTimeout() is recommended for thread-safe updates.
	IdleTimeout       time.Duration
	ConnectionTimeout time.Duration

	transport *http.Transport
	client    *http.Client

	OnConnect    func(url string)
	OnDisconnect func(url string, err error)
	OnError      func(url string, err error)
}

// New prepares an EventSource.
func New(req *http.Request) *EventSource {
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	es := &EventSource{
		request:           req,
		IdleTimeout:       15 * time.Second, // default read timeout
		ConnectionTimeout: 10 * time.Second, // default connection timeout
	}

	// Create reusable transport and client
	es.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			timeout := es.ConnectionTimeout
			if timeout == 0 {
				timeout = 10 * time.Second
			}
			return net.DialTimeout(network, addr, timeout)
		},
	}

	es.client = &http.Client{
		Transport: es.transport,
		Timeout:   0, // No timeout for long-lived SSE connections
	}

	return es
}

// SetIdleTimeout sets the read timeout for idle connections.
func (es *EventSource) SetIdleTimeout(timeout time.Duration) {
	es.mu.Lock()
	defer es.mu.Unlock()
	es.IdleTimeout = timeout
}

// Close stops the source permanently.
func (es *EventSource) Close() {
	es.mu.Lock()
	defer es.mu.Unlock()

	if es.r != nil {
		_ = es.r.Close()
		es.r = nil
	}
	es.dec = nil

	if es.transport != nil {
		es.transport.CloseIdleConnections()
	}
}

// connect attempt
func (es *EventSource) connect() bool {
	// Fast path: check if already connected without blocking
	es.mu.RLock()
	if es.r != nil {
		es.mu.RUnlock()
		return true // already connected.
	}
	es.mu.RUnlock()

	// Check if request context is cancelled before attempting connection
	if es.request.Context().Err() != nil {
		return false
	}

	// Prepare connection parameters under read lock
	es.mu.RLock()
	url := es.request.URL.String()
	lastEventID := es.lastEventID
	idleTimeout := es.IdleTimeout
	connectionTimeout := es.ConnectionTimeout
	es.mu.RUnlock()

	// Check again after releasing lock (double-check pattern)
	es.mu.Lock()
	if es.r != nil {
		es.mu.Unlock()
		return true // another goroutine connected while we were waiting
	}
	es.mu.Unlock()

	// Set header (this modifies the request, but it's safe as we're the only one connecting)
	es.request.Header.Set("Last-Event-Id", lastEventID)

	var tcpConn net.Conn
	// Create a temporary transport for this connection to capture the TCP connection
	tempTransport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			timeout := connectionTimeout
			if timeout == 0 {
				timeout = 10 * time.Second
			}
			conn, err := net.DialTimeout(network, addr, timeout)
			if err != nil {
				return nil, err
			}
			tcpConn = conn
			return conn, nil
		},
	}
	defer tempTransport.CloseIdleConnections()

	tempClient := &http.Client{
		Transport: tempTransport,
		Timeout:   0, // No timeout for long-lived SSE connections
	}

	// Perform HTTP request WITHOUT holding the lock (this can take a long time)
	resp, err := tempClient.Do(es.request)
	if err != nil {
		if es.OnError != nil {
			es.OnError(url, fmt.Errorf("connection attempt failed: %w", err))
		}
		return false
	}

	// Check response status (still without lock)
	switch {
	case resp.StatusCode >= 500:
		_ = resp.Body.Close()
		if es.OnError != nil {
			es.OnError(url, fmt.Errorf("temporary server error: %s", resp.Status))
		}
		return false

	case resp.StatusCode == 204:
		_ = resp.Body.Close()
		if es.OnDisconnect != nil {
			es.OnDisconnect(url, ErrClosed)
		}
		return false

	case resp.StatusCode != 200:
		_ = resp.Body.Close()
		if es.OnError != nil {
			es.OnError(url, fmt.Errorf("unrecoverable HTTP status: %s", resp.Status))
		}
		return false

	default:
		mediatype, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if mediatype != "text/event-stream" {
			_ = resp.Body.Close()
			if es.OnError != nil {
				es.OnError(url, fmt.Errorf("invalid content type: %s", resp.Header.Get("Content-Type")))
			}
			return false
		}
	}

	// Now acquire lock to set connection state
	es.mu.Lock()
	// Double-check: another goroutine might have connected while we were doing HTTP request
	if es.r != nil {
		// Another goroutine connected first, close our response
		es.mu.Unlock()
		_ = resp.Body.Close()
		return true
	}

	// wrap body (use the idleTimeout we captured earlier)
	es.r = &timeoutReader{
		conn:    tcpConn,
		r:       resp.Body,
		timeout: idleTimeout,
	}
	es.dec = NewDecoder(es.r)
	es.mu.Unlock()

	// Call callback outside of lock to avoid potential deadlocks
	if es.OnConnect != nil {
		es.OnConnect(url)
	}

	return true
}

// Read returns the next SSE event, reconnecting if needed.
// Read() is safe to call from multiple goroutines, but each call will
// read a separate event. For typical use, call Read() from a single goroutine.
func (es *EventSource) Read() (Event, error) {
	// Check context cancellation
	if es.request.Context().Err() != nil {
		return Event{}, es.request.Context().Err()
	}

	// connect if need.
	if !es.connect() {
		return Event{}, ErrConnectionFailed
	}

	// read line && decode.
	var e Event
	var err error

	es.mu.RLock()
	dec := es.dec
	es.mu.RUnlock()

	if dec == nil {
		return Event{}, ErrConnectionFailed
	}

	err = dec.Decode(&e)

	// process errors.
	if err != nil {
		if err != ErrInvalidEncoding {
			// treat network errors as disconnect
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				err = fmt.Errorf("read timeout: %w", err)
			}

			es.mu.Lock()
			if es.r != nil {
				_ = es.r.Close()
			}
			es.r = nil
			es.dec = nil
			es.mu.Unlock()

			if es.OnDisconnect != nil {
				es.OnDisconnect(es.request.URL.String(), err)
			}
		}

		return Event{}, err
	}

	if len(e.Data) == 0 {
		return Event{}, ErrEmptyLine
	}

	// Update lastEventID: if ResetID is true, clear it; otherwise use the ID if present
	es.mu.Lock()
	if e.ResetID {
		es.lastEventID = ""
	} else if len(e.ID) > 0 {
		es.lastEventID = e.ID
	}
	es.mu.Unlock()

	return e, nil
}

// timeoutReader wraps an io.ReadCloser to enforce a read timeout.
type timeoutReader struct {
	conn    net.Conn
	r       io.Reader
	timeout time.Duration
}

func (t *timeoutReader) Read(p []byte) (int, error) {
	if t.timeout > 0 {
		t.conn.SetReadDeadline(time.Now().Add(t.timeout))
	}
	return t.r.Read(p)
}

func (t *timeoutReader) Close() error {
	var err error
	if closer, ok := t.r.(io.Closer); ok {
		if closeErr := closer.Close(); closeErr != nil {
			err = closeErr
		}
	}
	if connErr := t.conn.Close(); connErr != nil && err == nil {
		err = connErr
	}
	return err
}

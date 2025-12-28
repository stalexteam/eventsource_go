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
	request     *http.Request
	r           io.ReadCloser
	dec         *Decoder
	lastEventID string

	IdleTimeout time.Duration

	OnConnect    func(url string)
	OnDisconnect func(url string, err error)
	OnError      func(url string, err error)
}

// New prepares an EventSource.
func New(req *http.Request) *EventSource {
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	return &EventSource{
		request:     req,
		IdleTimeout: 15 * time.Second, // default timeout
	}
}

// Close stops the source permanently.
func (es *EventSource) Close() {
	if es.r != nil {
		_ = es.r.Close()
	}
}

// connect attempt
func (es *EventSource) connect() bool {
	if es.r != nil {
		return true // already connected.
	}

	url := es.request.URL.String()
	es.request.Header.Set("Last-Event-Id", es.lastEventID)

	var tcpConn net.Conn
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := net.DialTimeout(network, addr, es.IdleTimeout)
			if err != nil {
				return nil, err
			}
			tcpConn = conn
			return conn, nil
		},
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   0,
	}

	resp, err := client.Do(es.request)
	if err != nil {
		if es.OnError != nil {
			es.OnError(url, fmt.Errorf("connection attempt failed: %w", err))
		}
		return false
	}

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

	// wrap body
	es.r = &timeoutReader{
		conn:    tcpConn,
		r:       resp.Body,
		timeout: es.IdleTimeout,
	}
	es.dec = NewDecoder(es.r)

	if es.OnConnect != nil {
		es.OnConnect(url)
	}

	return true
}

// Read returns the next SSE event, reconnecting if needed.
func (es *EventSource) Read() (Event, error) {
	// connect if need.
	if !es.connect() {
		return Event{}, ErrConnectionFailed
	}

	// read line && decode.
	var e Event
	err := es.dec.Decode(&e)

	// process errors.
	if err != nil {
		if err != ErrInvalidEncoding {
			// treat network errors as disconnect
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				err = fmt.Errorf("read timeout: %w", err)
			}
			es.r = nil

			if es.OnDisconnect != nil {
				es.OnDisconnect(es.request.URL.String(), err)
			}
		}

		return Event{}, err
	}

	if len(e.Data) == 0 {
		return Event{}, ErrEmptyLine
	}

	if len(e.ID) > 0 || e.ResetID {
		es.lastEventID = e.ID
	}

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
	n, err := t.r.Read(p)
	if n > 0 && t.timeout > 0 {
		t.conn.SetReadDeadline(time.Now().Add(t.timeout))
	}
	return n, err
}

func (t *timeoutReader) Close() error {
	return t.conn.Close()
}

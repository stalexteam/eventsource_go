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
	"strconv"
	"time"
)

var (
	ErrClosed          = errors.New("closed")
	ErrInvalidEncoding = errors.New("invalid UTF-8 sequence")
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
	retry       time.Duration
	request     *http.Request
	err         error
	r           io.ReadCloser
	dec         *Decoder
	lastEventID string

	IdleTimeout time.Duration
	RetryOverride time.Duration

	OnConnect    func(url string)
	OnDisconnect func(url string, err error)
	OnError      func(url string, err error)
}

// New prepares an EventSource. retry is the reconnection interval.
func New(req *http.Request, retry time.Duration) *EventSource {
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	return &EventSource{
		retry:       retry,
		request:     req,
		IdleTimeout: 15 * time.Second, // default timeout
		RetryOverride: 0,
	}
}

// Close stops the source permanently.
func (es *EventSource) Close() {
	if es.r != nil {
		_ = es.r.Close()
	}
	es.err = ErrClosed
}

// connect attempts to establish or re-establish the connection.
func (es *EventSource) connect() {
	url := es.request.URL.String()

	for es.err == nil {
		if es.r != nil {
			_ = es.r.Close()
			time.Sleep(es.retry)
		}

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
			time.Sleep(es.retry)
			continue
		}

		switch {
		case resp.StatusCode >= 500:
			_ = resp.Body.Close()
			if es.OnError != nil {
				es.OnError(url, fmt.Errorf("temporary server error: %s", resp.Status))
			}
			time.Sleep(es.retry)
			continue

		case resp.StatusCode == 204:
			_ = resp.Body.Close()
			es.err = ErrClosed
			if es.OnDisconnect != nil {
				es.OnDisconnect(url, es.err)
			}
			return

		case resp.StatusCode != 200:
			_ = resp.Body.Close()
			es.err = fmt.Errorf("unrecoverable HTTP status: %s", resp.Status)
			if es.OnError != nil {
				es.OnError(url, es.err)
			}
			return

		default:
			mediatype, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			if mediatype != "text/event-stream" {
				_ = resp.Body.Close()
				es.err = fmt.Errorf("invalid content type: %s", resp.Header.Get("Content-Type"))
				if es.OnError != nil {
					es.OnError(url, es.err)
				}
				return
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

		return
	}
}

// Read returns the next SSE event, reconnecting if needed.
func (es *EventSource) Read() (Event, error) {
	if es.err == ErrClosed {
		return Event{}, ErrClosed
	}

	if es.r == nil {
		es.connect()
	}

	for es.err == nil {
		var e Event
		err := es.dec.Decode(&e)

		if err == ErrInvalidEncoding {
			continue
		}

		if err != nil {
			// treat network errors as disconnect
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				err = fmt.Errorf("read timeout: %w", err)
			}

			if es.OnDisconnect != nil {
				es.OnDisconnect(es.request.URL.String(), err)
			}
			es.connect()
			continue
		}

		if len(e.Data) == 0 {
			continue
		}

		if len(e.ID) > 0 || e.ResetID {
			es.lastEventID = e.ID
		}

		if len(e.Retry) > 0 {
			if retry, err := strconv.Atoi(e.Retry); err == nil {
				if es.RetryOverride != 0 {
					es.retry = es.RetryOverride // <-- используем переопределение
				} else {
					es.retry = time.Duration(retry) * time.Millisecond
				}
			}
		}

		return e, nil
	}

	return Event{}, es.err
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

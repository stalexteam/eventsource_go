// Package eventsource provides the building blocks for consuming and building
// EventSource services, with better visibility and reconnect diagnostics.
package eventsource

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"
)

var (
	// ErrClosed signals that the event source has been closed and will not be reopened.
	ErrClosed = errors.New("closed")

	// ErrInvalidEncoding is returned by Encoder and Decoder when invalid UTF-8 event data is encountered.
	ErrInvalidEncoding = errors.New("invalid UTF-8 sequence")
)

// Event represents one SSE event.
type Event struct {
	Type    string
	ID      string
	Retry   string
	Data    []byte
	ResetID bool
}

// EventSource consumes server-sent events with auto-reconnect and state reporting.
type EventSource struct {
	retry       time.Duration
	request     *http.Request
	err         error
	r           io.ReadCloser
	dec         *Decoder
	lastEventID string

	// optional callbacks
	OnConnect    func(url string)
	OnDisconnect func(url string, err error)
	OnError      func(url string, err error)
}

// New prepares an EventSource. It reconnects after transient errors with the provided retry interval.
func New(req *http.Request, retry time.Duration) *EventSource {
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	return &EventSource{
		retry:   retry,
		request: req,
	}
}

// Close stops the stream permanently.
func (es *EventSource) Close() {
	if es.r != nil {
		_ = es.r.Close()
	}
	es.err = ErrClosed
}

// Connect tries to establish or re-establish a connection.
func (es *EventSource) connect() {
	url := es.request.URL.String()

	for es.err == nil {
		if es.r != nil {
			_ = es.r.Close()
			time.Sleep(es.retry)
		}

		es.request.Header.Set("Last-Event-Id", es.lastEventID)

		resp, err := http.DefaultClient.Do(es.request)
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

		es.r = resp.Body
		es.dec = NewDecoder(es.r)

		if es.OnConnect != nil {
			es.OnConnect(url)
		}

		return
	}
}

// Read returns the next event, reconnecting if possible.
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
				es.retry = time.Duration(retry) * time.Millisecond
			}
		}

		return e, nil
	}

	return Event{}, es.err
}

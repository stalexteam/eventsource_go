package eventsource

import (
	"context"
	"mime"
	"net/http"
	"strings"
)

// ConnectionInfo contains information about the SSE connection.
type ConnectionInfo struct {
	Request *http.Request
	LastID  string
	Context context.Context
}

// Handler is an adapter for ordinary functions to act as an HTTP handler for
// event sources. It receives the ID of the last event processed by the client,
// and Encoder to deliver messages, and a channel to be notified if the client
// connection is closed.
// The stop channel will be closed when the client disconnects or the request
// context is cancelled.
// Deprecated: Use HandlerV2 for access to ConnectionInfo and http.Request.
type Handler func(lastId string, encoder *Encoder, stop <-chan bool)

// HandlerV2 is the updated handler that provides access to ConnectionInfo,
// which includes the HTTP request, last event ID, and context.
type HandlerV2 func(info *ConnectionInfo, encoder *Encoder, stop <-chan bool)

func (h Handler) acceptable(accept string) bool {
	if accept == "" {
		// The absense of an Accept header is equivalent to "*/*".
		// https://tools.ietf.org/html/rfc2296#section-4.2.2
		return true
	}

	for _, a := range strings.Split(accept, ",") {
		mediatype, _, err := mime.ParseMediaType(a)
		if err != nil {
			continue
		}

		if mediatype == "text/event-stream" || mediatype == "text/*" || mediatype == "*/*" {
			return true
		}
	}

	return false
}

// ServeHTTP calls h with an Encoder and a close notification channel. It
// performs Content-Type negotiation.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept")

	if !h.acceptable(r.Header.Get("Accept")) {
		w.WriteHeader(http.StatusNotAcceptable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	// Use request context for cancellation instead of deprecated CloseNotifier
	stop := make(chan bool, 1)
	go func() {
		<-r.Context().Done()
		close(stop)
	}()

	lastId := r.Header.Get("Last-Event-Id")
	encoder := NewEncoderWithRequest(w, r)
	h(lastId, encoder, stop)
}

// ServeHTTP implements http.Handler for HandlerV2.
func (h HandlerV2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept")

	// Check Accept header
	accept := r.Header.Get("Accept")
	if accept == "" {
		// The absence of an Accept header is equivalent to "*/*".
		// https://tools.ietf.org/html/rfc2296#section-4.2.2
	} else {
		acceptable := false
		for _, a := range strings.Split(accept, ",") {
			mediatype, _, err := mime.ParseMediaType(a)
			if err != nil {
				continue
			}

			if mediatype == "text/event-stream" || mediatype == "text/*" || mediatype == "*/*" {
				acceptable = true
				break
			}
		}
		if !acceptable {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	// Use request context for cancellation
	stop := make(chan bool, 1)
	go func() {
		<-r.Context().Done()
		close(stop)
	}()

	lastId := r.Header.Get("Last-Event-Id")
	encoder := NewEncoderWithRequest(w, r)

	info := &ConnectionInfo{
		Request: r,
		LastID:  lastId,
		Context: r.Context(),
	}

	h(info, encoder, stop)
}

// HandlerWithManager creates a handler with a connection manager.
// The manager will automatically register connections when they are established
// and unregister them when they are closed.
func HandlerWithManager(manager *ConnectionManager, handler HandlerV2) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Vary", "Accept")

		// Check Accept header
		accept := r.Header.Get("Accept")
		if accept == "" {
			// The absence of an Accept header is equivalent to "*/*".
			// https://tools.ietf.org/html/rfc2296#section-4.2.2
		} else {
			acceptable := false
			for _, a := range strings.Split(accept, ",") {
				mediatype, _, err := mime.ParseMediaType(a)
				if err != nil {
					continue
				}

				if mediatype == "text/event-stream" || mediatype == "text/*" || mediatype == "*/*" {
					acceptable = true
					break
				}
			}
			if !acceptable {
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Use request context for cancellation
		stop := make(chan bool, 1)
		go func() {
			<-r.Context().Done()
			close(stop)
		}()

		lastId := r.Header.Get("Last-Event-Id")
		encoder := NewEncoderWithRequest(w, r)

		info := &ConnectionInfo{
			Request: r,
			LastID:  lastId,
			Context: r.Context(),
		}

		manager.Register(encoder, info)
		defer manager.Unregister(encoder)

		handler(info, encoder, stop)
	})
}

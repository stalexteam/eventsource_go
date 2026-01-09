## eventsource (fork)

`eventsource` is a Go package for consuming and building
[Server-Sent Events (SSE)][spec] services, with added visibility,
reconnect diagnostics, and optional callbacks for connection errors
and disconnections.

This fork improves on the original by:

- Reporting connection errors via `OnError` without stack traces.
- Reporting successful connections via `OnConnect`.
- Reporting disconnections via `OnDisconnect`.
- Auto-reconnect on recoverable errors.
- Optional read timeout support via `SetIdleTimeout`.
- Thread-safe operations with proper synchronization.

### Installing

```bash
$ go get github.com/stalexteam/eventsource_go
```

### Importing

```go
import "github.com/stalexteam/eventsource_go"
```

### Usage Example

#### Client (Consumer)

```go
import (
    "net/http"
    "time"
    "github.com/stalexteam/eventsource_go"
)

// Create HTTP request
req, err := http.NewRequest("GET", "https://example.com/events", nil)
if err != nil {
    log.Fatal(err)
}

// Create EventSource
es := eventsource.New(req)

// Set up callbacks
es.OnConnect = func(url string) {
    log.Printf("SSE connected to %s", url)
}

es.OnDisconnect = func(url string, err error) {
    log.Printf("SSE disconnected from %s: %v", url, err)
}

es.OnError = func(url string, err error) {
    log.Printf("SSE error on %s: %v", url, err)
}

// Optional: set read timeout (default is 15 seconds)
es.SetIdleTimeout(10 * time.Second)

// Read events in a loop
for {
    event, err := es.Read()
    if err != nil {
        if err == eventsource.ErrConnectionFailed {
            // Will auto-reconnect on next Read()
            time.Sleep(1 * time.Second)
            continue
        }
        log.Printf("Error reading event: %v", err)
        break
    }

    // Process event
    log.Printf("Event: type=%s, id=%s, data=%s", event.Type, event.ID, string(event.Data))
}

// Clean up
es.Close()
```

#### Server (Producer)

```go
import (
    "net/http"
    "time"
    "github.com/stalexteam/eventsource_go"
)

func eventHandler(w http.ResponseWriter, r *http.Request) {
    handler := eventsource.Handler(func(lastId string, encoder *eventsource.Encoder, stop <-chan bool) {
        eventID := 0
        for {
            select {
            case <-stop:
                return
            case <-time.After(1 * time.Second):
                eventID++
                event := eventsource.Event{
                    ID:   fmt.Sprintf("%d", eventID),
                    Type: "message",
                    Data: []byte(fmt.Sprintf("Event %d at %s", eventID, time.Now().Format(time.RFC3339))),
                }
                if err := encoder.Encode(event); err != nil {
                    return
                }
            }
        }
    })
    handler.ServeHTTP(w, r)
}

func main() {
    http.HandleFunc("/events", eventHandler)
    http.ListenAndServe(":8080", nil)
}
```

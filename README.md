````markdown
## eventsource (fork)

`eventsource` is a Go package for consuming and building
[Server-Sent Events (SSE)][spec] services, with added visibility,
reconnect diagnostics, and optional callbacks for connection errors
and disconnections.

This fork improves on the original by:

- Reporting connection errors via `OnError` without stack traces.
- Reporting successful connections via `OnConnect`.
- Reporting disconnections via `OnDisconnect`.
- Auto-reconnect on recoverable errors with configurable retry intervals.
- Optional read timeout support via `SetReadTimeout`.

### Installing

```bash
$ go get github.com/stalexteam/eventsource_go
````

### Importing

```go
import "github.com/stalexteam/eventsource_go"
```

### Usage Example

```go
sseIO := &SseIO{
    logger: logger, // *zap.SugaredLogger
    deej:   deejInstance,
    stopChannel: make(chan struct{}),
}

sseIO.es = eventsource.New(req, 3*time.Second)

sseIO.es.OnConnect = func(url string) {
    sseIO.logger.Infow("SSE connected", "url", url)
}

sseIO.es.OnDisconnect = func(url string, err error) {
    sseIO.logger.Warnw("SSE disconnected", "url", url, "error", err)
}

sseIO.es.OnError = func(url string, err error) {
    sseIO.logger.Warnw("Device seems offline or not responding", "url", url, "error", err.Error())
}

// Optional: set read timeout
sseIO.es.SetReadTimeout(10 * time.Second)

// Start reading events
err := sseIO.Start()
if err != nil {
    logger.Warn("Failed to start SSE", "error", err)
}
```

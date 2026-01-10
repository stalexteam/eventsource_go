package eventsource

import (
	"sync"
)

// ConnectionManager manages multiple SSE connections.
type ConnectionManager struct {
	mu         sync.RWMutex
	encoders   map[*Encoder]*ConnectionInfo
	onConnect  func(*Encoder)
	onDisconnect func(*Encoder)
}

// NewConnectionManager creates a new connection manager.
func NewConnectionManager() *ConnectionManager {
	return &ConnectionManager{
		encoders: make(map[*Encoder]*ConnectionInfo),
	}
}

// Register registers a new connection.
func (cm *ConnectionManager) Register(encoder *Encoder, info *ConnectionInfo) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.encoders[encoder] = info
	if cm.onConnect != nil {
		cm.onConnect(encoder)
	}
}

// Unregister removes a connection.
func (cm *ConnectionManager) Unregister(encoder *Encoder) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if info, exists := cm.encoders[encoder]; exists {
		delete(cm.encoders, encoder)
		if cm.onDisconnect != nil {
			cm.onDisconnect(encoder)
		}
		_ = info // Use info to avoid unused variable warning
	}
}

// Broadcast sends an event to all connected clients.
func (cm *ConnectionManager) Broadcast(event Event) error {
	cm.mu.RLock()
	encoders := make([]*Encoder, 0, len(cm.encoders))
	for encoder := range cm.encoders {
		encoders = append(encoders, encoder)
	}
	cm.mu.RUnlock()

	var lastErr error
	for _, encoder := range encoders {
		if err := encoder.Encode(event); err != nil {
			// Automatically remove failed connections
			cm.Unregister(encoder)
			if IsConnectionError(err) {
				lastErr = err
			}
		}
	}
	return lastErr
}

// BroadcastTo sends an event to all connections that pass the filter.
func (cm *ConnectionManager) BroadcastTo(event Event, filter func(*ConnectionInfo) bool) error {
	cm.mu.RLock()
	filtered := make([]*Encoder, 0)
	for encoder, info := range cm.encoders {
		if filter(info) {
			filtered = append(filtered, encoder)
		}
	}
	cm.mu.RUnlock()

	var lastErr error
	for _, encoder := range filtered {
		if err := encoder.Encode(event); err != nil {
			cm.Unregister(encoder)
			if IsConnectionError(err) {
				lastErr = err
			}
		}
	}
	return lastErr
}

// Count returns the number of active connections.
func (cm *ConnectionManager) Count() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.encoders)
}

// List returns a list of all active connection info.
func (cm *ConnectionManager) List() []*ConnectionInfo {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	result := make([]*ConnectionInfo, 0, len(cm.encoders))
	for _, info := range cm.encoders {
		result = append(result, info)
	}
	return result
}

// SetOnConnect sets the callback when a connection is established.
func (cm *ConnectionManager) SetOnConnect(fn func(*Encoder)) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.onConnect = fn
}

// SetOnDisconnect sets the callback when a connection is closed.
func (cm *ConnectionManager) SetOnDisconnect(fn func(*Encoder)) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.onDisconnect = fn
}

// CloseAll closes all connections.
func (cm *ConnectionManager) CloseAll() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for encoder := range cm.encoders {
		_ = encoder.Close()
	}
	cm.encoders = make(map[*Encoder]*ConnectionInfo)
}

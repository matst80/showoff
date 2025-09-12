package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/matst80/showoff/internal/obs"
)

// In-memory state implementation. Redis-backed implementation moved to server-redis-state.go.

type serverState struct {
	mu           sync.Mutex
	clients      map[string]*clientSession // name -> session
	pending      map[string]*pendingInfo   // requestID -> outside public connection + buffered bytes
	closing      bool
	ready        bool
	totalTunnels int64 // Total tunnels created
	timeouts     int64 // Total timeouts
}

func newServerState() *serverState {
	return &serverState{clients: make(map[string]*clientSession), pending: make(map[string]*pendingInfo)}
}

// Ensure serverState implements StateStore interface
var _ StateStore = (*serverState)(nil)

func (s *serverState) setClosing(closing bool) {
	s.mu.Lock()
	s.closing = closing
	s.mu.Unlock()
}

func (s *serverState) setReady(ready bool) {
	s.mu.Lock()
	s.ready = ready
	s.mu.Unlock()
}

func (s *serverState) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

func (s *serverState) isReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

func (s *serverState) registerClient(name string, sess *clientSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.clients[name]; exists {
		return fmt.Errorf("name already registered: %s", name)
	}
	s.clients[name] = sess
	obs.ActiveClients.Set(float64(len(s.clients)))
	return nil
}

func (s *serverState) getClient(name string) *clientSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clients[name]
}

func (s *serverState) setPending(id string, p *pendingInfo) {
	s.mu.Lock()
	s.pending[id] = p
	s.mu.Unlock()
	obs.PendingTunnels.Set(float64(len(s.pending)))
}

func (s *serverState) popPending(id string) *pendingInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[id]
	delete(s.pending, id)
	obs.PendingTunnels.Set(float64(len(s.pending)))
	return p
}

// removeClient removes a client and closes any pending outside connections waiting for it.
func (s *serverState) removeClient(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, name)
	closed := 0
	for id, p := range s.pending {
		if p.clientName == name {
			_ = p.conn.Close()
			delete(s.pending, id)
			closed++
		}
	}
	obs.ActiveClients.Set(float64(len(s.clients)))
	obs.PendingTunnels.Set(float64(len(s.pending)))
	return closed
}

func (s *serverState) cleanupExpiredPending(maxAge time.Duration) {
	var expired []*pendingInfo
	s.mu.Lock()
	if s.closing {
		for id, p := range s.pending { // shutdown: expire all
			expired = append(expired, p)
			delete(s.pending, id)
		}
	} else {
		cutoff := time.Now().Add(-maxAge)
		for id, p := range s.pending {
			if p.created.Before(cutoff) {
				expired = append(expired, p)
				delete(s.pending, id)
			}
		}
	}
	timeoutCount := int64(len(expired))
	s.timeouts += timeoutCount
	obs.PendingTunnels.Set(float64(len(s.pending)))
	s.mu.Unlock()
	for _, p := range expired {
		_, _ = p.conn.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\nContent-Type: text/plain\r\nContent-Length: 15\r\n\r\nGateway Timeout"))
		_ = p.conn.Close()
		obs.TunnelTimeoutTotal.Inc()
	}
}

// stats helpers
func (s *serverState) getStats() (int, int, int64, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients), len(s.pending), s.totalTunnels, s.timeouts
}

func (s *serverState) incrementTunnelCount() {
	s.mu.Lock()
	s.totalTunnels++
	s.mu.Unlock()
}

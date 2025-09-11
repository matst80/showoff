package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/matst80/showoff/internal/obs"
)

type clientSession struct {
	name        string
	controlConn net.Conn
	lastSeen    time.Time
}

// clientSessionData is the JSON-serializable version for Redis storage
type clientSessionData struct {
	Name     string    `json:"name"`
	LastSeen time.Time `json:"last_seen"`
	// Note: controlConn cannot be serialized, so Redis-backed sessions need special handling
}

// pendingInfo tracks an outside public connection waiting for a client data tunnel.
type pendingInfo struct {
	conn       net.Conn
	initialBuf []byte // initial request bytes already consumed from outside
	clientName string // owning client
	created    time.Time
	readyCh    chan struct{} // closed when data tunnel established
}

// pendingInfoData is the JSON-serializable version for Redis storage
type pendingInfoData struct {
	InitialBuf []byte    `json:"initial_buf"`
	ClientName string    `json:"client_name"`
	Created    time.Time `json:"created"`
	// Note: conn and readyCh cannot be serialized - these need special handling in Redis mode
}

// StateStore interface abstracts server state management for horizontal scaling
type StateStore interface {
	registerClient(name string, sess *clientSession) error
	getClient(name string) *clientSession
	removeClient(name string) int
	setPending(id string, p *pendingInfo)
	popPending(id string) *pendingInfo
	cleanupExpiredPending(maxAge time.Duration)
	setClosing(closing bool)
	setReady(ready bool)
	isClosing() bool
	isReady() bool
}

type serverState struct {
	mu      sync.Mutex
	clients map[string]*clientSession // name -> session
	pending map[string]*pendingInfo   // requestID -> outside public connection + buffered bytes
	closing bool
	ready   bool
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
		// On shutdown close all.
		for id, p := range s.pending {
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
	obs.PendingTunnels.Set(float64(len(s.pending)))
	s.mu.Unlock()
	for _, p := range expired {
		// If tunnel never became ready send timeout to client side user.
		_, _ = p.conn.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\nContent-Type: text/plain\r\nContent-Length: 15\r\n\r\nGateway Timeout"))
		_ = p.conn.Close()
		obs.TunnelTimeoutTotal.Inc()
	}
}

// redisStateStore implements StateStore using Redis for horizontal scaling
type redisStateStore struct {
	client   *redis.Client
	mu       sync.Mutex
	pending  map[string]*pendingInfo // local pending connections (cannot be shared)
	closing  bool
	ready    bool
	instanceID string // unique identifier for this server instance
}

func newRedisStateStore(addr, password string, db int) (*redisStateStore, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connection failed: %w", err)
	}

	instanceID := fmt.Sprintf("showoff-%d", time.Now().UnixNano())
	
	return &redisStateStore{
		client:     rdb,
		pending:    make(map[string]*pendingInfo),
		instanceID: instanceID,
	}, nil
}

// Ensure redisStateStore implements StateStore interface
var _ StateStore = (*redisStateStore)(nil)

func (r *redisStateStore) setClosing(closing bool) {
	r.mu.Lock()
	r.closing = closing
	r.mu.Unlock()
}

func (r *redisStateStore) setReady(ready bool) {
	r.mu.Lock()
	r.ready = ready
	r.mu.Unlock()
}

func (r *redisStateStore) isClosing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closing
}

func (r *redisStateStore) isReady() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready
}

func (r *redisStateStore) registerClient(name string, sess *clientSession) error {
	ctx := context.Background()
	
	// Check if name already exists
	exists, err := r.client.Exists(ctx, "client:"+name).Result()
	if err != nil {
		return fmt.Errorf("redis check failed: %w", err)
	}
	if exists > 0 {
		return fmt.Errorf("name already registered: %s", name)
	}

	// Store client session data (without controlConn)
	sessionData := clientSessionData{
		Name:     sess.name,
		LastSeen: sess.lastSeen,
	}
	
	data, err := json.Marshal(sessionData)
	if err != nil {
		return fmt.Errorf("marshal client session: %w", err)
	}

	// Store with TTL to prevent stale entries
	if err := r.client.Set(ctx, "client:"+name, data, 24*time.Hour).Err(); err != nil {
		return fmt.Errorf("redis set failed: %w", err)
	}

	// Store instance mapping for this client
	if err := r.client.Set(ctx, "instance:"+name, r.instanceID, 24*time.Hour).Err(); err != nil {
		return fmt.Errorf("redis instance mapping failed: %w", err)
	}

	obs.ActiveClients.Inc()
	return nil
}

func (r *redisStateStore) getClient(name string) *clientSession {
	ctx := context.Background()
	
	data, err := r.client.Get(ctx, "client:"+name).Result()
	if err != nil {
		if err != redis.Nil {
			obs.Error("redis.get_client", obs.Fields{"err": err.Error(), "name": name})
		}
		return nil
	}

	var sessionData clientSessionData
	if err := json.Unmarshal([]byte(data), &sessionData); err != nil {
		obs.Error("redis.unmarshal_client", obs.Fields{"err": err.Error(), "name": name})
		return nil
	}

	// Return a client session without controlConn (it will be nil)
	// The calling code needs to handle the case where controlConn is nil
	return &clientSession{
		name:        sessionData.Name,
		controlConn: nil, // Cannot be serialized across instances
		lastSeen:    sessionData.LastSeen,
	}
}

func (r *redisStateStore) removeClient(name string) int {
	ctx := context.Background()
	
	// Remove client data and instance mapping
	pipe := r.client.Pipeline()
	pipe.Del(ctx, "client:"+name)
	pipe.Del(ctx, "instance:"+name)
	_, err := pipe.Exec(ctx)
	if err != nil {
		obs.Error("redis.remove_client", obs.Fields{"err": err.Error(), "name": name})
	}

	// Clean up local pending connections for this client
	r.mu.Lock()
	defer r.mu.Unlock()
	closed := 0
	for id, p := range r.pending {
		if p.clientName == name {
			_ = p.conn.Close()
			delete(r.pending, id)
			closed++
		}
	}

	obs.ActiveClients.Dec()
	obs.PendingTunnels.Set(float64(len(r.pending)))
	return closed
}

func (r *redisStateStore) setPending(id string, p *pendingInfo) {
	// Pending connections cannot be shared across instances due to network connections
	// They remain local to each server instance
	r.mu.Lock()
	r.pending[id] = p
	r.mu.Unlock()
	obs.PendingTunnels.Set(float64(len(r.pending)))
}

func (r *redisStateStore) popPending(id string) *pendingInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pending[id]
	delete(r.pending, id)
	obs.PendingTunnels.Set(float64(len(r.pending)))
	return p
}

func (r *redisStateStore) cleanupExpiredPending(maxAge time.Duration) {
	var expired []*pendingInfo
	r.mu.Lock()
	if r.closing {
		// On shutdown close all local pending
		for id, p := range r.pending {
			expired = append(expired, p)
			delete(r.pending, id)
		}
	} else {
		cutoff := time.Now().Add(-maxAge)
		for id, p := range r.pending {
			if p.created.Before(cutoff) {
				expired = append(expired, p)
				delete(r.pending, id)
			}
		}
	}
	obs.PendingTunnels.Set(float64(len(r.pending)))
	r.mu.Unlock()
	
	for _, p := range expired {
		// If tunnel never became ready send timeout to client side user.
		_, _ = p.conn.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\nContent-Type: text/plain\r\nContent-Length: 15\r\n\r\nGateway Timeout"))
		_ = p.conn.Close()
		obs.TunnelTimeoutTotal.Inc()
	}
}

// newStateStore creates either an in-memory or Redis-backed state store based on configuration
func newStateStore(redisAddr, redisPassword string, redisDB int) (StateStore, error) {
	if redisAddr == "" {
		obs.Info("state.backend", obs.Fields{"type": "in-memory"})
		return newServerState(), nil
	}

	obs.Info("state.backend", obs.Fields{"type": "redis", "addr": redisAddr})
	return newRedisStateStore(redisAddr, redisPassword, redisDB)
}

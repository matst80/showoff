package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/matst80/showoff/internal/obs"
	"github.com/redis/go-redis/v9"
)

// clientSessionData is the JSON form stored in Redis (sans controlConn)
type clientSessionData struct {
	Name     string    `json:"name"`
	LastSeen time.Time `json:"last_seen"`
}

// redisStateStore implements StateStore using Redis for horizontal scaling.
// It also keeps a small in-memory positive cache for quick lookups of recently used clients.
type redisStateStore struct {
	client     *redis.Client
	mu         sync.Mutex
	pending    map[string]*pendingInfo // local pending connections (not shared)
	closing    bool
	ready      bool
	instanceID string

	// local client session cache (name -> last seen timestamp + expiry)
	cache      map[string]*clientSession
	cacheTTL   time.Duration
	cachePurge time.Time

	// maintenance configuration
	heartbeatInterval time.Duration
	redisKeyTTL       time.Duration
}

func newRedisStateStore(addr, password string, db int) (*redisStateStore, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connection failed: %w", err)
	}
	return &redisStateStore{
		client:            rdb,
		pending:           make(map[string]*pendingInfo),
		instanceID:        fmt.Sprintf("showoff-%d", time.Now().UnixNano()),
		cache:             make(map[string]*clientSession),
		cacheTTL:          15 * time.Second,
		heartbeatInterval: 30 * time.Second,
		redisKeyTTL:       24 * time.Hour,
	}, nil
}

var _ StateStore = (*redisStateStore)(nil)

func (r *redisStateStore) setClosing(closing bool) { r.mu.Lock(); r.closing = closing; r.mu.Unlock() }
func (r *redisStateStore) setReady(ready bool)     { r.mu.Lock(); r.ready = ready; r.mu.Unlock() }
func (r *redisStateStore) isClosing() bool         { r.mu.Lock(); defer r.mu.Unlock(); return r.closing }
func (r *redisStateStore) isReady() bool           { r.mu.Lock(); defer r.mu.Unlock(); return r.ready }

func (r *redisStateStore) registerClient(name string, sess *clientSession) error {
	ctx := context.Background()
	exists, err := r.client.Exists(ctx, "client:"+name).Result()
	if err != nil {
		return fmt.Errorf("redis check failed: %w", err)
	}
	if exists > 0 {
		return fmt.Errorf("name already registered: %s", name)
	}
	data, err := json.Marshal(clientSessionData{Name: sess.name, LastSeen: sess.lastSeen})
	if err != nil {
		return fmt.Errorf("marshal client session: %w", err)
	}
	if err := r.client.Set(ctx, "client:"+name, data, 24*time.Hour).Err(); err != nil {
		return fmt.Errorf("redis set failed: %w", err)
	}
	if err := r.client.Set(ctx, "instance:"+name, r.instanceID, 24*time.Hour).Err(); err != nil {
		return fmt.Errorf("redis instance map failed: %w", err)
	}
	// populate local cache as present (controlConn only valid locally)
	r.mu.Lock()
	r.cache[name] = &clientSession{name: name, controlConn: sess.controlConn, lastSeen: sess.lastSeen}
	r.mu.Unlock()
	obs.ActiveClients.Inc()
	return nil
}

func (r *redisStateStore) getClient(name string) *clientSession {
	// Try cache first
	r.mu.Lock()
	cs, ok := r.cache[name]
	if ok && time.Since(cs.lastSeen) < r.cacheTTL { // still warm
		copy := *cs
		r.mu.Unlock()
		return &copy
	}
	r.mu.Unlock()
	ctx := context.Background()
	val, err := r.client.Get(ctx, "client:"+name).Result()
	if err != nil {
		if err != redis.Nil {
			obs.Error("redis.get_client", obs.Fields{"err": err.Error(), "name": name})
		}
		return nil
	}
	var data clientSessionData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		obs.Error("redis.unmarshal_client", obs.Fields{"err": err.Error(), "name": name})
		return nil
	}
	// Update cache (without controlConn since remote)
	r.mu.Lock()
	cached := &clientSession{name: data.Name, lastSeen: data.LastSeen}
	r.cache[name] = cached
	r.mu.Unlock()
	return &clientSession{name: data.Name, lastSeen: data.LastSeen}
}

func (r *redisStateStore) removeClient(name string) int {
	ctx := context.Background()
	pipe := r.client.Pipeline()
	pipe.Del(ctx, "client:"+name)
	pipe.Del(ctx, "instance:"+name)
	if _, err := pipe.Exec(ctx); err != nil {
		obs.Error("redis.remove_client", obs.Fields{"err": err.Error(), "name": name})
	}
	r.mu.Lock()
	delete(r.cache, name)
	closed := 0
	for id, p := range r.pending {
		if p.clientName == name {
			_ = p.conn.Close()
			delete(r.pending, id)
			closed++
		}
	}
	obs.PendingTunnels.Set(float64(len(r.pending)))
	r.mu.Unlock()
	obs.ActiveClients.Dec()
	return closed
}

func (r *redisStateStore) setPending(id string, p *pendingInfo) {
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
	if r.closing { // expire all
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
		_, _ = p.conn.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\nContent-Type: text/plain\r\nContent-Length: 15\r\n\r\nGateway Timeout"))
		_ = p.conn.Close()
		obs.TunnelTimeoutTotal.Inc()
	}
}

// stats helpers (Redis backend only tracks tunnels/timeouts via global metrics; local counters are not persisted)
func (r *redisStateStore) getStats() (int, int, int64, int64) {
	// We can't cheaply count all clients across Redis without SCAN.
	// For dashboard, return cached clients length as an approximation.
	r.mu.Lock()
	clients := len(r.cache)
	pending := len(r.pending)
	r.mu.Unlock()
	return clients, pending, 0, 0
}
func (r *redisStateStore) incrementTunnelCount() { /* no-op for redis (metric already tracked globally) */
}

// startMaintenance launches periodic heartbeat + cache cleanup.
func (r *redisStateStore) startMaintenance(ctx context.Context) {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.heartbeat()
			r.cleanupCache()
		}
	}
}

// heartbeat refreshes lastSeen for locally owned client sessions (those with a controlConn) and extends key TTLs.
func (r *redisStateStore) heartbeat() {
	now := time.Now()
	r.mu.Lock()
	names := make([]string, 0, len(r.cache))
	for name, sess := range r.cache {
		if sess.controlConn != nil { // local authoritative
			sess.lastSeen = now
			names = append(names, name)
		}
	}
	r.mu.Unlock()
	if len(names) == 0 {
		return
	}
	ctx := context.Background()
	for _, name := range names {
		data, err := json.Marshal(clientSessionData{Name: name, LastSeen: now})
		if err != nil {
			obs.Error("redis.heartbeat.marshal", obs.Fields{"err": err.Error(), "name": name})
			continue
		}
		if err := r.client.Set(ctx, "client:"+name, data, r.redisKeyTTL).Err(); err != nil {
			obs.Error("redis.heartbeat.set", obs.Fields{"err": err.Error(), "name": name})
		}
		if err := r.client.Expire(ctx, "instance:"+name, r.redisKeyTTL).Err(); err != nil {
			obs.Error("redis.heartbeat.expire_instance", obs.Fields{"err": err.Error(), "name": name})
		}
	}
}

// cleanupCache removes local cache entries whose Redis key TTL indicates expiration.
func (r *redisStateStore) cleanupCache() {
	r.mu.Lock()
	names := make([]string, 0, len(r.cache))
	for name := range r.cache {
		names = append(names, name)
	}
	r.mu.Unlock()
	if len(names) == 0 {
		return
	}
	ctx := context.Background()
	for _, name := range names {
		ttl, err := r.client.TTL(ctx, "client:"+name).Result()
		if err != nil {
			if err != redis.Nil {
				obs.Error("redis.cache.ttl", obs.Fields{"err": err.Error(), "name": name})
			}
			continue
		}
		if ttl <= 0 { // expired
			r.mu.Lock()
			delete(r.cache, name)
			r.mu.Unlock()
		}
	}
}

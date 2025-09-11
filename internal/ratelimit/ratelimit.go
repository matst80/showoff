package ratelimit

import (
	"sync"
	"time"
)

// TokenBucket implements a token bucket rate limiter
type TokenBucket struct {
	mu       sync.Mutex
	tokens   int
	capacity int
	rate     int           // tokens per second
	lastRefill time.Time
}

// NewTokenBucket creates a new token bucket with the given rate and capacity
func NewTokenBucket(rate, capacity int) *TokenBucket {
	return &TokenBucket{
		tokens:     capacity,
		capacity:   capacity,
		rate:       rate,
		lastRefill: time.Now(),
	}
}

// Allow checks if a request can be allowed and consumes a token if available
func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill)
	
	// Add tokens based on elapsed time
	tokensToAdd := int(elapsed.Seconds() * float64(tb.rate))
	if tokensToAdd > 0 {
		tb.tokens += tokensToAdd
		if tb.tokens > tb.capacity {
			tb.tokens = tb.capacity
		}
		tb.lastRefill = now
	}
	
	// Check if we have tokens available
	if tb.tokens > 0 {
		tb.tokens--
		return true
	}
	
	return false
}

// RateLimiter manages both global and per-client rate limiting
type RateLimiter struct {
	mu                    sync.RWMutex
	globalConnLimiter     *TokenBucket
	globalReqLimiter      *TokenBucket
	perClientConnLimiters map[string]*TokenBucket
	perClientReqLimiters  map[string]*TokenBucket
	connRate              int
	reqRate               int
	burstSize             int
}

// NewRateLimiter creates a new rate limiter with the given configuration
func NewRateLimiter(globalConnLimit, perClientConnLimit, globalReqLimit, perClientReqLimit, burstSize int) *RateLimiter {
	rl := &RateLimiter{
		perClientConnLimiters: make(map[string]*TokenBucket),
		perClientReqLimiters:  make(map[string]*TokenBucket),
		connRate:              perClientConnLimit,
		reqRate:               perClientReqLimit,
		burstSize:             burstSize,
	}
	
	if globalConnLimit > 0 {
		rl.globalConnLimiter = NewTokenBucket(globalConnLimit, burstSize)
	}
	
	if globalReqLimit > 0 {
		rl.globalReqLimiter = NewTokenBucket(globalReqLimit, burstSize)
	}
	
	return rl
}

// AllowConnection checks if a connection is allowed for the given client
func (rl *RateLimiter) AllowConnection(clientName string) bool {
	// Check global connection limit first
	if rl.globalConnLimiter != nil && !rl.globalConnLimiter.Allow() {
		return false
	}
	
	// Check per-client connection limit
	if rl.connRate > 0 {
		rl.mu.Lock()
		bucket, exists := rl.perClientConnLimiters[clientName]
		if !exists {
			bucket = NewTokenBucket(rl.connRate, rl.burstSize)
			rl.perClientConnLimiters[clientName] = bucket
		}
		rl.mu.Unlock()
		
		if !bucket.Allow() {
			return false
		}
	}
	
	return true
}

// AllowRequest checks if a request is allowed for the given client
func (rl *RateLimiter) AllowRequest(clientName string) bool {
	// Check global request limit first
	if rl.globalReqLimiter != nil && !rl.globalReqLimiter.Allow() {
		return false
	}
	
	// Check per-client request limit
	if rl.reqRate > 0 {
		rl.mu.Lock()
		bucket, exists := rl.perClientReqLimiters[clientName]
		if !exists {
			bucket = NewTokenBucket(rl.reqRate, rl.burstSize)
			rl.perClientReqLimiters[clientName] = bucket
		}
		rl.mu.Unlock()
		
		if !bucket.Allow() {
			return false
		}
	}
	
	return true
}

// CleanupExpiredClients removes rate limiters for clients that no longer exist
func (rl *RateLimiter) CleanupExpiredClients(activeClients map[string]bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	
	// Clean up connection limiters
	for clientName := range rl.perClientConnLimiters {
		if !activeClients[clientName] {
			delete(rl.perClientConnLimiters, clientName)
		}
	}
	
	// Clean up request limiters
	for clientName := range rl.perClientReqLimiters {
		if !activeClients[clientName] {
			delete(rl.perClientReqLimiters, clientName)
		}
	}
}
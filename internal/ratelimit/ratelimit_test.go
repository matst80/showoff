package ratelimit

import (
	"testing"
	"time"
)

func TestTokenBucket(t *testing.T) {
	// Test basic token bucket functionality
	bucket := NewTokenBucket(2, 5) // 2 tokens per second, capacity of 5
	
	// Initial tokens should be at capacity
	for i := 0; i < 5; i++ {
		if !bucket.Allow() {
			t.Errorf("Expected initial request %d to be allowed", i)
		}
	}
	
	// Next request should be denied (bucket empty)
	if bucket.Allow() {
		t.Error("Expected request to be denied when bucket is empty")
	}
	
	// Wait and check if tokens are refilled
	time.Sleep(1100 * time.Millisecond) // Wait slightly more than 1 second
	
	// Should have 2 tokens available now
	if !bucket.Allow() {
		t.Error("Expected request to be allowed after token refill")
	}
	if !bucket.Allow() {
		t.Error("Expected second request to be allowed after token refill")
	}
	
	// Third request should be denied
	if bucket.Allow() {
		t.Error("Expected third request to be denied")
	}
}

func TestRateLimiter(t *testing.T) {
	// Test rate limiter with only per-client limits for simplicity
	rl := NewRateLimiter(0, 2, 0, 5, 3) // global limits disabled; per-client: 2 conn/s, 5 req/s; burst: 3
	
	// Test per-client connection limits
	client := "test-client"
	
	// Should allow initial burst
	for i := 0; i < 3; i++ {
		if !rl.AllowConnection(client) {
			t.Errorf("Expected connection %d to be allowed for client %s", i, client)
		}
	}
	
	// Next connection should be denied (per-client limit)
	if rl.AllowConnection(client) {
		t.Error("Expected connection to be denied due to per-client limit")
	}
	
	// Test per-client request limits
	for i := 0; i < 3; i++ {
		if !rl.AllowRequest(client) {
			t.Errorf("Expected request %d to be allowed for client %s", i, client)
		}
	}
	
	// Next request should be denied (per-client limit)
	if rl.AllowRequest(client) {
		t.Error("Expected request to be denied due to per-client limit")
	}
	
	// Test different client should have separate limits
	client2 := "test-client-2"
	if !rl.AllowConnection(client2) {
		t.Error("Expected connection to be allowed for different client")
	}
	if !rl.AllowRequest(client2) {
		t.Error("Expected request to be allowed for different client")
	}
}

func TestRateLimiterWithGlobalLimits(t *testing.T) {
	// Test rate limiter with global limits
	rl := NewRateLimiter(2, 0, 2, 0, 2) // global: 2 conn/s, 2 req/s; per-client limits disabled; burst: 2
	
	client1 := "test-client-1"
	client2 := "test-client-2"
	
	// Should allow initial burst from global limit
	if !rl.AllowConnection(client1) {
		t.Error("Expected first global connection to be allowed")
	}
	if !rl.AllowConnection(client2) {
		t.Error("Expected second global connection to be allowed")
	}
	
	// Next connection should be denied (global limit)
	if rl.AllowConnection(client1) {
		t.Error("Expected connection to be denied due to global limit")
	}
	
	// Same for requests
	if !rl.AllowRequest(client1) {
		t.Error("Expected first global request to be allowed")
	}
	if !rl.AllowRequest(client2) {
		t.Error("Expected second global request to be allowed")
	}
	
	// Next request should be denied (global limit)
	if rl.AllowRequest(client1) {
		t.Error("Expected request to be denied due to global limit")
	}
}

func TestRateLimiterCleanup(t *testing.T) {
	rl := NewRateLimiter(0, 1, 0, 1, 1) // Only per-client limits enabled
	
	// Create limiters for two clients
	client1 := "client1"
	client2 := "client2"
	
	rl.AllowConnection(client1)
	rl.AllowConnection(client2)
	rl.AllowRequest(client1)
	rl.AllowRequest(client2)
	
	// Check that both clients have limiters
	if len(rl.perClientConnLimiters) != 2 {
		t.Errorf("Expected 2 connection limiters, got %d", len(rl.perClientConnLimiters))
	}
	if len(rl.perClientReqLimiters) != 2 {
		t.Errorf("Expected 2 request limiters, got %d", len(rl.perClientReqLimiters))
	}
	
	// Cleanup with only client1 active
	activeClients := map[string]bool{client1: true}
	rl.CleanupExpiredClients(activeClients)
	
	// Check that only client1 limiters remain
	if len(rl.perClientConnLimiters) != 1 {
		t.Errorf("Expected 1 connection limiter after cleanup, got %d", len(rl.perClientConnLimiters))
	}
	if len(rl.perClientReqLimiters) != 1 {
		t.Errorf("Expected 1 request limiter after cleanup, got %d", len(rl.perClientReqLimiters))
	}
	
	if _, exists := rl.perClientConnLimiters[client1]; !exists {
		t.Error("Expected client1 connection limiter to remain")
	}
	if _, exists := rl.perClientReqLimiters[client1]; !exists {
		t.Error("Expected client1 request limiter to remain")
	}
	
	if _, exists := rl.perClientConnLimiters[client2]; exists {
		t.Error("Expected client2 connection limiter to be cleaned up")
	}
	if _, exists := rl.perClientReqLimiters[client2]; exists {
		t.Error("Expected client2 request limiter to be cleaned up")
	}
}

func TestRateLimiterDisabled(t *testing.T) {
	// Test with all limits disabled (0 = disabled)
	rl := NewRateLimiter(0, 0, 0, 0, 5)
	
	client := "test-client"
	
	// Should allow unlimited connections and requests
	for i := 0; i < 100; i++ {
		if !rl.AllowConnection(client) {
			t.Errorf("Expected connection %d to be allowed when limits disabled", i)
		}
		if !rl.AllowRequest(client) {
			t.Errorf("Expected request %d to be allowed when limits disabled", i)
		}
	}
}
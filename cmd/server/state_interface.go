package main

import "time"

// StateStore abstracts server state management to allow horizontal scaling.
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
	// stats helpers (not exported outside package main)
	getStats() (clients int, pending int, totalTunnels int64, timeouts int64)
	incrementTunnelCount()
}

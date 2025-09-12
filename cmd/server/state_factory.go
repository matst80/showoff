package main

import "github.com/matst80/showoff/internal/obs"

// newStateStore creates either an in-memory or Redis-backed state store based on configuration
func newStateStore(redisAddr, redisPassword string, redisDB int) (StateStore, error) {
	if redisAddr == "" {
		obs.Info("state.backend", obs.Fields{"type": "in-memory"})
		return newServerState(), nil
	}
	obs.Info("state.backend", obs.Fields{"type": "redis", "addr": redisAddr})
	return newRedisStateStore(redisAddr, redisPassword, redisDB)
}

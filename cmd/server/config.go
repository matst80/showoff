package main

import (
	"flag"
	"time"
)

// Config holds all runtime configuration derived from flags (future: env vars / file).
type Config struct {
	ControlAddr      string
	PublicAddr       string
	DataAddr         string
	Token            string
	RequestTimeout   time.Duration
	MaxHeaderSize    int
	CleanupInterval  time.Duration
	MetricsAddr      string
	Debug            bool
	BaseDomain       string
	EnableProxyProto bool
	AddXFF           bool
	// Redis configuration for horizontal scaling
	RedisAddr     string
	RedisPassword string
	RedisDB       int
}

var cfg Config

// init registers flags into the global flag set. main() simply parses and uses cfg.
func init() {
	// Register flags first, then parse. (Previously Parse was called before definitions, so flags were ignored.)
	flag.StringVar(&cfg.ControlAddr, "control", ":9000", "address for client control connections")
	flag.StringVar(&cfg.PublicAddr, "public", ":8080", "public listener address")
	flag.StringVar(&cfg.DataAddr, "data", ":9001", "data connection listener address")
	flag.StringVar(&cfg.Token, "token", "", "shared secret token; if set clients must provide matching token")
	flag.DurationVar(&cfg.RequestTimeout, "request-timeout", 10*time.Second, "time limit for client to establish data tunnel")
	flag.IntVar(&cfg.MaxHeaderSize, "max-header-size", 32*1024, "maximum allowed initial HTTP header bytes")
	flag.DurationVar(&cfg.CleanupInterval, "pending-cleanup-interval", 5*time.Second, "interval for sweeping expired pending requests")
	flag.StringVar(&cfg.MetricsAddr, "metrics", ":9100", "metrics and health listen address")
	flag.BoolVar(&cfg.Debug, "debug", false, "enable debug logs")
	flag.StringVar(&cfg.BaseDomain, "domain", "", "base wildcard domain (e.g. example.com) to extract subdomain names")
	flag.BoolVar(&cfg.EnableProxyProto, "proxy-protocol", false, "expect and parse HAProxy PROXY protocol v1 line on public connections")
	flag.BoolVar(&cfg.AddXFF, "add-xff", true, "append X-Forwarded-For header with original client IP (from PROXY or remote addr)")
	// Redis flags for horizontal scaling
	flag.StringVar(&cfg.RedisAddr, "redis-addr", "", "Redis address (host:port) for shared state; if empty, uses in-memory state")
	flag.StringVar(&cfg.RedisPassword, "redis-password", "", "Redis password for authentication")
	flag.IntVar(&cfg.RedisDB, "redis-db", 0, "Redis database number")
	flag.Parse()
}

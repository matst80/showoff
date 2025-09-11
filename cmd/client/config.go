package main

import (
	"flag"
	"net"
	"time"
)

// Config holds client runtime configuration.
type Config struct {
	ServerAddr  string
	DataAddr    string
	Host        string // convenience host to derive server/data if those not explicitly set
	Name        string
	Token       string
	Target      string
	StripHost   bool
	HostRewrite string
	GracePeriod time.Duration
}

var cfg Config

// init registers all client flags into the default flag set.
func init() {
	// Register all flags first; previously Parse was called before definitions so user-supplied flags were ignored.
	flag.StringVar(&cfg.ServerAddr, "server", "127.0.0.1:9000", "server control address")
	flag.StringVar(&cfg.DataAddr, "data", "127.0.0.1:9001", "server data address")
	flag.StringVar(&cfg.Host, "host", "show.knatofs.se", "base host; if set and --server/--data not explicitly provided, they default to host:9000 & host:9001")
	flag.StringVar(&cfg.Name, "name", "demo", "public name to register")
	flag.StringVar(&cfg.Token, "token", "", "shared secret token")
	flag.StringVar(&cfg.Target, "target", "127.0.0.1:3000", "local address to expose")
	flag.BoolVar(&cfg.StripHost, "strip-host", false, "remove Host header before sending to local target (HTTP/1.1 may break)")
	flag.StringVar(&cfg.HostRewrite, "host-rewrite", "", "rewrite Host header to this value (overrides original)")
	flag.DurationVar(&cfg.GracePeriod, "grace-period", 0, "time to wait for active tunnels to drain after shutdown signal (0 = immediate)")
	flag.Parse()
	var serverSet, dataSet bool
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "server" {
			serverSet = true
		}
		if f.Name == "data" {
			dataSet = true
		}
	})
	if cfg.Host != "" {
		if !serverSet {
			cfg.ServerAddr = net.JoinHostPort(cfg.Host, "9000")
		}
		if !dataSet {
			cfg.DataAddr = net.JoinHostPort(cfg.Host, "9001")
		}
	}
}

package obs

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	ActiveClients          = promauto.NewGauge(prometheus.GaugeOpts{Name: "showoff_active_clients", Help: "Current registered clients"})
	PendingTunnels         = promauto.NewGauge(prometheus.GaugeOpts{Name: "showoff_pending_tunnels", Help: "Pending (not yet connected) tunnels"})
	TunnelEstablishedTotal = promauto.NewCounter(prometheus.CounterOpts{Name: "showoff_tunnel_established_total", Help: "Tunnels established"})
	TunnelTimeoutTotal     = promauto.NewCounter(prometheus.CounterOpts{Name: "showoff_tunnel_timeout_total", Help: "Tunnels timed out before client"})
	ErrorsTotal            = promauto.NewCounterVec(prometheus.CounterOpts{Name: "showoff_errors_total", Help: "Errors by type"}, []string{"type"})
	TunnelDurationSeconds  = promauto.NewHistogram(prometheus.HistogramOpts{Name: "showoff_tunnel_duration_seconds", Help: "Tunnel lifetime seconds", Buckets: prometheus.ExponentialBuckets(0.01, 2, 16)})
)

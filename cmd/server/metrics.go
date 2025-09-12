package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/matst80/showoff/internal/obs"
	"github.com/matst80/showoff/internal/web"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// startMetricsServer serves Prometheus metrics plus lightweight dashboard & state endpoints.
func startMetricsServer(addr string, state StateStore) {
	mux := http.NewServeMux()
	mux.Handle("/show-off/metrics", promhttp.Handler())
	mux.HandleFunc("/show-off/api/state", func(w http.ResponseWriter, r *http.Request) {
		st := collectStats(state)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("/show-off/dashboard", func(w http.ResponseWriter, r *http.Request) {
		st := collectStats(state)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := web.Render(w, "dashboard", st.ToTemplateMap()); err != nil {
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte("dashboard template missing"))
			return
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if state.isClosing() || !state.isReady() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	if err := http.ListenAndServe(addr, mux); err != nil && !errors.Is(err, http.ErrServerClosed) {
		obs.Error("metrics.server", obs.Fields{"err": err.Error(), "addr": addr})
	}
}

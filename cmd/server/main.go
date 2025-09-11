package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/matst80/showoff/internal/httpx"
	"github.com/matst80/showoff/internal/obs"
	"github.com/matst80/showoff/internal/proto"
	hostparse "github.com/matst80/showoff/internal/server"
	"github.com/matst80/showoff/internal/web"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Template embedding moved to internal/web package.

func main() {

	if cfg.Debug {
		obs.EnableDebug(true)
	}
	obs.Info("server.start", obs.Fields{"control": cfg.ControlAddr, "public": cfg.PublicAddr, "data": cfg.DataAddr, "metrics": cfg.MetricsAddr})
	state := newServerState()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start control listener
	ctrlLn, err := net.Listen("tcp", cfg.ControlAddr)
	if err != nil {
		obs.Error("listen.control", obs.Fields{"err": err.Error(), "addr": cfg.ControlAddr})
		os.Exit(1)
	}
	defer ctrlLn.Close()

	// Start data listener
	dataLn, err := net.Listen("tcp", cfg.DataAddr)
	if err != nil {
		obs.Error("listen.data", obs.Fields{"err": err.Error(), "addr": cfg.DataAddr})
		os.Exit(1)
	}
	defer dataLn.Close()

	// Start public listener
	pubLn, err := net.Listen("tcp", cfg.PublicAddr)
	if err != nil {
		obs.Error("listen.public", obs.Fields{"err": err.Error(), "addr": cfg.PublicAddr})
		os.Exit(1)
	}
	defer pubLn.Close()

	// Start metrics / health server (readiness will be false until listeners & goroutines started)
	go startMetricsServer(cfg.MetricsAddr, state)

	go runCleanupLoop(ctx, state, cfg.CleanupInterval, cfg.RequestTimeout)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { defer wg.Done(); acceptControl(ctx, ctrlLn, state, &cfg) }()
	wg.Add(1)
	go func() { defer wg.Done(); acceptData(ctx, dataLn, state) }()
	wg.Add(1)
	go func() { defer wg.Done(); acceptPublic(ctx, pubLn, state, &cfg) }()

	state.mu.Lock()
	state.ready = true
	state.mu.Unlock()
	obs.Info("server.ready", obs.Fields{})

	<-ctx.Done()
	obs.Info("server.shutdown.signal", obs.Fields{})
	state.mu.Lock()
	state.closing = true
	state.mu.Unlock()
	_ = ctrlLn.Close()
	_ = dataLn.Close()
	_ = pubLn.Close()
	// Final cleanup sweep
	state.cleanupExpiredPending(cfg.RequestTimeout)
	wg.Wait()
	obs.Info("server.shutdown.complete", obs.Fields{})
}

func acceptControl(ctx context.Context, ln net.Listener, state *serverState, cfg *Config) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		c, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				obs.Error("accept.control.timeout", obs.Fields{"err": err.Error()})
				continue
			}
			return
		}
		go handleControl(c, state, cfg.Token)
	}
}

func handleControl(c net.Conn, state *serverState, token string) {
	defer c.Close()
	rd := bufio.NewReader(c)
	line, err := rd.ReadString('\n')
	if err != nil {
		obs.Error("control.auth.read", obs.Fields{"err": err.Error()})
		return
	}
	line = strings.TrimSpace(line)
	var auth proto.Auth
	if err := json.Unmarshal([]byte(line), &auth); err != nil {
		obs.Error("control.auth.json", obs.Fields{"err": err.Error()})
		obs.ErrorsTotal.WithLabelValues("auth_json").Inc()
		return
	}
	if token != "" && auth.Token != token {
		obs.Error("control.auth.token", obs.Fields{"remote": c.RemoteAddr().String()})
		obs.ErrorsTotal.WithLabelValues("auth_token").Inc()
		_ = writeJSONLine(c, map[string]string{"error": "unauthorized"})
		return
	}
	if auth.Name == "" {
		obs.ErrorsTotal.WithLabelValues("auth_missing_name").Inc()
		_ = writeJSONLine(c, map[string]string{"error": "missing name"})
		return
	}
	if err := state.registerClient(auth.Name, &clientSession{name: auth.Name, controlConn: c, lastSeen: time.Now()}); err != nil {
		obs.ErrorsTotal.WithLabelValues("register_conflict").Inc()
		_ = writeJSONLine(c, map[string]string{"error": err.Error()})
		return
	}
	_ = writeJSONLine(c, proto.AuthOK{Msg: "ok"})
	obs.Info("client.registered", obs.Fields{"name": auth.Name, "remote": c.RemoteAddr().String()})

	// Keep control connection open; read pings or ignore further input.
	for {
		if _, err := rd.ReadString('\n'); err != nil {
			if !errors.Is(err, io.EOF) {
				obs.Error("control.conn.read", obs.Fields{"err": err.Error(), "name": auth.Name})
			}
			closed := state.removeClient(auth.Name)
			if closed > 0 {
				obs.Info("control.conn.cleanup", obs.Fields{"cleaned": closed, "name": auth.Name})
			}
			return
		}
	}
}

func acceptData(ctx context.Context, ln net.Listener, state *serverState) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		c, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				obs.Error("accept.data.timeout", obs.Fields{"err": err.Error()})
				continue
			}
			return
		}
		go handleDataConn(c, state)
	}
}

func handleDataConn(c net.Conn, state *serverState) {
	rd := bufio.NewReader(c)
	line, err := rd.ReadString('\n')
	if err != nil {
		obs.Error("data.read", obs.Fields{"err": err.Error()})
		obs.ErrorsTotal.WithLabelValues("data_read").Inc()
		_ = c.Close()
		return
	}
	line = strings.TrimSpace(line)
	var data proto.Data
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		obs.Error("data.json", obs.Fields{"err": err.Error()})
		obs.ErrorsTotal.WithLabelValues("data_json").Inc()
		_ = c.Close()
		return
	}
	if data.ID == "" {
		_ = c.Close()
		return
	}
	pinfo := state.popPending(data.ID)
	if pinfo == nil {
		obs.Error("data.no_pending", obs.Fields{"id": data.ID})
		obs.ErrorsTotal.WithLabelValues("no_pending").Inc()
		_ = c.Close()
		return
	}
	outside := pinfo.conn
	obs.Info("tunnel.established", obs.Fields{"id": data.ID, "initial_bytes": len(pinfo.initialBuf)})
	close(pinfo.readyCh)
	obs.TunnelEstablishedTotal.Inc()
	state.incrementTunnelCount() // Track tunnel count in state
	// Send initial buffered request bytes to client over data connection BEFORE starting copy loops.
	if len(pinfo.initialBuf) > 0 {
		if _, err := c.Write(pinfo.initialBuf); err != nil {
			obs.Error("tunnel.forward_initial", obs.Fields{"id": data.ID, "err": err.Error()})
			obs.ErrorsTotal.WithLabelValues("forward_initial").Inc()
		}
	}
	// Start bidirectional proxy with duration measurement.
	start := time.Now()
	var wg sync.WaitGroup
	var once sync.Once
	closeBoth := func() { _ = outside.Close(); _ = c.Close() }
	copyFn := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		once.Do(closeBoth)
	}
	wg.Add(2)
	go copyFn(outside, c)
	go copyFn(c, outside)
	go func() {
		wg.Wait()
		obs.TunnelDurationSeconds.Observe(time.Since(start).Seconds())
	}()
}

func acceptPublic(ctx context.Context, ln net.Listener, state *serverState, cfg *Config) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		c, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				obs.Error("accept.public.timeout", obs.Fields{"err": err.Error()})
				continue
			}
			return
		}
		go handlePublicConn(c, state, cfg.RequestTimeout, cfg.MaxHeaderSize, cfg.BaseDomain, cfg.EnableProxyProto, cfg.AddXFF)
	}
}

func handlePublicConn(c net.Conn, state *serverState, timeout time.Duration, maxHeader int, baseDomain string, proxyProto bool, addXFF bool) {
	origRemote := c.RemoteAddr().String()
	br := bufio.NewReader(c)
	var pre []byte
	var realRemoteIP string
	if proxyProto {
		line, err := br.ReadString('\n')
		if err != nil {
			obs.Error("public.proxy_proto.read", obs.Fields{"err": err.Error()})
			_ = c.Close()
			return
		}
		if strings.HasPrefix(line, "PROXY ") {
			parts := strings.Fields(line)
			if len(parts) >= 6 {
				realRemoteIP = parts[2]
			}
		} else {
			pre = append(pre, []byte(line)...)
		}
	}
	parsed, _, err := httpx.ParseRequest(br, maxHeader, pre)
	if err != nil {
		obs.Error("public.header", obs.Fields{"err": err.Error()})
		obs.ErrorsTotal.WithLabelValues("public_header").Inc()
		_ = c.Close()
		return
	}
	// Serve dashboard & state JSON directly on public port if requested (namespaced)
	if parsed.Method == "GET" && (parsed.URI == "/show-off/dashboard" || parsed.URI == "/show-off/dashboard/") {
		// minimal stats page reuse template
		clients, pending, total, timeouts := state.getStats()
		var buf bytes.Buffer
		_ = web.Render(&buf, "dashboard", map[string]any{"Clients": clients, "Pending": pending, "Total": total, "Timeouts": timeouts})
		body := buf.Bytes()
		head := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\nCache-Control: no-store\r\n\r\n", len(body))
		_, _ = c.Write(append([]byte(head), body...))
		_ = c.Close()
		return
	}
	if parsed.Method == "GET" && parsed.URI == "/show-off/api/state" {
		clients, pending, total, timeouts := state.getStats()
		payload := map[string]any{"clients": clients, "pending": pending, "total_tunnels": total, "timeouts": timeouts, "now": time.Now().UTC().Format(time.RFC3339)}
		b, _ := json.Marshal(payload)
		head := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nCache-Control: no-store\r\n\r\n", len(b))
		_, _ = c.Write(append([]byte(head), b...))
		_ = c.Close()
		return
	}
	hostHeader := parsed.Get("Host")
	var name string
	if hostHeader != "" {
		// Domain extraction
		fakeHostLine := []byte("Host: " + hostHeader + "\r\n\r\n")
		_, name, _, _ = hostparse.ExtractName(fakeHostLine, baseDomain)
	}
	if name == "" { // fallback path based
		// Attempt hostparse on reconstructed raw header bytes (for path prefix logic)
		// Rebuild minimal buffer
		var raw bytes.Buffer
		parsed.WriteTo(&raw)
		_, name, _, _ = hostparse.ExtractName(raw.Bytes(), baseDomain)
	}
	if name == "" {
		obs.Error("public.host", obs.Fields{"host": hostHeader})
		obs.ErrorsTotal.WithLabelValues("public_host").Inc()
		writeErrorTemplate(c, 404, "notfound.html", ErrorPageData{Name: hostHeader})
		return
	}
	sess := state.getClient(name)
	if sess == nil {
		writeErrorTemplate(c, 502, "down.html", ErrorPageData{Name: name})
		return
	}
	if addXFF {
		clientIP := realRemoteIP
		if clientIP == "" {
			clientIP, _, _ = net.SplitHostPort(origRemote)
		}
		parsed.AugmentXFF(clientIP)
	}
	id, _ := cryptoRandomID(20)
	// Serialize modified headers to buffer for initialBuf
	var hdrOut bytes.Buffer
	parsed.WriteTo(&hdrOut)
	initial := hdrOut.Bytes()
	pinfo := &pendingInfo{conn: c, initialBuf: initial, clientName: name, created: time.Now(), readyCh: make(chan struct{})}
	state.setPending(id, pinfo)
	_ = writeJSONLine(sess.controlConn, proto.Request{ID: id, Name: name})

	select {
	case <-pinfo.readyCh:
		return
	case <-time.After(timeout):
		obs.Error("public.timeout", obs.Fields{"id": id})
		obs.TunnelTimeoutTotal.Inc()
		obs.ErrorsTotal.WithLabelValues("timeout").Inc()
		if state.popPending(id) != nil {
			writeErrorTemplate(c, 504, "timeout.html", ErrorPageData{Name: name, ID: id, Timeout: timeout.String(), Wait: timeout.String()})
		}
	}
}

// ErrorPageData structured data for error templates.
type ErrorPageData struct {
	Name    string
	ID      string
	Timeout string
	Wait    string
}

func (e ErrorPageData) toMap() map[string]any {
	m := map[string]any{}
	if e.Name != "" {
		m["Name"] = e.Name
	}
	if e.ID != "" {
		m["ID"] = e.ID
	}
	if e.Timeout != "" {
		m["Timeout"] = e.Timeout
	}
	if e.Wait != "" {
		m["Wait"] = e.Wait
	}
	return m
}

// writeTemplateConn renders an HTML template (with data) to a raw net.Conn; falls back to plain text on error.
func writeTemplateConn(c net.Conn, status int, tmpl string, headers map[string]string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	var buf bytes.Buffer
	if err := web.Render(&buf, tmpl, data); err != nil {
		body := http.StatusText(status)
		msg := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nCache-Control: no-store\r\n\r\n%s", status, http.StatusText(status), len(body), body)
		_, _ = c.Write([]byte(msg))
		_ = c.Close()
		return
	}
	body := buf.Bytes()
	var headBuf bytes.Buffer
	fmt.Fprintf(&headBuf, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))
	fmt.Fprintf(&headBuf, "Content-Type: text/html; charset=utf-8\r\n")
	fmt.Fprintf(&headBuf, "Content-Length: %d\r\n", len(body))
	fmt.Fprintf(&headBuf, "Cache-Control: no-store\r\n")
	for k, v := range headers {
		fmt.Fprintf(&headBuf, "%s: %s\r\n", k, v)
	}
	headBuf.WriteString("\r\n")
	_, _ = c.Write(append(headBuf.Bytes(), body...))
	_ = c.Close()
}

// writeErrorTemplate convenience wrapper using ErrorPageData.
func writeErrorTemplate(c net.Conn, status int, tmpl string, d ErrorPageData) {
	writeTemplateConn(c, status, tmpl, nil, d.toMap())
}

// cryptoRandomID returns a hex string of n bytes (2n chars). For base62 shortened form we could post-process, but hex is fine here.
func cryptoRandomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// readInitialHeader reads from conn until end-of-header marker or size limit.
// (legacy header read helpers removed; replaced by httpx.ParseRequest)

func runCleanupLoop(ctx context.Context, state *serverState, interval, maxAge time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			state.cleanupExpiredPending(maxAge)
			return
		case <-t.C:
			state.cleanupExpiredPending(maxAge)
		}
	}
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// startMetricsServer serves Prometheus metrics and health endpoints.
func startMetricsServer(addr string, state *serverState) {
	mux := http.NewServeMux()
	mux.Handle("/show-off/metrics", promhttp.Handler())
	mux.HandleFunc("/show-off/api/state", func(w http.ResponseWriter, r *http.Request) {
		clients, pending, total, timeouts := state.getStats()
		resp := map[string]any{
			"clients":       clients,
			"pending":       pending,
			"total_tunnels": total,
			"timeouts":      timeouts,
			"now":           time.Now().UTC().Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/show-off/dashboard", func(w http.ResponseWriter, r *http.Request) {
		clients, pending, total, timeouts := state.getStats()
		data := map[string]any{
			"Clients":  clients,
			"Pending":  pending,
			"Total":    total,
			"Timeouts": timeouts,
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := web.Render(w, "dashboard", data); err != nil {
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
		state.mu.Lock()
		closing := state.closing
		ready := state.ready
		state.mu.Unlock()
		if closing || !ready {
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

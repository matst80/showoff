package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
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

	"github.com/matst80/showoff/internal/obs"
	"github.com/matst80/showoff/internal/proto"
	hostparse "github.com/matst80/showoff/internal/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type clientSession struct {
	name        string
	controlConn net.Conn
	lastSeen    time.Time
}

// pendingInfo tracks an outside public connection waiting for a client data tunnel.
type pendingInfo struct {
	conn       net.Conn
	initialBuf []byte // initial request bytes already consumed from outside
	clientName string // owning client
	created    time.Time
	readyCh    chan struct{} // closed when data tunnel established
}

type serverState struct {
	mu      sync.Mutex
	clients map[string]*clientSession // name -> session
	pending map[string]*pendingInfo   // requestID -> outside public connection + buffered bytes
	closing bool
	ready   bool
}

func newServerState() *serverState {
	return &serverState{clients: make(map[string]*clientSession), pending: make(map[string]*pendingInfo)}
}

func (s *serverState) registerClient(name string, sess *clientSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.clients[name]; exists {
		return fmt.Errorf("name already registered: %s", name)
	}
	s.clients[name] = sess
	obs.ActiveClients.Set(float64(len(s.clients)))
	return nil
}

func (s *serverState) getClient(name string) *clientSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clients[name]
}

func (s *serverState) setPending(id string, p *pendingInfo) {
	s.mu.Lock()
	s.pending[id] = p
	s.mu.Unlock()
	obs.PendingTunnels.Set(float64(len(s.pending)))
}

func (s *serverState) popPending(id string) *pendingInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[id]
	delete(s.pending, id)
	obs.PendingTunnels.Set(float64(len(s.pending)))
	return p
}

// removeClient removes a client and closes any pending outside connections waiting for it.
func (s *serverState) removeClient(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, name)
	closed := 0
	for id, p := range s.pending {
		if p.clientName == name {
			_ = p.conn.Close()
			delete(s.pending, id)
			closed++
		}
	}
	obs.ActiveClients.Set(float64(len(s.clients)))
	obs.PendingTunnels.Set(float64(len(s.pending)))
	return closed
}

func (s *serverState) cleanupExpiredPending(maxAge time.Duration) {
	var expired []*pendingInfo
	s.mu.Lock()
	if s.closing {
		// On shutdown close all.
		for id, p := range s.pending {
			expired = append(expired, p)
			delete(s.pending, id)
		}
	} else {
		cutoff := time.Now().Add(-maxAge)
		for id, p := range s.pending {
			if p.created.Before(cutoff) {
				expired = append(expired, p)
				delete(s.pending, id)
			}
		}
	}
	obs.PendingTunnels.Set(float64(len(s.pending)))
	s.mu.Unlock()
	for _, p := range expired {
		// If tunnel never became ready send timeout to client side user.
		_, _ = p.conn.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\nContent-Type: text/plain\r\nContent-Length: 15\r\n\r\nGateway Timeout"))
		_ = p.conn.Close()
		obs.TunnelTimeoutTotal.Inc()
	}
}

func main() {
	var controlAddr string
	var publicAddr string
	var dataAddr string
	var token string
	var requestTimeout time.Duration
	var maxHeaderSize int
	var cleanupInterval time.Duration
	var metricsAddr string
	var debug bool
	flag.StringVar(&controlAddr, "control", ":9000", "address for client control connections")
	flag.StringVar(&publicAddr, "public", ":8080", "public listener address")
	flag.StringVar(&dataAddr, "data", ":9001", "data connection listener address")
	flag.StringVar(&token, "token", "", "shared secret token; if set clients must provide matching token")
	flag.DurationVar(&requestTimeout, "request-timeout", 10*time.Second, "time limit for client to establish data tunnel")
	flag.IntVar(&maxHeaderSize, "max-header-size", 32*1024, "maximum allowed initial HTTP header bytes")
	flag.DurationVar(&cleanupInterval, "pending-cleanup-interval", 5*time.Second, "interval for sweeping expired pending requests")
	flag.StringVar(&metricsAddr, "metrics", ":9100", "metrics and health listen address")
	flag.BoolVar(&debug, "debug", false, "enable debug logs")
	flag.Parse()

	if debug {
		obs.EnableDebug(true)
	}
	obs.Info("server.start", obs.Fields{"control": controlAddr, "public": publicAddr, "data": dataAddr, "metrics": metricsAddr})
	state := newServerState()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start control listener
	ctrlLn, err := net.Listen("tcp", controlAddr)
	if err != nil {
		obs.Error("listen.control", obs.Fields{"err": err.Error(), "addr": controlAddr})
		os.Exit(1)
	}
	defer ctrlLn.Close()

	// Start data listener
	dataLn, err := net.Listen("tcp", dataAddr)
	if err != nil {
		obs.Error("listen.data", obs.Fields{"err": err.Error(), "addr": dataAddr})
		os.Exit(1)
	}
	defer dataLn.Close()

	// Start public listener
	pubLn, err := net.Listen("tcp", publicAddr)
	if err != nil {
		obs.Error("listen.public", obs.Fields{"err": err.Error(), "addr": publicAddr})
		os.Exit(1)
	}
	defer pubLn.Close()

	// Start metrics / health server (readiness will be false until listeners & goroutines started)
	go startMetricsServer(metricsAddr, state)

	var wg sync.WaitGroup
	go runCleanupLoop(ctx, state, cleanupInterval, requestTimeout)

	wg.Add(1)
	go func() { defer wg.Done(); acceptControl(ctx, ctrlLn, state, token) }()
	wg.Add(1)
	go func() { defer wg.Done(); acceptData(ctx, dataLn, state) }()
	wg.Add(1)
	go func() { defer wg.Done(); acceptPublic(ctx, pubLn, state, requestTimeout, maxHeaderSize) }()

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
	state.cleanupExpiredPending(requestTimeout)
	wg.Wait()
	obs.Info("server.shutdown.complete", obs.Fields{})
}

func acceptControl(ctx context.Context, ln net.Listener, state *serverState, token string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		c, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				obs.Error("accept.control.temp", obs.Fields{"err": err.Error()})
				continue
			}
			return
		}
		go handleControl(c, state, token)
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
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				obs.Error("accept.data.temp", obs.Fields{"err": err.Error()})
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

func acceptPublic(ctx context.Context, ln net.Listener, state *serverState, requestTimeout time.Duration, maxHeader int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		c, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				obs.Error("accept.public.temp", obs.Fields{"err": err.Error()})
				continue
			}
			return
		}
		go handlePublicConn(c, state, requestTimeout, maxHeader)
	}
}

func handlePublicConn(c net.Conn, state *serverState, timeout time.Duration, maxHeader int) {
	// Robust header read: read until CRLFCRLF or size limit.
	header, err := readInitialHeader(c, maxHeader)
	if err != nil {
		obs.Error("public.header", obs.Fields{"err": err.Error()})
		obs.ErrorsTotal.WithLabelValues("public_header").Inc()
		_ = c.Close()
		return
	}
	host, name, _, err := hostparse.ExtractName(header)
	if err != nil || name == "" {
		obs.Error("public.host", obs.Fields{"err": fmt.Sprint(err), "host": host})
		obs.ErrorsTotal.WithLabelValues("public_host").Inc()
		_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nContent-Length: 11\r\n\r\nBad Gateway"))
		_ = c.Close()
		return
	}
	sess := state.getClient(name)
	if sess == nil {
		_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nContent-Length: 11\r\n\r\nBad Gateway"))
		_ = c.Close()
		return
	}
	// Create request id
	id, _ := cryptoRandomID(20)
	pinfo := &pendingInfo{conn: c, initialBuf: header, clientName: name, created: time.Now(), readyCh: make(chan struct{})}
	state.setPending(id, pinfo)
	// Send request over control
	_ = writeJSONLine(sess.controlConn, proto.Request{ID: id, Name: name})

	select {
	case <-pinfo.readyCh:
		return // data handler took over
	case <-time.After(timeout):
		obs.Error("public.timeout", obs.Fields{"id": id})
		obs.TunnelTimeoutTotal.Inc()
		obs.ErrorsTotal.WithLabelValues("timeout").Inc()
		if state.popPending(id) != nil {
			_, _ = c.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\nContent-Type: text/plain\r\nContent-Length: 15\r\n\r\nGateway Timeout"))
			_ = c.Close()
		}
	}
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
func readInitialHeader(conn net.Conn, max int) ([]byte, error) {
	buf := make([]byte, 0, 1024)
	chunk := make([]byte, 1024)
	deadline := time.Now().Add(15 * time.Second)
	_ = conn.SetReadDeadline(deadline)
	for {
		n, err := conn.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			// Detect end of headers
			if hasHeaderEnd(buf) {
				break
			}
			if len(buf) > max {
				return nil, fmt.Errorf("header too large (%d>%d)", len(buf), max)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	return buf, nil
}

func hasHeaderEnd(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	// Check for \r\n\r\n or \n\n
	if strings.Contains(string(b), "\r\n\r\n") {
		return true
	}
	if strings.Contains(string(b), "\n\n") {
		return true
	}
	return false
}

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

func proxy(dst net.Conn, src net.Conn) { // retained (unused) for potential future reuse
	defer dst.Close()
	defer src.Close()
	_, _ = io.Copy(dst, src)
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// startMetricsServer serves Prometheus metrics and simple health endpoints.
func startMetricsServer(addr string, state *serverState) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
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

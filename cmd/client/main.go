package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/matst80/showoff/internal/httpx"
	"github.com/matst80/showoff/internal/proto"
)

// activeTunnels tracks currently active proxy tunnels (established data connections).
var activeTunnels sync.WaitGroup

// waitForDrain waits for all active tunnels to finish or the timeout, whichever first.
func waitForDrain(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { activeTunnels.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("showoff client starting name=%s target=%s server=%s data=%s", cfg.Name, cfg.Target, cfg.ServerAddr, cfg.DataAddr)
	for {
		if ctx.Err() != nil {
			if cfg.GracePeriod > 0 {
				if waitForDrain(cfg.GracePeriod) {
					log.Printf("all tunnels drained; shutdown complete")
				} else {
					log.Printf("grace period expired; exiting with active tunnels still open")
				}
			} else {
				log.Printf("shutdown complete")
			}
			return
		}
		if err := runOnce(ctx, &cfg); err != nil {
			if ctx.Err() != nil { // shutting down
				log.Printf("exiting: %v", ctx.Err())
				return
			}
			log.Printf("control connection ended: %v", err)
		}
		// Reconnect delay or exit if shutting down.
		select {
		case <-ctx.Done():
			if cfg.GracePeriod > 0 {
				log.Printf("shutdown requested; waiting up to %s for active tunnels (%s) to drain", cfg.GracePeriod, cfg.Name)
				if waitForDrain(cfg.GracePeriod) {
					log.Printf("tunnels drained; exiting")
				} else {
					log.Printf("tunnels not drained before grace timeout; exiting")
				}
			}
			return
		case <-time.After(2 * time.Second):
		}
		log.Printf("reconnecting...")
	}
}

func runOnce(ctx context.Context, cfg *Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	var tlsConfig *tls.Config
	var err error
	if cfg.EnableTLS {
		tlsConfig, err = createClientTLSConfig(cfg)
		if err != nil {
			return fmt.Errorf("TLS config error: %v", err)
		}
	}

	c, err := dialServer(cfg.ServerAddr, tlsConfig)
	if err != nil {
		return err
	}
	defer c.Close()
	// Ensure the control connection is closed promptly on shutdown to unblock reads.
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()
	if err := writeJSONLine(c, proto.Auth{Token: cfg.Token, Name: cfg.Name, Target: cfg.Target}); err != nil {
		return err
	}
	rd := bufio.NewReader(c)
	line, err := rd.ReadString('\n')
	if err != nil {
		return err
	}
	if strings.Contains(line, "error") {
		return fmt.Errorf("auth failed: %s", line)
	}
	log.Printf("registered name=%s", cfg.Name)
	// Listen for requests
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" { // keepalive maybe
			continue
		}
		var req proto.Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			log.Printf("invalid request line: %s", line)
			continue
		}
		go handleRequest(req, cfg)
	}
}

func handleRequest(req proto.Request, cfg *Config) {
	var tlsConfig *tls.Config
	var err error
	if cfg.EnableTLS {
		tlsConfig, err = createClientTLSConfig(cfg)
		if err != nil {
			log.Printf("TLS config error: %v", err)
			return
		}
	}

	// Establish data connection first so server doesn't time out.
	dataConn, err := dialServer(cfg.DataAddr, tlsConfig)
	if err != nil {
		log.Printf("dial data error: %v", err)
		return
	}
	if err := writeJSONLine(dataConn, proto.Data{ID: req.ID}); err != nil {
		log.Printf("write data handshake error: %v", err)
		_ = dataConn.Close()
		return
	}
	// Now connect to local target.
	local, err := net.Dial("tcp", cfg.Target)
	if err != nil {
		// Send a quick HTTP 502 so the user sees a response.
		_, _ = dataConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nContent-Length: 11\r\n\r\nBad Gateway"))
		_ = dataConn.Close()
		log.Printf("dial local error (sent 502): %v", err)
		return
	}
	// Intercept and possibly modify the initial HTTP headers from dataConn before passing to local.
	rd := bufio.NewReader(dataConn)
	modified, bodyRemainder, err := readAndMaybeRewriteHeaders(rd, cfg.StripHost, cfg.HostRewrite)
	if err != nil {
		log.Printf("header rewrite error (forwarding raw): %v", err)
		// If we failed, fall back: write what we read so far then continue raw proxy.
		if len(modified) > 0 {
			_, _ = local.Write(modified)
		}
		go proxy(local, dataConn)
		go proxy(dataConn, local)
		return
	}
	// Send modified headers
	if len(modified) > 0 {
		_, _ = local.Write(modified)
	}
	// Forward any body bytes already read with the headers.
	if bodyRemainder != nil && bodyRemainder.Len() > 0 {
		_, _ = io.Copy(local, bodyRemainder)
	}
	// Continue streaming: requests (remaining from rd) to local; responses back to dataConn.
	activeTunnels.Add(1)
	var wg sync.WaitGroup
	var once sync.Once
	closeBoth := func() { _ = local.Close(); _ = dataConn.Close() }
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(local, rd)
		once.Do(closeBoth)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(dataConn, local)
		once.Do(closeBoth)
	}()
	go func() { // signal tunnel completion precisely when both copy loops finish
		wg.Wait()
		activeTunnels.Done()
	}()
}

// readAndMaybeRewriteHeaders reads HTTP/1.x request headers from rd and applies host stripping / rewriting.
// Returns the rewritten header bytes (including terminating CRLF CRLF), a buffer containing any body bytes
// that were read past the header boundary, or an error.
func readAndMaybeRewriteHeaders(rd *bufio.Reader, stripHost bool, hostRewrite string) ([]byte, *bytes.Buffer, error) {
	// Use shared httpx parser (same max as server for consistency) - 64KiB cap.
	const maxHeaderBytes = 64 * 1024
	parsed, _, err := httpx.ParseRequest(rd, maxHeaderBytes, nil)
	if err != nil {
		return nil, nil, err
	}
	// Apply rewrites.
	if stripHost {
		parsed.StripHost()
	} else if hostRewrite != "" {
		parsed.ReplaceHost(hostRewrite)
	}
	var out bytes.Buffer
	if _, err := parsed.WriteTo(&out); err != nil {
		return nil, nil, err
	}
	// Body start already written by WriteTo; remaining body bytes come from rd.
	return out.Bytes(), nil, nil
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func proxy(dst net.Conn, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	_, _ = io.Copy(dst, src)
}

// createClientTLSConfig creates a TLS configuration for the client
func createClientTLSConfig(cfg *Config) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}

	// Load CA certificate if provided
	if cfg.TLSCAFile != "" {
		caCert, err := os.ReadFile(cfg.TLSCAFile)
		if err != nil {
			return nil, err
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		tlsConfig.RootCAs = caCertPool
	}

	// Load client certificate and key if provided (for mTLS)
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
}

// dialServer creates either a plain TCP or TLS connection based on tlsConfig
func dialServer(addr string, tlsConfig *tls.Config) (net.Conn, error) {
	if tlsConfig == nil {
		return net.Dial("tcp", addr)
	}
	return tls.Dial("tcp", addr, tlsConfig)
}

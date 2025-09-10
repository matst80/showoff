package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/matst80/showoff/internal/proto"
)

func main() {
	var serverAddr string
	var dataAddr string
	var name string
	var token string
	var target string
	var stripHost bool
	var hostRewrite string
	flag.StringVar(&serverAddr, "server", "127.0.0.1:9000", "server control address")
	flag.StringVar(&dataAddr, "data", "127.0.0.1:9001", "server data address")
	flag.StringVar(&name, "name", "demo", "public name to register")
	flag.StringVar(&token, "token", "", "shared secret token")
	flag.StringVar(&target, "target", "127.0.0.1:3000", "local address to expose")
	flag.BoolVar(&stripHost, "strip-host", false, "remove Host header before sending to local target (HTTP/1.1 may break)")
	flag.StringVar(&hostRewrite, "host-rewrite", "", "rewrite Host header to this value (overrides original)")
	flag.Parse()

	log.Printf("showoff client starting name=%s target=%s server=%s", name, target, serverAddr)
	for {
		if err := runOnce(serverAddr, dataAddr, name, token, target, stripHost, hostRewrite); err != nil {
			log.Printf("control connection ended: %v", err)
		}
		time.Sleep(2 * time.Second)
		log.Printf("reconnecting...")
	}
}

func runOnce(serverAddr, dataAddr, name, token, target string, stripHost bool, hostRewrite string) error {
	c, err := net.Dial("tcp", serverAddr)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := writeJSONLine(c, proto.Auth{Token: token, Name: name, Target: target}); err != nil {
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
	log.Printf("registered name=%s", name)
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
		go handleRequest(req, dataAddr, target, stripHost, hostRewrite)
	}
}

func handleRequest(req proto.Request, dataAddr, target string, stripHost bool, hostRewrite string) {
	// Establish data connection first so server doesn't time out.
	dataConn, err := net.Dial("tcp", dataAddr)
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
	local, err := net.Dial("tcp", target)
	if err != nil {
		// Send a quick HTTP 502 so the user sees a response.
		_, _ = dataConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nContent-Length: 11\r\n\r\nBad Gateway"))
		_ = dataConn.Close()
		log.Printf("dial local error (sent 502): %v", err)
		return
	}
	// Intercept and possibly modify the initial HTTP headers from dataConn before passing to local.
	rd := bufio.NewReader(dataConn)
	modified, bodyRemainder, err := readAndMaybeRewriteHeaders(rd, stripHost, hostRewrite)
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
	go func() {
		_, _ = io.Copy(local, rd)
		local.Close()
		dataConn.Close()
	}()
	go func() {
		_, _ = io.Copy(dataConn, local)
		local.Close()
		dataConn.Close()
	}()
}

// readAndMaybeRewriteHeaders reads HTTP/1.x request headers from rd and applies host stripping / rewriting.
// Returns the rewritten header bytes (including terminating CRLF CRLF), a buffer containing any body bytes
// that were read past the header boundary, or an error.
func readAndMaybeRewriteHeaders(rd *bufio.Reader, stripHost bool, hostRewrite string) ([]byte, *bytes.Buffer, error) {
	// Peek progressively until we find the header terminator or exceed limit.
	var buf bytes.Buffer
	// We'll read line by line up to a cap.
	const maxHeaderBytes = 64 * 1024
	lines := []string{}
	for {
		if buf.Len() > maxHeaderBytes {
			return buf.Bytes(), nil, fmt.Errorf("header too large")
		}
		line, err := rd.ReadString('\n')
		if err != nil {
			return nil, nil, err
		}
		buf.WriteString(line)
		lines = append(lines, line)
		if line == "\r\n" || line == "\n" { // end of headers
			break
		}
	}
	origHeader := buf.String()
	// If no changes requested, return original.
	if !stripHost && hostRewrite == "" {
		return []byte(origHeader), nil, nil
	}
	// Process lines.
	var out bytes.Buffer
	hostHandled := false
	for i, line := range lines {
		if i == 0 { // request line
			out.WriteString(line)
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "host:") {
			if stripHost {
				continue // drop it
			}
			if hostRewrite != "" {
				out.WriteString("Host: " + hostRewrite + "\r\n")
				hostHandled = true
				continue
			}
		}
		out.WriteString(line)
	}
	if !stripHost && hostRewrite != "" && !hostHandled {
		// Insert before final CRLF (currently last line is CRLF already appended)
		// We can just add another host header before the blank line; simplest: rewrite by adding before final CRLF.
		// Remove last blank line if present to insert header then re-add.
		outs := out.String()
		if strings.HasSuffix(outs, "\r\n\r\n") {
			outs = strings.TrimSuffix(outs, "\r\n\r\n")
			outs += "\r\nHost: " + hostRewrite + "\r\n\r\n"
			out.Reset()
			out.WriteString(outs)
		} else if strings.HasSuffix(outs, "\n\n") {
			outs = strings.TrimSuffix(outs, "\n\n")
			outs += "\nHost: " + hostRewrite + "\n\n"
			out.Reset()
			out.WriteString(outs)
		} else {
			out.WriteString("Host: " + hostRewrite + "\r\n\r\n")
		}
	}
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

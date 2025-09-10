package httpx

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

// Header represents a single HTTP header field (case preserved as seen on wire).
type Header struct {
	Name  string
	Value string
}

// ProxyHeaders is a parsed representation of an HTTP request start-line + headers.
type ProxyHeaders struct {
	Method  string
	URI     string
	Proto   string
	Headers []Header
	// RawBodyStart holds any bytes read that belong to the body (if header terminator encountered early)
	RawBodyStart []byte
}

// Get returns the first value associated with name (case-insensitive) or empty.
func (p *ProxyHeaders) Get(name string) string {
	lname := strings.ToLower(name)
	for _, h := range p.Headers {
		if strings.ToLower(h.Name) == lname {
			return h.Value
		}
	}
	return ""
}

// Set sets (replaces) a header (case of Name preserved as provided).
func (p *ProxyHeaders) Set(name, value string) {
	lname := strings.ToLower(name)
	for i, h := range p.Headers {
		if strings.ToLower(h.Name) == lname {
			p.Headers[i].Value = value
			return
		}
	}
	p.Headers = append(p.Headers, Header{Name: name, Value: value})
}

// Add appends a header (does not replace existing).
func (p *ProxyHeaders) Add(name, value string) {
	p.Headers = append(p.Headers, Header{Name: name, Value: value})
}

// Del deletes all headers with given name (case-insensitive).
func (p *ProxyHeaders) Del(name string) {
	lname := strings.ToLower(name)
	out := p.Headers[:0]
	for _, h := range p.Headers {
		if strings.ToLower(h.Name) != lname {
			out = append(out, h)
		}
	}
	p.Headers = out
}

// ParseRequest reads from r until complete HTTP headers are obtained or size limit exceeded.
// Supports partial reads from an existing prebuffer (prefill). Returns ProxyHeaders and raw header bytes length.
func ParseRequest(r *bufio.Reader, max int, prefill []byte) (*ProxyHeaders, int, error) {
	buf := append([]byte{}, prefill...)
	for {
		if hasHeaderEnd(buf) {
			break
		}
		if len(buf) > max {
			return nil, 0, fmt.Errorf("header too large (%d>%d)", len(buf), max)
		}
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			buf = append(buf, line...)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, 0, err
		}
	}
	p, err := parseBuffer(buf)
	if err != nil {
		return nil, 0, err
	}
	return p, len(buf), nil
}

func hasHeaderEnd(b []byte) bool {
	return bytes.Contains(b, []byte("\r\n\r\n")) || bytes.Contains(b, []byte("\n\n"))
}

func parseBuffer(buf []byte) (*ProxyHeaders, error) {
	// Split header and possible early body start
	var headerPart, bodyStart []byte
	if idx := bytes.Index(buf, []byte("\r\n\r\n")); idx != -1 {
		headerPart = buf[:idx+4]
		bodyStart = buf[idx+4:]
	} else if idx := bytes.Index(buf, []byte("\n\n")); idx != -1 {
		headerPart = buf[:idx+2]
		bodyStart = buf[idx+2:]
	} else {
		headerPart = buf
	}
	reader := bufio.NewReader(bytes.NewReader(headerPart))
	reqLine, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	reqLine = strings.TrimRight(reqLine, "\r\n")
	parts := strings.Split(reqLine, " ")
	if len(parts) < 3 {
		return nil, fmt.Errorf("bad request line: %q", reqLine)
	}
	ph := &ProxyHeaders{Method: parts[0], URI: parts[1], Proto: parts[2]}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) || len(line) == 0 {
				break
			}
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" { // end
			break
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue // skip malformed
		}
		name := line[:colon]
		value := strings.TrimSpace(line[colon+1:])
		ph.Headers = append(ph.Headers, Header{Name: name, Value: value})
	}
	if len(bodyStart) > 0 {
		ph.RawBodyStart = append([]byte{}, bodyStart...)
	}
	return ph, nil
}

// WriteTo streams headers (with modifications) to w, followed by any pre-read body bytes, then leaves further body streaming to caller.
func (p *ProxyHeaders) WriteTo(w io.Writer) (int64, error) {
	var total int64
	write := func(b []byte) error {
		n, err := w.Write(b)
		total += int64(n)
		return err
	}
	if err := write([]byte(fmt.Sprintf("%s %s %s\r\n", p.Method, p.URI, p.Proto))); err != nil {
		return total, err
	}
	for _, h := range p.Headers {
		if err := write([]byte(h.Name + ": " + h.Value + "\r\n")); err != nil {
			return total, err
		}
	}
	if err := write([]byte("\r\n")); err != nil {
		return total, err
	}
	if len(p.RawBodyStart) > 0 {
		if err := write(p.RawBodyStart); err != nil {
			return total, err
		}
	}
	return total, nil
}

// AugmentXFF appends / sets X-Forwarded-For using clientIP.
func (p *ProxyHeaders) AugmentXFF(clientIP string) {
	if clientIP == "" {
		return
	}
	lname := "x-forwarded-for"
	for i, h := range p.Headers {
		if strings.ToLower(h.Name) == lname {
			p.Headers[i].Value = h.Value + ", " + clientIP
			return
		}
	}
	p.Headers = append(p.Headers, Header{Name: "X-Forwarded-For", Value: clientIP})
}

// ReplaceHost changes Host header (if present) or adds it.
func (p *ProxyHeaders) ReplaceHost(host string) {
	if host == "" {
		return
	}
	lname := "host"
	for i, h := range p.Headers {
		if strings.ToLower(h.Name) == lname {
			p.Headers[i].Value = host
			return
		}
	}
	p.Headers = append(p.Headers, Header{Name: "Host", Value: host})
}

// StripHost removes any Host header.
func (p *ProxyHeaders) StripHost() { p.Del("Host") }

// CopyBody streams remaining data from r to w after headers already written.
func CopyBody(dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)
	return err
}

// RemoteIPFromConn extracts IP portion from remote address.
func RemoteIPFromConn(c net.Conn) string {
	h, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		return c.RemoteAddr().String()
	}
	return h
}

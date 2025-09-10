package server

import (
	"bytes"
	"errors"
	"strings"
)

// ExtractName parses the initial HTTP request bytes to discover the host header or a path prefix.
// It returns (hostHeader, name, rewrittenRequest, error). If a path-based prefix /name/ is used, the
// rewrittenRequest will have that prefix stripped from the path for forwarding to the target.
// ExtractName parses request headers and optional path to extract client name. If baseDomain is non-empty
// and the Host header ends with "."+baseDomain, the left-most label before the baseDomain is treated as the name.
// If baseDomain is empty, the first label of Host is used (legacy behavior).
func ExtractName(req []byte, baseDomain string) (host string, name string, rewritten []byte, err error) {
	// naive search for Host: header
	upper := bytes.ToUpper(req)
	hostIdx := bytes.Index(upper, []byte("HOST:"))
	if hostIdx != -1 {
		lineEnd := bytes.IndexByte(req[hostIdx:], '\n')
		if lineEnd != -1 {
			hostLine := string(bytes.TrimSpace(req[hostIdx+5 : hostIdx+lineEnd]))
			hostLine = strings.TrimPrefix(hostLine, ":")
			hostLine = strings.TrimSpace(hostLine)
			host = hostLine
			if host != "" {
				if baseDomain != "" {
					if strings.HasSuffix(host, "."+baseDomain) {
						trimmed := strings.TrimSuffix(host, "."+baseDomain)
						if trimmed != "" {
							labels := strings.Split(trimmed, ".")
							name = labels[len(labels)-1]
						}
					}
				} else {
					parts := strings.Split(host, ".")
					name = parts[0]
				}
			}
		}
	}
	// Fallback to path prefix
	firstLineEnd := bytes.IndexByte(req, '\n')
	if firstLineEnd == -1 {
		return host, name, req, errors.New("no request line end")
	}
	requestLine := string(bytes.TrimSpace(req[:firstLineEnd]))
	// METHOD /something HTTP/1.1
	fields := strings.Split(requestLine, " ")
	if len(fields) < 2 {
		return host, name, req, errors.New("bad request line")
	}
	path := fields[1]
	if name == "" && strings.Count(path, "/") > 1 { // at least /name/
		elems := strings.Split(path, "/")
		if len(elems) > 2 {
			candidate := elems[1]
			if candidate != "" {
				name = candidate
				// rewrite path without candidate
				newPath := "/" + strings.Join(elems[2:], "/")
				if newPath == "/" { // if no remaining path
					newPath = "/"
				}
				fields[1] = newPath
				newReqLine := strings.Join(fields, " ")
				rewritten = bytes.Replace(req, []byte(requestLine), []byte(newReqLine), 1)
				return host, name, rewritten, nil
			}
		}
	}
	return host, name, req, nil
}

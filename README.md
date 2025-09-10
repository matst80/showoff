# showoff

Minimal experimental ngrok-like tunnel (educational prototype).

## Overview

Two binaries:

* `showoff-server` (cmd/server) – runs on a public host, accepts client registrations and public traffic.
* `showoff-client` (cmd/client) – runs behind NAT, maintains a control connection, and opens data tunnels on demand to proxy traffic to a local service.

Current constraints (initial version):

* Only raw TCP for data; public side expects HTTP and uses Host header subdomain or path prefix `/name/...` to choose client.
* No TLS / encryption (use within trusted networks or behind your own TLS terminator / reverse proxy like Caddy or nginx).
* Very small, unauthenticated (optional shared token) and not production-hardened. Use for learning only.

## Protocol (line-oriented JSON)

Control connection (client -> server first line):

```json
{"token":"<shared>", "name":"myapp", "target":"127.0.0.1:3000"}
```

Server ack:

```json
{"msg":"ok"}
```

When public request arrives for `myapp`, server sends:

```json
{"id":"<random>", "name":"myapp"}
```

Client opens a new TCP connection to server data port and first line:

```json
{"id":"<same-random>"}
```

Then raw bidirectional copy begins (initial HTTP request bytes currently NOT re-sent if they were buffered before tunnel establishment – future improvement).

## Build

```powershell
go build ./...
```

## Run Example

In one terminal (server):

```powershell
go run ./cmd/server --control :9000 --public :8080 --data :9001 --token secret
```

In another terminal (client exposing local web on 3000):

```powershell
go run ./cmd/client --server 127.0.0.1:9000 --data 127.0.0.1:9001 --name myapp --token secret --target 127.0.0.1:5173
```

Access via (adjust Host header). On Windows with curl you can specify:

```powershell
curl -H "Host: myapp.local" http://127.0.0.1:8080/
```

Or path-based:

```powershell
curl http://127.0.0.1:8080/myapp/
```

## Roadmap Ideas

* Forward initial buffered request after tunnel ready.
* Multiplex multiple streams over single data TCP using yamux or HTTP/2.
* TLS mutual auth, per-client tokens.
* Web UI / status.
* UDP support.
* Idle timeout and better cleanup of pending connections.

## Disclaimer

Prototype code. Not audited. Don't expose untrusted traffic.

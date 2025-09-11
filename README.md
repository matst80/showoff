# showoff

Minimal experimental ngrok-like tunnel (educational prototype). Vibe coded so far!!

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

Shortcut: if control & data ports are standard (9000/9001) you can now just specify the host once:

```powershell
go run ./cmd/client --host 127.0.0.1 --name myapp --token secret --target 127.0.0.1:5173
```

Access via (adjust Host header). On Windows with curl you can specify:

```powershell
curl -H "Host: myapp.local" http://127.0.0.1:8080/
```

Or path-based:

```powershell
curl http://127.0.0.1:8080/myapp/
```

## Kubernetes Deployment

An example manifest is provided in `k8s/showoff.yaml` which deploys the server with:
* Deployment + readiness/liveness probes (`/readyz`, `/healthz`)
* Services:
	* `showoff-public` (ClusterIP -> server public port 8080)
	* `showoff-tunnel` (LoadBalancer exporting control 9000 & data 9001)
	* `showoff-metrics` (ClusterIP -> metrics/health 9100)
* Wildcard Ingress (nginx) with TLS offload via cert-manager (update `*.example.com`).

### Apply (after adjusting domains & image)

```powershell
kubectl apply -f k8s/showoff.yaml
```

Wait for the `showoff-tunnel` LoadBalancer to obtain an external address:

```powershell
kubectl get svc showoff-tunnel
```

Assume it becomes (example):
```
NAME             TYPE           CLUSTER-IP     EXTERNAL-IP      PORT(S)
showoff-tunnel   LoadBalancer   10.0.120.55    10.10.3.13       9000:.../TCP,9001:.../TCP
```

And your Ingress wildcard domain `*.example.com` points to the nginx ingress controller load balancer (DNS setup required).

### Starting a Client against Kubernetes Server

You need two addresses: control port (9000) and data port (9001) from the LoadBalancer. If both map to the same external IP you can just change ports.

```powershell
go run ./cmd/client `
	--server 10.10.3.13:9000 `
	--data 10.10.3.13:9001 `
	--name myapp `
	--token change-me `
	--target 127.0.0.1:5173 `
	--host-rewrite localhost
```

Or with the new host shortcut (auto uses :9000 / :9001):

```powershell
go run ./cmd/client \
	--host 10.10.3.13 \
	--name myapp \
	--token change-me \
	--target 127.0.0.1:5173 \
	--host-rewrite localhost
```

If you enabled a wildcard Ingress for public traffic, a user can reach your tunneled service via:

```powershell
curl https://myapp.example.com/
```

If not using Ingress yet (only Service), port-forward or expose service:

```powershell
kubectl port-forward svc/showoff-public 8080:80
curl -H "Host: myapp.example.com" http://127.0.0.1:8080/
```

### Metrics & Health in the Cluster

```powershell
kubectl port-forward svc/showoff-metrics 9100:9100
curl http://127.0.0.1:9100/show-off/metrics
curl http://127.0.0.1:9100/readyz
```

### Token Management

The deployment expects a Secret `showoff-token` with key `token`. Update it:

```powershell
kubectl create secret generic showoff-token --from-literal=token=secret --dry-run=client -o yaml | kubectl apply -f -
```

### Updating the Image

After building & pushing your image:

```powershell
kubectl set image deployment/showoff-server server=your-registry/showoff-server:TAG
```

### Environment Variable Configuration (Future)

Flags are currently used; an upcoming enhancement will mirror them via env vars for simpler container config.

## Roadmap Ideas

* Forward initial buffered request after tunnel ready.
* Multiplex multiple streams over single data TCP using yamux or HTTP/2.
* TLS mutual auth, per-client tokens.
* Web UI / status.
* UDP support.
* Idle timeout and better cleanup of pending connections.

## Disclaimer

Prototype code. Not audited. Don't expose untrusted traffic.

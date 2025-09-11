# Redis Horizontal Scaling Documentation

## Overview

The showoff server now supports horizontal scaling through Redis shared state, implementing the P2 roadmap item "Horizontal scaling plan - Strategy: sticky hashing for names or shared state (Redis/NATS)".

## Configuration

Add the following flags to enable Redis-backed state:

```bash
go run ./cmd/server \
    --redis-addr localhost:6379 \
    --redis-password your-redis-password \
    --redis-db 0 \
    [other existing flags...]
```

### Configuration Options

- `--redis-addr`: Redis server address (host:port). If empty, uses in-memory state (default behavior)
- `--redis-password`: Redis password for authentication (optional)
- `--redis-db`: Redis database number (default: 0)

## How It Works

### Shared State

When Redis is configured, client registrations are stored in Redis and can be accessed by any server instance:

- Client sessions are stored as JSON in Redis with keys like `client:myapp`
- Instance mappings track which server instance a client is connected to (`instance:myapp`)
- Client data includes name, last seen timestamp, but excludes the control connection

### Local State

Some state remains local to each server instance for performance and technical reasons:

- **Pending Requests**: Active network connections waiting for client data tunnels
- **Control Connections**: The actual TCP connections to clients cannot be serialized

### High Availability

- Client registrations have a 24-hour TTL to prevent stale entries
- Server instances are identified by unique IDs
- Graceful fallback to in-memory state when Redis is unavailable

## Limitations

1. **Control Connection Locality**: When a client registers on server A, requests can be routed through any server instance, but the control connection remains on server A. This means:
   - Cross-instance request routing will not work for active tunneling
   - This is the foundation for future improvements (pub/sub coordination)

2. **Pending Request Isolation**: Pending requests are local to each server instance and cannot be shared across instances

## Future Enhancements

This implementation provides the foundation for:

- P3 Distributed coordination: Shared pub/sub for request dispatch
- Sticky hashing for client names to improve routing
- Cross-instance control connection proxying

## Testing

Test with in-memory state (default):
```bash
go run ./cmd/server --token test
```

Test with Redis (requires Redis running):
```bash
go run ./cmd/server --token test --redis-addr localhost:6379
```

## Example Multi-Instance Setup

```bash
# Terminal 1 - Server instance 1
go run ./cmd/server \
    --control :9000 \
    --public :8080 \
    --data :9001 \
    --redis-addr localhost:6379 \
    --token secret

# Terminal 2 - Server instance 2  
go run ./cmd/server \
    --control :9010 \
    --public :8090 \
    --data :9011 \
    --redis-addr localhost:6379 \
    --token secret
```

Both instances will share client registration state through Redis.
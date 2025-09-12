# mTLS Configuration Guide

This document describes how to configure mutual TLS (mTLS) authentication between the showoff client and server for strong mutual identity verification.

## Overview

The showoff tunnel supports three connection modes:
1. **Plain TCP** (default) - No encryption
2. **TLS** - Server authentication only  
3. **mTLS** - Mutual authentication (both client and server verify each other)

## Certificate Requirements

For mTLS, you need:
- **CA Certificate** - Root certificate authority to sign both client and server certificates
- **Server Certificate & Key** - Server's identity certificate signed by the CA
- **Client Certificate & Key** - Client's identity certificate signed by the CA

## Server Configuration

### TLS Only (Server Authentication)
```bash
./server \
  --tls \
  --tls-cert server.crt \
  --tls-key server.key
```

### mTLS (Mutual Authentication)
```bash
./server \
  --tls \
  --tls-cert server.crt \
  --tls-key server.key \
  --tls-ca ca.crt
```

### Server Flags
- `--tls` - Enable TLS for control and data connections
- `--tls-cert` - Path to server certificate file
- `--tls-key` - Path to server private key file  
- `--tls-ca` - Path to CA certificate (enables client certificate verification)

## Client Configuration

### TLS Only (Verify Server)
```bash
./client \
  --tls \
  --tls-ca ca.crt \
  --server secure.example.com:9000
```

### mTLS (Mutual Authentication)
```bash
./client \
  --tls \
  --tls-cert client.crt \
  --tls-key client.key \
  --tls-ca ca.crt \
  --server secure.example.com:9000
```

### Client Flags  
- `--tls` - Enable TLS for server connections
- `--tls-cert` - Path to client certificate file (for mTLS)
- `--tls-key` - Path to client private key file (for mTLS)
- `--tls-ca` - Path to CA certificate for server verification
- `--tls-insecure` - Skip certificate verification (insecure, testing only)

## Certificate Generation Example

Here's how to generate test certificates:

```bash
# Generate CA private key
openssl genrsa -out ca.key 2048

# Generate CA certificate
openssl req -new -x509 -days 365 -key ca.key -out ca.crt -subj "/CN=Test CA"

# Generate server private key
openssl genrsa -out server.key 2048

# Create server certificate with SAN
cat > server.conf <<EOF
[req]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[req_distinguished_name]
CN = your-server.com

[v3_req]
keyUsage = keyEncipherment, dataEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = your-server.com
DNS.2 = localhost
IP.1 = 127.0.0.1
EOF

# Generate server certificate
openssl req -new -key server.key -out server.csr -config server.conf
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out server.crt -days 365 -extensions v3_req -extfile server.conf

# Generate client private key  
openssl genrsa -out client.key 2048

# Generate client certificate
openssl req -new -key client.key -out client.csr -subj "/CN=client-name"
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out client.crt -days 365
```

## Security Considerations

1. **Certificate Validation**: Server certificates must include proper Subject Alternative Names (SANs)
2. **CA Security**: Keep the CA private key secure and rotate certificates regularly
3. **Key Protection**: Protect private keys with appropriate file permissions (600)
4. **Certificate Rotation**: Implement certificate renewal before expiration
5. **Revocation**: Consider implementing certificate revocation if needed

## Troubleshooting

### Common Errors

**"tls: client didn't provide a certificate"**
- Client not configured with certificate for mTLS server
- Add `--tls-cert` and `--tls-key` flags to client

**"tls: failed to verify certificate"**  
- Server certificate not trusted by client
- Ensure `--tls-ca` points to correct CA certificate
- Check certificate SANs match server hostname

**"remote error: tls: bad certificate"**
- Client certificate not trusted by server  
- Ensure client certificate is signed by server's trusted CA

### Debug Tips

1. Enable debug logging with `--debug` flag
2. Verify certificate details with: `openssl x509 -in cert.crt -text -noout`
3. Test connectivity with: `openssl s_client -connect host:port -cert client.crt -key client.key`

## Backward Compatibility

mTLS is opt-in and fully backward compatible:
- Existing plain TCP configurations continue to work unchanged
- TLS can be enabled incrementally (server-only first, then client certificates)
- Public listener remains plain TCP for external HTTP traffic
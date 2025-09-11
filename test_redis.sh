#!/bin/bash

# Test script for Redis functionality
# Note: This test requires Redis to be running on localhost:6379

echo "Testing showoff with Redis backend..."

# Start a local Redis server if needed (using Docker)
if command -v docker >/dev/null 2>&1; then
    echo "Starting Redis container..."
    docker run -d --name showoff-redis-test -p 6379:6379 redis:latest 2>/dev/null || true
    sleep 2
fi

# Test 1: Start server with Redis backend
echo "Starting server with Redis backend..."
timeout 3s go run ./cmd/server \
    --control :9010 \
    --public :8010 \
    --data :9011 \
    --token test \
    --redis-addr localhost:6379 \
    --debug

echo "Test completed."

# Cleanup
if command -v docker >/dev/null 2>&1; then
    echo "Cleaning up Redis container..."
    docker stop showoff-redis-test >/dev/null 2>&1
    docker rm showoff-redis-test >/dev/null 2>&1
fi
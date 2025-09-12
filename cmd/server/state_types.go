package main

import (
	"net"
	"time"
)

// clientSession represents a connected client control session.
// controlConn is only valid for the local instance that accepted it.
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

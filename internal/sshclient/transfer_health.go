package sshclient

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type handshakeGate struct {
	slots chan struct{}
	refs  int
}

// Bound only simultaneous authentication attempts to a host. Established
// transfers, conflict prompts and retry delays never occupy these slots.
func (m *Manager) transferHandshake(ctx context.Context, address string) (func(), error) {
	m.transferMu.Lock()
	if m.transferStarts == nil {
		m.transferStarts = map[string]*handshakeGate{}
	}
	gate := m.transferStarts[address]
	if gate == nil {
		gate = &handshakeGate{slots: make(chan struct{}, 2)}
		m.transferStarts[address] = gate
	}
	gate.refs++
	m.transferMu.Unlock()
	releaseRef := func() {
		m.transferMu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(m.transferStarts, address)
		}
		m.transferMu.Unlock()
	}
	select {
	case gate.slots <- struct{}{}:
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() { once.Do(func() { <-gate.slots; releaseRef() }) }, nil
}

type transferConn struct {
	net.Conn
	lastRead atomic.Int64
}

func newTransferConn(conn net.Conn) *transferConn {
	tracked := &transferConn{Conn: conn}
	tracked.lastRead.Store(time.Now().UnixNano())
	return tracked
}

func (c *transferConn) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	if n > 0 {
		c.lastRead.Store(time.Now().UnixNano())
	}
	return n, err
}

func (c *transferConn) lastActivity() time.Time { return time.Unix(0, c.lastRead.Load()) }

// A busy SFTP server can delay global-request replies while still moving data.
// Do not kill a healthy transfer just because one heartbeat takes ten seconds;
// abort only after sixty seconds without any inbound transport activity.
func keepTransferAlive(ctx context.Context, send func() error, activity func() time.Time, closeConnection func(), interval, idleTimeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var pending <-chan error
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-pending:
			pending = nil
			if err != nil {
				closeConnection()
				return
			}
		case <-ticker.C:
			if time.Since(activity()) >= idleTimeout {
				closeConnection()
				return
			}
			if pending == nil {
				result := make(chan error, 1)
				pending = result
				go func() { result <- send() }()
			}
		}
	}
}

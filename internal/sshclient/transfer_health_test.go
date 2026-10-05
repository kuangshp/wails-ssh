package sshclient

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDelayedHeartbeatDoesNotDropActiveTransfer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	blockedReply := make(chan struct{})
	closed := make(chan struct{})
	var once sync.Once
	go keepTransferAlive(ctx, func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-blockedReply:
			return nil
		}
	},
		func() time.Time { return time.Unix(0, lastRead.Load()) }, func() { once.Do(func() { close(closed) }) }, 5*time.Millisecond, 50*time.Millisecond)
	// The heartbeat remains unanswered, but incoming SFTP packets prove that the
	// connection is healthy. This outlasts the configured idle timeout twice.
	until := time.NewTimer(120 * time.Millisecond)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-closed:
			t.Fatal("active transfer was closed because a heartbeat reply was delayed")
		case <-tick.C:
			lastRead.Store(time.Now().UnixNano())
		case <-until.C:
			goto quiet
		}
	}
quiet:
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("inactive transport was not closed after activity stopped")
	}
}

func TestFailedHeartbeatClosesTransfer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := make(chan struct{})
	go keepTransferAlive(ctx, func() error { return errors.New("transport closed") }, time.Now, func() { close(closed) }, time.Millisecond, time.Second)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("heartbeat transport failure was ignored")
	}
}

func TestHandshakeLimitIsPerHostAndOnlyCoversAuthentication(t *testing.T) {
	m := New("", nil)
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := m.transferHandshake(ctx, "host-a:22")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.transferHandshake(ctx, "host-a:22")
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.transferHandshake(ctx, "host-b:22")
	if err != nil {
		t.Fatal(err)
	}
	other()
	acquired := make(chan func(), 1)
	go func() {
		release, err := m.transferHandshake(ctx, "host-a:22")
		if err == nil {
			acquired <- release
		}
	}()
	select {
	case release := <-acquired:
		release()
		t.Fatal("third authentication bypassed the per-host limit")
	case <-time.After(20 * time.Millisecond):
	}
	first()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("completed authentication kept its slot")
	}
	second()
	m.transferMu.Lock()
	remaining := len(m.transferStarts)
	m.transferMu.Unlock()
	if remaining != 0 {
		t.Fatalf("authentication gate leaked %d host entries", remaining)
	}
}

func TestWaitingHandshakeHonorsCancellation(t *testing.T) {
	m := New("", nil)
	defer m.Close()
	first, _ := m.transferHandshake(context.Background(), "host:22")
	defer first()
	second, _ := m.transferHandshake(context.Background(), "host:22")
	defer second()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.transferHandshake(ctx, "host:22"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter returned %v", err)
	}
}

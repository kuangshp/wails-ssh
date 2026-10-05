package sshclient

import (
	"context"
	"testing"
)

func TestTransferTransportSurvivesTerminalClosureAndHonorsCancellation(t *testing.T) {
	f := server(t, true)
	m := trustedManager(t, f, nil)
	connection, err := m.Connect(f.profile(), "secret", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := m.TransferScope(connection.ID)
	if err != nil || scope != f.listener.Addr().String() {
		t.Fatalf("transfer scope does not identify authenticated endpoint: %q, %v", scope, err)
	}
	dial, err := m.TransferDialer(connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	files, cleanup, err := dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := m.Disconnect(connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := files.RealPath("."); err != nil {
		t.Fatalf("closing a terminal interrupted its independent transfer: %v", err)
	}
	// A checkpoint can reconnect with the captured credentials after its PTY died.
	resumed, closeResumed, err := dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closeResumed()
	if _, err := resumed.RealPath("."); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, _, err := dial(ctx); err == nil {
		t.Fatal("cancelled transfer context permitted redial")
	}
	m.Close()
	if _, _, err := dial(context.Background()); err == nil {
		t.Fatal("closed app permitted transfer redial")
	}
}

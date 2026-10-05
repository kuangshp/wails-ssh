package sshclient

import (
	"path/filepath"
	"testing"
)

func TestConnectionTestDoesNotCreateTerminalOrSession(t *testing.T) {
	f := server(t, false)
	m := trustedManager(t, f, func(name string, args ...interface{}) {
		t.Errorf("connection test emitted lifecycle event %s", name)
	})
	if err := m.TestConnection(f.profile(), "secret"); err != nil {
		t.Fatal(err)
	}
	if f.shells.Load() != 0 || len(m.sessions) != 0 {
		t.Fatal("connection test must not create a terminal or managed session")
	}
}

func TestConnectionTestRequiresTrustAndAuthentication(t *testing.T) {
	f := server(t, false)
	untrusted := New(filepath.Join(t.TempDir(), "known_hosts"), nil)
	t.Cleanup(untrusted.Close)
	if err := untrusted.TestConnection(f.profile(), "secret"); err == nil {
		t.Fatal("untrusted host accepted")
	}
	m := trustedManager(t, f, nil)
	if err := m.TestConnection(f.profile(), "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	m.Close()
	if err := m.TestConnection(f.profile(), "secret"); err == nil {
		t.Fatal("closed manager accepted a test")
	}
}

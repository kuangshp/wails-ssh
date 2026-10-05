package main

import (
	"path/filepath"
	"testing"

	"wails-ssh/internal/model"
	"wails-ssh/internal/storage"
)

func TestEditorConnectionSecretReuse(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.Save(model.Profile{Name: "fixture", Host: "example.invalid", Port: 22, Username: "test", AuthKind: "private_key", KeyPath: "/fixture/old-key"}, "fixture-only", false)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: s}
	for _, tc := range []struct {
		name       string
		profile    model.Profile
		secret     string
		keepSecret bool
		want       string
	}{
		{"retain unchanged key", p, "", true, "fixture-only"},
		{"explicit replacement", p, "replacement-fixture", true, "replacement-fixture"},
		{"unchecked retention", p, "", false, ""},
		{"changed authentication", model.Profile{ID: p.ID, AuthKind: "password"}, "", true, ""},
		{"changed key path", model.Profile{ID: p.ID, AuthKind: "private_key", KeyPath: "/fixture/new-key"}, "", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret, err := a.testSecret(tc.profile, tc.secret, tc.keepSecret)
			if err != nil || secret != tc.want {
				t.Fatalf("credential resolution failed: error=%v", err)
			}
		})
	}
	stored, err := s.Get(p.ID)
	if err != nil || stored.LastConnectedAt != "" || stored.KeyPath != p.KeyPath {
		t.Fatal("resolving a test credential changed the stored profile")
	}
	// A test with no usable password must fail before touching the SSH manager.
	password := model.Profile{Host: "example.invalid", Port: 22, Username: "test", AuthKind: "password"}
	if err := a.TestConnection(password, "", false); err == nil {
		t.Fatal("connection test accepted a missing password")
	}
}

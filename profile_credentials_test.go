package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"wails-ssh/internal/model"
	"wails-ssh/internal/storage"
)

func credentialTestApp(t *testing.T) *App {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "credentials-fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &App{store: s}
}

func TestExplicitProfileCredentialActions(t *testing.T) {
	for _, authKind := range []string{"password", "private_key"} {
		t.Run(authKind, func(t *testing.T) {
			a := credentialTestApp(t)
			p, err := a.SaveProfile(model.Profile{
				Name: "credential fixture", Host: "192.0.2.12", Port: 2222, Username: "fixture-user",
				AuthKind: authKind, KeyPath: filepath.Join(t.TempDir(), "key-that-does-not-exist"),
			}, " synthetic-fixture-secret ", false)
			if err != nil {
				t.Fatal(err)
			}
			secret, err := a.RevealProfileSecret(p.ID)
			if err != nil || secret != " synthetic-fixture-secret " {
				t.Fatalf("explicit reveal did not preserve the saved credential: %v", err)
			}
			// Resolve current saved values, without touching the OS clipboard.
			p.Host, p.Port, p.Username = "2001:db8::12", 2201, "updated-user"
			p, err = a.SaveProfile(p, "replacement-fixture-secret", false)
			if err != nil {
				t.Fatal(err)
			}
			current, currentSecret, err := a.profileCredentials(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			label := "密码"
			if authKind == "private_key" {
				label = "私钥口令"
			}
			want := "IP: 2001:db8::12\n端口: 2201\n账号: updated-user\n" + label + ": replacement-fixture-secret"
			if formatProfileCredentials(current, currentSecret) != want {
				t.Fatal("credential copy format did not use current saved connection details")
			}
			// The key path deliberately does not exist: reveal/copy formatting must
			// never read private-key file contents to retrieve its saved passphrase.
			if strings.Contains(formatProfileCredentials(current, currentSecret), p.KeyPath) {
				t.Fatal("private-key path unexpectedly included in connection details")
			}
			profiles, err := a.ListProfiles()
			if err != nil || len(profiles) != 1 || !profiles[0].HasSecret {
				t.Fatalf("profile listing failed after explicit reveal: %v", err)
			}
			payload, err := json.Marshal(profiles)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(payload), currentSecret) || strings.Contains(string(payload), "synthetic-fixture-secret") {
				t.Fatal("normal profile response exposed saved credentials")
			}
			var fields []map[string]any
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"secret", "password", "passphrase"} {
				if _, exists := fields[0][name]; exists {
					t.Fatal("normal profile response contains a plaintext credential field")
				}
			}
		})
	}
}

func TestProfileCredentialActionsRejectUnavailableCredentials(t *testing.T) {
	a := credentialTestApp(t)
	for _, authKind := range []string{"password", "private_key"} {
		t.Run("missing "+authKind, func(t *testing.T) {
			p, err := a.SaveProfile(model.Profile{
				Name: "missing credential", Host: "example.invalid", Port: 22, Username: "fixture-user",
				AuthKind: authKind, KeyPath: "/fixture/nonexistent-key",
			}, "", false)
			if err != nil {
				t.Fatal(err)
			}
			want := "该连接没有保存密码"
			if authKind == "private_key" {
				want = "该连接没有保存私钥口令"
			}
			secret, err := a.RevealProfileSecret(p.ID)
			if secret != "" || err == nil || err.Error() != want {
				t.Fatal("missing credential reveal did not return a clear error")
			}
			// App.ctx is nil; these errors must return before any clipboard call.
			if err := a.CopyProfileCredentials(p.ID); err == nil || err.Error() != want {
				t.Fatal("copy accepted an incomplete credential bundle")
			}
		})
	}
	if _, err := a.RevealProfileSecret(123456789); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("nonexistent reveal must preserve the store error: %v", err)
	}
	if err := a.CopyProfileCredentials(123456789); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("nonexistent copy must preserve the store error: %v", err)
	}
	for _, unavailable := range []*App{{}, {initErr: errors.New("fixture initialization failed")}} {
		_, revealErr := unavailable.RevealProfileSecret(1)
		copyErr := unavailable.CopyProfileCredentials(1)
		if revealErr == nil || copyErr == nil || revealErr.Error() != unavailable.ready().Error() || copyErr.Error() != unavailable.ready().Error() {
			t.Fatal("credential actions did not enforce application readiness")
		}
	}
}

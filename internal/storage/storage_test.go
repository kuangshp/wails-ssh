package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wails-ssh/internal/model"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "servers.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func testProfile() model.Profile {
	return model.Profile{Name: "生产服务器", Host: "localhost", Port: 22, Username: "root", AuthKind: "password", GroupName: "生产", Remark: "测试"}
}
func TestCredentialsAndHistory(t *testing.T) {
	s := testStore(t)
	p, err := s.Save(testProfile(), "secret", false)
	if err != nil {
		t.Fatal(err)
	}
	p.Remark = "更新"
	p, err = s.Save(p, "", true)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := s.Secret(p.ID)
	if secret != "secret" || !p.HasSecret {
		t.Fatal("credential lost")
	}
	list, _ := s.List()
	data, _ := json.Marshal(list)
	if strings.Contains(string(data), `"secret"`) {
		t.Fatal("secret exposed")
	}
	for _, cmd := range []string{"pwd", "ls -lah", "pwd"} {
		if err = s.RecordCommand(p.ID, cmd); err != nil {
			t.Fatal(err)
		}
	}
	history, _ := s.History(p.ID)
	if len(history) != 2 || history[0] != "pwd" || history[1] != "ls -lah" {
		t.Fatal(history)
	}
	p, err = s.Save(p, "", false)
	if err != nil || p.HasSecret {
		t.Fatal("credential was not cleared", err)
	}
	if err = s.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	history, _ = s.History(p.ID)
	if len(history) != 0 {
		t.Fatal("orphan history")
	}
}

func TestHistoryRemembersRecentCommandsAfterReopen(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "history.db")
	s, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, err := s.Save(testProfile(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	other := testProfile()
	other.Name = "another host"
	other, err = s.Save(other, "", false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		if err := s.RecordCommand(p.ID, fmt.Sprintf("echo %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordCommand(p.ID, "echo 10"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCommand(other.ID, "whoami"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.History(p.ID)
	if err != nil || len(history) != 200 || history[0] != "echo 10" || history[1] != "echo 204" {
		t.Fatalf("history was not restored in recent order: %v %v", history, err)
	}
	otherHistory, err := s.History(other.ID)
	if err != nil || len(otherHistory) != 1 || otherHistory[0] != "whoami" {
		t.Fatalf("history leaked across hosts: %v %v", otherHistory, err)
	}
}
func TestImportLegacyReadOnlyAndDedup(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE server_profiles(name TEXT,host TEXT,port INTEGER,username TEXT,auth_kind TEXT,secret TEXT);INSERT INTO server_profiles VALUES('legacy','127.0.0.1',22,'root','password','legacy-secret')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, _ := os.ReadFile(legacy)
	s := testStore(t)
	n, err := s.ImportLegacy(legacy)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	after, _ := os.ReadFile(legacy)
	if string(before) != string(after) {
		t.Fatal("source modified")
	}
	list, _ := s.List()
	secret, _ := s.Secret(list[0].ID)
	if secret != "legacy-secret" {
		t.Fatal("credentials not imported")
	}
	n, err = s.ImportLegacy(legacy)
	if err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
func TestImportRollsBackInvalidProfile(t *testing.T) {
	s := testStore(t)
	p := testProfile()
	bad := p
	bad.Port = 0
	if _, err := s.ImportProfiles([]model.Profile{p, bad}); err == nil {
		t.Fatal("expected error")
	}
	items, _ := s.List()
	if len(items) != 0 {
		t.Fatal("partial import")
	}
}
func TestSSHConfig(t *testing.T) {
	content := `Host alpha beta
 HostName = 10.0.0.1 # production
 User deploy
 Port 2222
 IdentityFile "~/.ssh/my key"
Host *.internal
 User other
Match all
 User wrong
Host plain
 HostName example.com
`
	profiles, err := ParseSSHConfig(strings.NewReader(content), "/Users/test")
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 3 {
		t.Fatal(profiles)
	}
	if profiles[1].KeyPath != "/Users/test/.ssh/my key" || profiles[1].Port != 2222 {
		t.Fatal(profiles[1])
	}
	if profiles[2].Username != "root" {
		t.Fatal("Match leaked")
	}
}

package transfer

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

type event struct {
	name string
	data interface{}
}

func testService(t *testing.T) (*Service, <-chan event) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	server, err := sftp.NewServer(serverConn)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve() }()
	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan event, 256)
	service := New(func(string) (*sftp.Client, error) { return client, nil }, func(name string, args ...interface{}) {
		events <- event{name, args[0]}
	})
	t.Cleanup(func() { service.Close(); client.Close(); server.Close() })
	return service, events
}

func waitTransfer(t *testing.T, events <-chan event, id string, onEvent func(event)) Progress {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			if onEvent != nil {
				onEvent(ev)
			}
			if p, ok := ev.data.(Progress); ok && p.ID == id {
				switch p.Status {
				case "completed", "failed", "cancelled", "skipped":
					return p
				}
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for transfer %s", id)
		}
	}
}

func putFile(t *testing.T, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, content, 0644); err != nil {
		t.Fatal(err)
	}
}

func assertContent(t *testing.T, name string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("unexpected content at %s: got %d bytes, want %d", name, len(got), len(want))
	}
}

func symlinkOrSkip(t *testing.T, old, new string) {
	t.Helper()
	if err := os.Symlink(old, new); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func remoteName(name string) string {
	name = filepath.ToSlash(name)
	if runtime.GOOS == "windows" {
		return "/" + name
	}
	return name
}

func assertNoTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(entry.Name(), "wails-ssh-") {
			return fmt.Errorf("temporary file left behind: %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRecursiveUploadDownload(t *testing.T) {
	s, events := testService(t)
	base := t.TempDir()
	source, remote, downloads := filepath.Join(base, "source"), filepath.Join(base, "remote"), filepath.Join(base, "downloads")
	content := bytes.Repeat([]byte("ssh transfer 内容\n"), 20000)
	putFile(t, filepath.Join(source, "nested", "large.txt"), content)
	putFile(t, filepath.Join(source, "empty.txt"), nil)
	if err := os.MkdirAll(remote, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(downloads, 0755); err != nil {
		t.Fatal(err)
	}
	id, err := s.Upload("session", []string{source}, remoteName(remote))
	if err != nil {
		t.Fatal(err)
	}
	if p := waitTransfer(t, events, id, nil); p.Status != "completed" {
		t.Fatalf("upload: %+v", p)
	}
	assertContent(t, filepath.Join(remote, "source", "nested", "large.txt"), content)
	id, err = s.Download("session", remoteName(filepath.Join(remote, "source")), downloads)
	if err != nil {
		t.Fatal(err)
	}
	if p := waitTransfer(t, events, id, nil); p.Status != "completed" {
		t.Fatalf("download: %+v", p)
	}
	assertContent(t, filepath.Join(downloads, "source", "nested", "large.txt"), content)
	assertContent(t, filepath.Join(downloads, "source", "empty.txt"), nil)
	assertNoTemporaryFiles(t, base)
}

func TestConflictsOverwriteAllAndSkipAll(t *testing.T) {
	for _, choice := range []string{"overwrite_all", "skip_all"} {
		t.Run(choice, func(t *testing.T) {
			s, events := testService(t)
			base := t.TempDir()
			source, target := filepath.Join(base, "source"), filepath.Join(base, "target")
			for _, name := range []string{"one.txt", "two.txt"} {
				putFile(t, filepath.Join(source, name), []byte("new"))
				putFile(t, filepath.Join(target, "source", name), []byte("old"))
			}
			id, err := s.Upload("session", []string{source}, remoteName(target))
			if err != nil {
				t.Fatal(err)
			}
			conflicts := 0
			p := waitTransfer(t, events, id, func(ev event) {
				if c, ok := ev.data.(Conflict); ok {
					conflicts++
					if err := s.ResolveConflict(c.ID, choice); err != nil {
						t.Fatal(err)
					}
				}
			})
			want, status := "new", "completed"
			if choice == "skip_all" {
				want, status = "old", "skipped"
			}
			if conflicts != 1 || p.Status != status {
				t.Fatalf("conflicts=%d, progress=%+v", conflicts, p)
			}
			assertContent(t, filepath.Join(target, "source", "one.txt"), []byte(want))
			assertContent(t, filepath.Join(target, "source", "two.txt"), []byte(want))
			assertNoTemporaryFiles(t, base)
		})
	}
}

func TestCancelPendingConflictPreservesDestination(t *testing.T) {
	s, events := testService(t)
	base := t.TempDir()
	source, target := filepath.Join(base, "source", "file.txt"), filepath.Join(base, "target")
	putFile(t, source, []byte("new"))
	putFile(t, filepath.Join(target, "file.txt"), []byte("original"))
	id, err := s.Upload("session", []string{source}, remoteName(target))
	if err != nil {
		t.Fatal(err)
	}
	p := waitTransfer(t, events, id, func(ev event) {
		if _, ok := ev.data.(Conflict); ok {
			if err := s.Cancel(id); err != nil {
				t.Fatal(err)
			}
		}
	})
	if p.Status != "cancelled" {
		t.Fatalf("progress=%+v", p)
	}
	assertContent(t, filepath.Join(target, "file.txt"), []byte("original"))
	assertNoTemporaryFiles(t, base)
}

func TestCancelSessionReleasesOnlyItsPendingConflict(t *testing.T) {
	s, events := testService(t)
	base := t.TempDir()
	source := filepath.Join(base, "source", "file.txt")
	putFile(t, source, []byte("new"))
	targets := []string{filepath.Join(base, "first"), filepath.Join(base, "second")}
	ids := make([]string, 2)
	for i, target := range targets {
		putFile(t, filepath.Join(target, "file.txt"), []byte("original"))
		var err error
		ids[i], err = s.Upload(fmt.Sprintf("session-%d", i), []string{source}, remoteName(target))
		if err != nil {
			t.Fatal(err)
		}
	}
	conflicts := make(map[string]bool)
	completed := make(map[string]string)
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for len(completed) < 2 {
		select {
		case ev := <-events:
			if conflict, ok := ev.data.(Conflict); ok {
				conflicts[conflict.ID] = true
				if len(conflicts) == 2 {
					s.CancelSession("session-0")
					if err := s.ResolveConflict(ids[1], "overwrite"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if p, ok := ev.data.(Progress); ok && (p.Status == "completed" || p.Status == "failed" || p.Status == "cancelled" || p.Status == "skipped") {
				completed[p.ID] = p.Status
			}
		case <-timeout.C:
			t.Fatal("session cancellation left a transfer waiting")
		}
	}
	if completed[ids[0]] != "cancelled" || completed[ids[1]] != "completed" {
		t.Fatalf("unexpected terminal states: %v", completed)
	}
	assertContent(t, filepath.Join(targets[0], "file.txt"), []byte("original"))
	assertContent(t, filepath.Join(targets[1], "file.txt"), []byte("new"))
	if err := s.ResolveConflict(ids[0], "overwrite"); err == nil {
		t.Fatal("cancelled job was not removed")
	}
	assertNoTemporaryFiles(t, base)
}

func TestCloseCancelsConflictAndRejectsNewJobs(t *testing.T) {
	s, events := testService(t)
	base := t.TempDir()
	source, target := filepath.Join(base, "source", "file.txt"), filepath.Join(base, "target")
	putFile(t, source, []byte("new"))
	putFile(t, filepath.Join(target, "file.txt"), []byte("original"))
	id, err := s.Upload("session", []string{source}, remoteName(target))
	if err != nil {
		t.Fatal(err)
	}
	p := waitTransfer(t, events, id, func(ev event) {
		if _, ok := ev.data.(Conflict); ok {
			s.Close()
		}
	})
	if p.Status != "cancelled" {
		t.Fatalf("progress=%+v", p)
	}
	if _, err := s.Upload("session", []string{source}, remoteName(target)); err == nil {
		t.Fatal("accepted a transfer after closing")
	}
	assertContent(t, filepath.Join(target, "file.txt"), []byte("original"))
}

func TestCancelDownloadDuringCopyPreservesDestination(t *testing.T) {
	s, events := testService(t)
	base := t.TempDir()
	source, target := filepath.Join(base, "source", "large.bin"), filepath.Join(base, "target")
	putFile(t, source, bytes.Repeat([]byte("x"), 8*1024*1024))
	putFile(t, filepath.Join(target, "large.bin"), []byte("original"))
	// Cancel synchronously from the event callback so no scheduling race lets
	// the commit happen before cancellation has been observed.
	originalEmit := s.emit
	s.emit = func(name string, args ...interface{}) {
		if p, ok := args[0].(Progress); ok && p.Status == "running" && p.Transferred > 0 {
			_ = s.Cancel(p.ID)
		}
		originalEmit(name, args...)
	}
	id, err := s.Download("session", remoteName(source), target)
	if err != nil {
		t.Fatal(err)
	}
	p := waitTransfer(t, events, id, func(ev event) {
		if c, ok := ev.data.(Conflict); ok {
			if err := s.ResolveConflict(c.ID, "overwrite"); err != nil {
				t.Fatal(err)
			}
		}
	})
	if p.Status != "cancelled" {
		t.Fatalf("progress=%+v", p)
	}
	assertContent(t, filepath.Join(target, "large.bin"), []byte("original"))
	assertNoTemporaryFiles(t, base)
}

func TestFileOperationsAndSymlinkDeletion(t *testing.T) {
	s, _ := testService(t)
	base := t.TempDir()
	file := filepath.Join(base, "original.txt")
	putFile(t, file, []byte("keep"))
	if err := s.CreateFile("session", remoteName(file)); err == nil {
		t.Fatal("CreateFile overwrote existing file")
	}
	assertContent(t, file, []byte("keep"))
	if err := s.CreateFile("session", remoteName(filepath.Join(base, "new.txt"))); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("session", remoteName(file), remoteName(filepath.Join(base, "new.txt"))); err == nil {
		t.Fatal("Rename overwrote existing file")
	}
	if err := s.Chmod("session", remoteName(file), "0600"); err != nil {
		t.Fatal(err)
	}
	if err := s.Chmod("session", remoteName(file), "0899"); err == nil {
		t.Fatal("accepted invalid mode")
	}
	dir := filepath.Join(base, "directory")
	if err := s.Mkdir("session", remoteName(dir)); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, file, filepath.Join(dir, "link"))
	listing, err := s.List("session", remoteName(base))
	if err != nil {
		t.Fatal(err)
	}
	if !listing.Entries[0].IsDir {
		t.Fatal("directories should sort first")
	}
	if err := s.Delete("session", remoteName(dir)); err != nil {
		t.Fatal(err)
	}
	assertContent(t, file, []byte("keep"))
	if err := s.Delete("session", "/"); err == nil {
		t.Fatal("accepted root deletion")
	}
}

func TestDownloadRejectsLocalSymlinkAndRemoteSymlink(t *testing.T) {
	s, events := testService(t)
	base := t.TempDir()
	remote, local, outside := filepath.Join(base, "remote", "data"), filepath.Join(base, "local"), filepath.Join(base, "outside")
	putFile(t, filepath.Join(remote, "file"), []byte("new"))
	putFile(t, filepath.Join(outside, "file"), []byte("keep"))
	if err := os.Mkdir(local, 0755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(local, "data"))
	id, err := s.Download("session", remoteName(remote), local)
	if err != nil {
		t.Fatal(err)
	}
	if p := waitTransfer(t, events, id, nil); p.Status != "failed" {
		t.Fatalf("symlink destination accepted: %+v", p)
	}
	assertContent(t, filepath.Join(outside, "file"), []byte("keep"))
	link := filepath.Join(base, "remote", "remote-link")
	symlinkOrSkip(t, outside, link)
	id, err = s.Download("session", remoteName(link), local)
	if err != nil {
		t.Fatal(err)
	}
	if p := waitTransfer(t, events, id, nil); p.Status != "failed" {
		t.Fatalf("remote symlink accepted: %+v", p)
	}
}

func TestSafeReplacementRestoresOriginalOnCommitFailure(t *testing.T) {
	base := t.TempDir()
	target, temp := filepath.Join(base, "target"), filepath.Join(base, "temp")
	putFile(t, target, []byte("original"))
	putFile(t, temp, []byte("new"))
	err := replaceSafely(replaceOps{
		lstat: os.Lstat, remove: os.Remove,
		rename: func(from, to string) error {
			if from == temp {
				return errors.New("injected disk error")
			}
			return os.Rename(from, to)
		},
	}, temp, target)
	if err == nil || !strings.Contains(err.Error(), "original restored") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContent(t, target, []byte("original"))
	assertContent(t, temp, []byte("new"))
	assertNoTemporaryFiles(t, base)
}

func TestSafeReplacementRetainsBackupWhenRestoreFails(t *testing.T) {
	base := t.TempDir()
	target, temp := filepath.Join(base, "target"), filepath.Join(base, "temp")
	putFile(t, target, []byte("original"))
	putFile(t, temp, []byte("new"))
	err := replaceSafely(replaceOps{
		lstat: os.Lstat, remove: os.Remove,
		rename: func(from, to string) error {
			if to == target {
				return errors.New("injected disk error")
			}
			return os.Rename(from, to)
		},
	}, temp, target)
	if err == nil || !strings.Contains(err.Error(), "original remains at") {
		t.Fatalf("unexpected error: %v", err)
	}
	backups, err := filepath.Glob(target + ".wails-ssh-backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("original backup missing: %v, %v", backups, err)
	}
	assertContent(t, backups[0], []byte("original"))
}

func TestLocalCommitNeverOverwritesUnapprovedConcurrentFile(t *testing.T) {
	base := t.TempDir()
	putFile(t, filepath.Join(base, "temp"), []byte("new"))
	putFile(t, filepath.Join(base, "target"), []byte("original"))
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := replaceLocal(root, "temp", "target", false); err == nil {
		t.Fatal("replaced an unapproved file")
	}
	assertContent(t, filepath.Join(base, "target"), []byte("original"))
}

func TestUnsafeNamesAndMutationPaths(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../escape", "/absolute", "nested/file", "nested\\file", "bad\x00name"} {
		if safeName(name) {
			t.Errorf("accepted unsafe filename %q", name)
		}
	}
	for _, name := range []string{"", "/", ".", "../..", "/var/../", "x/../../y"} {
		if writablePath(name) == nil {
			t.Errorf("accepted unsafe mutation path %q", name)
		}
	}
}

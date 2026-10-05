package transfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pkg/sftp"
)

func TestUploadRejectsRelativeDestinationBeforeStarting(t *testing.T) {
	var prepares, events atomic.Int32
	s := NewWithDialer(nil, func(string) (ClientDialer, error) {
		prepares.Add(1)
		return nil, errors.New("unexpected transfer preparation")
	}, func(string, ...interface{}) { events.Add(1) })
	t.Cleanup(s.Close)
	for _, target := range []string{".", "assets", "../assets", "~/site", "C:\\site"} {
		id, err := s.Upload("relative", []string{"index.html"}, target)
		if id != "" || err == nil || !strings.Contains(err.Error(), "上传目标必须是绝对路径") {
			t.Fatalf("relative target %q accepted: id=%q err=%v", target, id, err)
		}
	}
	if prepares.Load() != 0 || events.Load() != 0 || len(s.jobs) != 0 {
		t.Fatalf("rejected upload started: prepares=%d events=%d jobs=%d", prepares.Load(), events.Load(), len(s.jobs))
	}
}

// The directory connection resolves alias first. Every worker must retain that
// address even if a deployment changes alias before the first file is opened.
func TestUploadKeepsResolvedDestinationWhenAncestorAliasChanges(t *testing.T) {
	s, events, _ := recoverableService(t)
	base := t.TempDir()
	source := filepath.Join(base, "local", "bundle")
	original, redirected := filepath.Join(base, "original"), filepath.Join(base, "redirected")
	alias := filepath.Join(base, "alias")
	contents := map[string][]byte{"00-index.html": []byte("prepared index")}
	for i := range 8 {
		contents[fmt.Sprintf("assets/file-%02d.js", i)] = []byte(fmt.Sprintf("prepared asset %d", i))
	}
	var total int64
	for name, content := range contents {
		putFile(t, filepath.Join(source, filepath.FromSlash(name)), content)
		putFile(t, filepath.Join(redirected, "target", "bundle", filepath.FromSlash(name)), []byte("keep redirected file"))
		total += int64(len(content))
	}
	putFile(t, filepath.Join(original, "target", "bundle", "00-index.html"), []byte("previous index"))
	symlinkOrSkip(t, original, alias)
	resolved, err := filepath.EvalSymlinks(filepath.Join(original, "target"))
	if err != nil {
		t.Fatal(err)
	}
	expectedPaths := make(map[string]bool, len(contents))
	for name := range contents {
		expectedPaths[path.Join(remoteName(resolved), "bundle", name)] = true
	}
	prepare := s.prepare
	var dials atomic.Int32
	var switchAlias sync.Once
	var switchErr error
	s.prepare = func(id string) (ClientDialer, error) {
		dial, err := prepare(id)
		return func(ctx context.Context) (*sftp.Client, func(), error) {
			if dials.Add(1) > 1 {
				switchAlias.Do(func() {
					switchErr = os.Remove(alias)
					if switchErr == nil {
						switchErr = os.Symlink(redirected, alias)
					}
				})
				if switchErr != nil {
					return nil, nil, switchErr
				}
			}
			return dial(ctx)
		}, err
	}
	const session = "fixed-destination"
	id, err := s.Upload(session, []string{source}, remoteName(filepath.Join(alias, "target")))
	if err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	result := waitTransfer(t, events, id, func(ev event) {
		if p, ok := ev.data.(Progress); ok && p.ID == id && p.FilesTotal > 0 {
			if !expectedPaths[p.Path] || p.Total != total || p.FilesTotal != len(contents) || p.Transferred > p.Total {
				t.Fatalf("progress used a different destination or counted a file twice: %+v", p)
			}
		}
		if c, ok := ev.data.(Conflict); ok && c.ID == id {
			conflicts++
			want := path.Join(remoteName(resolved), "bundle", "00-index.html")
			if c.Target != want {
				t.Fatalf("conflict moved with alias: got %s, want %s", c.Target, want)
			}
			s.targetMu.Lock()
			lock := s.targets[remoteTarget(session, want)]
			locked := lock != nil && len(lock.token) == 1
			s.targetMu.Unlock()
			if !locked {
				t.Fatal("conflict did not hold the actual canonical destination lock")
			}
			if err := s.ResolveConflict(id, "overwrite"); err != nil {
				t.Fatal(err)
			}
		}
	})
	if result.Status != "completed" || result.FilesDone != len(contents) || result.Transferred != total || conflicts != 1 || dials.Load() < 2 {
		t.Fatalf("upload did not complete at its fixed destination: %+v conflicts=%d dials=%d", result, conflicts, dials.Load())
	}
	for name, content := range contents {
		assertContent(t, filepath.Join(original, "target", "bundle", filepath.FromSlash(name)), content)
		assertContent(t, filepath.Join(redirected, "target", "bundle", filepath.FromSlash(name)), []byte("keep redirected file"))
	}
	assertNoTemporaryFiles(t, base)
}

func TestEnsureRemoteDirectoryUsesResolvedParentForMutation(t *testing.T) {
	s, _ := testService(t)
	client, err := s.getClient("directory")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	original, redirected := filepath.Join(base, "original"), filepath.Join(base, "redirected")
	alias := filepath.Join(base, "alias")
	for _, dir := range []string{original, redirected} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	symlinkOrSkip(t, original, alias)
	cache := make(map[string]string)
	parent, err := canonicalRemoteDirectory(client, remoteName(alias), cache, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(redirected, alias); err != nil {
		t.Fatal(err)
	}
	j := newJob(context.Background(), func() {}, Progress{}, nil)
	for _, name := range []string{"existing", "created"} {
		if name == "existing" {
			if err := os.Mkdir(filepath.Join(original, name), 0755); err != nil {
				t.Fatal(err)
			}
			// A non-directory at the redirected address must be irrelevant.
			putFile(t, filepath.Join(redirected, name), []byte("keep redirected file"))
		}
		got, err := s.ensureRemoteDirectory(j, client, path.Join(remoteName(alias), name), cache)
		if err != nil || got != path.Join(parent, name) {
			t.Fatalf("directory %s did not use its resolved parent: got=%s err=%v", name, got, err)
		}
		info, err := os.Stat(filepath.Join(original, name))
		if err != nil || !info.IsDir() {
			t.Fatalf("original directory was not prepared: %v", err)
		}
	}
	assertContent(t, filepath.Join(redirected, "existing"), []byte("keep redirected file"))
	if _, err := os.Stat(filepath.Join(redirected, "created")); !os.IsNotExist(err) {
		t.Fatalf("created directory under changed alias: %v", err)
	}
	symlinkOrSkip(t, redirected, filepath.Join(original, "final-link"))
	if _, err := s.ensureRemoteDirectory(j, client, path.Join(remoteName(alias), "final-link"), cache); err == nil || !strings.Contains(err.Error(), "not a regular directory") {
		t.Fatalf("final destination symlink was accepted: %v", err)
	}
}

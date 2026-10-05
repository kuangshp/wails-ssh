package transfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
)

// A production build commonly replaces every hashed asset after the batch
// has been selected. Queued uploads must use the same prepared batch as index.
func TestUploadPreparedBatchSurvivesBuildDirectoryReplacement(t *testing.T) {
	s, events, fixture := recoverableService(t)
	base := t.TempDir()
	dist, target := filepath.Join(base, "dist"), filepath.Join(base, "remote")
	assets := filepath.Join(dist, "assets")
	const assetCount = 1176
	for i := range assetCount {
		putFile(t, filepath.Join(assets, fmt.Sprintf("index.%04d.js", i)), []byte(fmt.Sprintf("original asset %d", i)))
	}
	putFile(t, filepath.Join(dist, "index.html"), []byte("original index"))
	putFile(t, filepath.Join(dist, "assets.zip"), []byte("original archive"))
	if err := os.Chmod(filepath.Join(dist, "assets.zip"), 0751); err != nil {
		t.Fatal(err)
	}
	archiveInfo, err := os.Stat(filepath.Join(dist, "assets.zip"))
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Join(target, "index.html"), []byte("previous index"))
	prepare := s.prepare
	var rebuild sync.Once
	var rebuildErr error
	s.prepare = func(id string) (ClientDialer, error) {
		dial, err := prepare(id)
		return func(ctx context.Context) (*sftp.Client, func(), error) {
			// The first network request runs after preparation. Simulate Vite
			// emptying dist while all 1178 files are still waiting to upload.
			rebuild.Do(func() {
				rebuildErr = os.RemoveAll(dist)
				if rebuildErr == nil {
					rebuildErr = os.MkdirAll(assets, 0755)
				}
				if rebuildErr == nil {
					rebuildErr = os.WriteFile(filepath.Join(dist, "index.html"), []byte("new build index"), 0644)
				}
			})
			if rebuildErr != nil {
				return nil, nil, rebuildErr
			}
			return dial(ctx)
		}, err
	}
	id, err := s.Upload("rebuilt-source", []string{assets, filepath.Join(dist, "assets.zip"), filepath.Join(dist, "index.html")}, remoteName(target))
	if err != nil {
		t.Fatal(err)
	}
	var conflictSource string
	result := waitTransfer(t, events, id, func(ev event) {
		if c, ok := ev.data.(Conflict); ok {
			conflictSource = c.Source
			if err := s.ResolveConflict(c.ID, "overwrite_all"); err != nil {
				t.Fatal(err)
			}
		}
	})
	if result.Status != "completed" || result.Error != "" || result.FilesDone != assetCount+2 || result.FilesTotal != assetCount+2 {
		t.Fatalf("incomplete prepared batch: %+v", result)
	}
	if conflictSource != filepath.Join(dist, "index.html") {
		t.Fatalf("conflict exposed snapshot path: %s", conflictSource)
	}
	if fixture.dials.Load() > 5 {
		t.Fatalf("connections were not reused: %d", fixture.dials.Load())
	}
	for i := range assetCount {
		assertContent(t, filepath.Join(target, "assets", fmt.Sprintf("index.%04d.js", i)), []byte(fmt.Sprintf("original asset %d", i)))
	}
	assertContent(t, filepath.Join(target, "index.html"), []byte("original index"))
	assertContent(t, filepath.Join(target, "assets.zip"), []byte("original archive"))
	remoteInfo, err := os.Stat(filepath.Join(target, "assets.zip"))
	if err != nil || remoteInfo.Mode().Perm() != archiveInfo.Mode().Perm() {
		t.Fatalf("snapshot changed remote file permissions: %v, %v", remoteInfo, err)
	}
	assertContent(t, filepath.Join(dist, "index.html"), []byte("new build index"))
	assertNoTemporaryFiles(t, base)
}

func TestUploadCanApplyRemoteModeWithoutOwnerRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX owner/group permission bits")
	}
	s, _ := testService(t)
	base := t.TempDir()
	putFile(t, filepath.Join(base, "prepared.bin"), []byte("prepared contents"))
	if err := os.Chmod(filepath.Join(base, "prepared.bin"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	client, err := s.getClient("mode-fixture")
	if err != nil {
		t.Fatal(err)
	}
	j := newJob(context.Background(), func() {}, Progress{ID: "remote-mode", Status: "running"}, nil)
	mode := os.FileMode(0044)
	j.uploadMode = &mode
	target := filepath.Join(base, "target.bin")
	if err := s.uploadPath(j, client, root, "prepared.bin", remoteName(target)); err != nil {
		t.Fatalf("readable snapshot could not apply independent source mode: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("remote mode=%v err=%v", info, err)
	}
	if err := os.Chmod(target, 0600); err != nil {
		t.Fatal(err)
	}
	assertContent(t, target, []byte("prepared contents"))
}

func TestUploadMissingSourceFailsBeforeOpeningRemoteTransport(t *testing.T) {
	s, events, fixture := recoverableService(t)
	base := t.TempDir()
	good, missing, target := filepath.Join(base, "index.html"), filepath.Join(base, "deleted.js"), filepath.Join(base, "remote")
	putFile(t, good, []byte("new index"))
	putFile(t, filepath.Join(target, "index.html"), []byte("keep index"))
	id, err := s.Upload("missing-source", []string{good, missing}, remoteName(target))
	if err != nil {
		t.Fatal(err)
	}
	result := waitTransfer(t, events, id, nil)
	if result.Status != "failed" || !strings.Contains(result.Error, missing) || fixture.dials.Load() != 0 {
		t.Fatalf("preflight failure mutated server or lost source: %+v dials=%d", result, fixture.dials.Load())
	}
	assertContent(t, filepath.Join(target, "index.html"), []byte("keep index"))
	assertNoTemporaryFiles(t, base)
}

func TestCancelUploadWhilePreparingDoesNotOpenRemoteTransport(t *testing.T) {
	s, events, fixture := recoverableService(t)
	base := t.TempDir()
	source, target := filepath.Join(base, "index.html"), filepath.Join(base, "remote")
	putFile(t, source, []byte("index"))
	emit := s.emit
	s.emit = func(name string, args ...interface{}) {
		if p, ok := args[0].(Progress); ok && p.Status == "preparing" {
			_ = s.Cancel(p.ID)
		}
		emit(name, args...)
	}
	id, err := s.Upload("cancel-preparation", []string{source}, remoteName(target))
	if err != nil {
		t.Fatal(err)
	}
	result := waitTransfer(t, events, id, nil)
	if result.Status != "cancelled" || fixture.dials.Load() != 0 {
		t.Fatalf("cancelled preparation reached the server: %+v", result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target was created: %v", err)
	}
}

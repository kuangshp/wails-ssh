package transfer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

func TestDownloadCheckpointRemainsWritableUntilVerified(t *testing.T) {
	for _, mode := range []os.FileMode{0444, 0751} {
		t.Run(fmt.Sprintf("source-%04o", mode), func(t *testing.T) {
			s, events, fixture := recoverableService(t)
			base := t.TempDir()
			source, target := filepath.Join(base, "remote", "sample.bin"), filepath.Join(base, "downloads")
			local := filepath.Join(target, "sample.bin")
			content := bytes.Repeat([]byte("read-only resume 内容\n"), 40000)
			original := []byte("original local file")
			putFile(t, source, content)
			putFile(t, local, original)
			if err := os.Chmod(source, mode); err != nil {
				t.Fatal(err)
			}
			// Windows readonly flags must not prevent test-directory cleanup.
			t.Cleanup(func() { _ = os.Chmod(source, 0600); _ = os.Chmod(local, 0600) })
			var interruptions atomic.Int32
			var firstOffset atomic.Int64
			checkpointErrors := make(chan error, 2)
			emit := s.emit
			s.emit = func(name string, values ...interface{}) {
				if p, ok := values[0].(Progress); ok && p.Status == "running" && p.Transferred >= 128*1024 {
					stage := interruptions.Load()
					interruptCopy := stage == 0 && p.Transferred < p.Total
					interruptBeforeVerification := stage == 1 && p.Transferred == p.Total
					if (interruptCopy || interruptBeforeVerification) && interruptions.CompareAndSwap(stage, stage+1) {
						if stage == 0 {
							firstOffset.Store(p.Transferred)
						}
						checkpointErrors <- inspectDownloadCheckpoint(target, local, original)
						fixture.disconnect()
					}
				}
				emit(name, values...)
			}
			id, err := s.Download("readonly-download", remoteName(source), target)
			if err != nil {
				t.Fatal(err)
			}
			retries, conflicts := 0, 0
			resumed := false
			result := waitTransfer(t, events, id, func(ev event) {
				if conflict, ok := ev.data.(Conflict); ok {
					conflicts++
					if err := s.ResolveConflict(conflict.ID, "overwrite"); err != nil {
						t.Fatal(err)
					}
				}
				if progress, ok := ev.data.(Progress); ok && progress.ID == id {
					if progress.Status == "retrying" {
						retries++
					}
					if retries > 0 && progress.Status == "running" && progress.Transferred >= firstOffset.Load() && progress.Transferred > 0 {
						resumed = true
					}
				}
			})
			if result.Status != "completed" || interruptions.Load() != 2 || retries < 2 || !resumed || conflicts != 1 {
				t.Fatalf("download did not resume both checkpoints: result=%+v interruptions=%d retries=%d resumed=%v conflicts=%d", result, interruptions.Load(), retries, resumed, conflicts)
			}
			for range 2 {
				if err := <-checkpointErrors; err != nil {
					t.Fatal(err)
				}
			}
			assertContent(t, local, content)
			info, err := os.Stat(local)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != mode {
				t.Fatalf("completed file lost source permissions: got %04o, want %04o", info.Mode().Perm(), mode)
			}
			assertNoTemporaryFiles(t, base)
		})
	}
}

func inspectDownloadCheckpoint(directory, destination string, original []byte) error {
	files, err := filepath.Glob(filepath.Join(directory, ".wails-ssh-*.part"))
	if err != nil {
		return err
	}
	if len(files) != 1 {
		return fmt.Errorf("expected one owned checkpoint, got %d", len(files))
	}
	info, err := os.Stat(files[0])
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		return fmt.Errorf("incomplete checkpoint permissions: got %04o, want 0600", info.Mode().Perm())
	}
	file, err := os.OpenFile(files[0], os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("owned checkpoint cannot reopen for resume: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	actual, err := os.ReadFile(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, original) {
		return fmt.Errorf("original destination changed before verification and commit")
	}
	return nil
}

package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// Use real local SFTP transports with a second job held at a conflict. Stopping
// a bulk job must close all four of its workers and discard its queued files,
// while the other job (including one in the same tab) can still finish.
func TestCancelBulkTransferStopsWorkersAndPreservesOtherJob(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		for _, sameSession := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sameSession=%v", direction, sameSession), func(t *testing.T) {
				s, events, fixture := recoverableService(t)
				base := t.TempDir()
				otherSource, otherTarget := filepath.Join(base, "other", "keep.txt"), filepath.Join(base, "other-target")
				putFile(t, otherSource, []byte("independent transfer"))
				putFile(t, filepath.Join(otherTarget, "keep.txt"), []byte("original"))
				otherDirection := "download"
				if direction == "download" {
					otherDirection = "upload"
				}
				otherID := startFixtureTransfer(t, s, "other-session", otherDirection, otherSource, otherTarget)
				waitConflict(t, events, otherID)

				// Hold the new job's four workers at connection setup so even the
				// fastest local transport cannot finish before its peers start.
				prepare := s.prepare
				var jobDials atomic.Int32
				workersReady := make(chan struct{})
				s.prepare = func(id string) (ClientDialer, error) {
					dial, err := prepare(id)
					return func(ctx context.Context) (*sftp.Client, func(), error) {
						client, closeClient, err := dial(ctx)
						if err != nil {
							return client, closeClient, err
						}
						n := jobDials.Add(1)
						if n > 1 && n <= 5 {
							if n == 5 {
								close(workersReady)
							}
							select {
							case <-workersReady:
							case <-ctx.Done():
								closeClient()
								return nil, nil, ctx.Err()
							}
						}
						return client, closeClient, nil
					}, err
				}
				source, target := filepath.Join(base, "bulk"), filepath.Join(base, "target")
				content := bytes.Repeat([]byte("bulk"), 512*1024)
				for i := range 12 {
					putFile(t, filepath.Join(source, fmt.Sprintf("%02d.bin", i)), content)
				}
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
				var cancelled atomic.Bool
				emit := s.emit
				s.emit = func(name string, values ...interface{}) {
					if p, ok := values[0].(Progress); ok && p.ID != otherID && p.Status == "running" && p.Transferred >= 128*1024 && cancelled.CompareAndSwap(false, true) {
						_ = s.Cancel(p.ID)
						_ = s.Cancel(p.ID)
					}
					emit(name, values...)
				}
				session := "cancelled-session"
				if sameSession {
					session = "other-session"
				}
				id := startFixtureTransfer(t, s, session, direction, source, target)
				p := waitTransfer(t, events, id, nil)
				if p.Status != "cancelled" || !cancelled.Load() || p.ActiveFiles != 0 || p.FilesTotal != 12 || p.FilesDone >= 12 {
					t.Fatalf("bulk transfer was not stopped: %+v", p)
				}
				if jobDials.Load() != 5 || fixture.active.Load() != 1 {
					t.Fatalf("cancelled workers remain or queued files opened connections: dials=%d active=%d", jobDials.Load(), fixture.active.Load())
				}
				if err := s.Cancel(id); err != nil {
					t.Fatalf("cancellation after final progress must be idempotent: %v", err)
				}
				assertResetProgressSnapshot(t, p)
				if err := s.ResolveConflict(otherID, "overwrite"); err != nil {
					t.Fatalf("stopping one job affected the other job: %v", err)
				}
				other := waitTransfer(t, events, otherID, nil)
				if other.Status != "completed" || other.ActiveFiles != 0 {
					t.Fatalf("independent job did not finish: %+v", other)
				}
				assertResetProgressSnapshot(t, other)
				assertContent(t, filepath.Join(otherTarget, "keep.txt"), []byte("independent transfer"))
			})
		}
	}
}

func startFixtureTransfer(t *testing.T, s *Service, session, direction, source, target string) string {
	t.Helper()
	var id string
	var err error
	if direction == "upload" {
		id, err = s.Upload(session, []string{source}, remoteName(target))
	} else {
		id, err = s.Download(session, remoteName(source), target)
	}
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertResetProgressSnapshot(t *testing.T, p Progress) {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"activeFiles", "attempt", "delaySeconds"} {
		if value, exists := snapshot[key]; !exists || value != float64(0) {
			t.Fatalf("terminal event cannot clear stale %s: %s", key, data)
		}
	}
	if value, exists := snapshot["error"]; !exists || value != "" {
		t.Fatalf("terminal event cannot clear stale error: %s", data)
	}
}

func TestCancelWaitingDestinationReleasesOnlyItsJob(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			s, events, _ := recoverableService(t)
			s.remoteScope = func(string) (string, error) { return "same-server", nil }
			base := t.TempDir()
			first, second, target := filepath.Join(base, "first", "config"), filepath.Join(base, "second", "config"), filepath.Join(base, "target")
			putFile(t, first, []byte("first"))
			putFile(t, second, []byte("second"))
			putFile(t, filepath.Join(target, "config"), []byte("original"))
			firstID := startFixtureTransfer(t, s, "first", direction, first, target)
			waitConflict(t, events, firstID)
			secondID := startFixtureTransfer(t, s, "second", direction, second, target)
			deadline := time.Now().Add(5 * time.Second)
			for {
				s.targetMu.Lock()
				waiting := false
				for _, lock := range s.targets {
					waiting = waiting || lock.refs > 1
				}
				s.targetMu.Unlock()
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("second job never waited for the first job's destination")
				}
				time.Sleep(time.Millisecond)
			}
			if err := s.Cancel(secondID); err != nil {
				t.Fatal(err)
			}
			p := waitTransfer(t, events, secondID, nil)
			if p.Status != "cancelled" || p.ActiveFiles != 0 {
				t.Fatalf("waiting job failed to stop: %+v", p)
			}
			if err := s.ResolveConflict(firstID, "overwrite"); err != nil {
				t.Fatal(err)
			}
			if p := waitTransfer(t, events, firstID, nil); p.Status != "completed" {
				t.Fatalf("cancelling waiting job affected the lock owner: %+v", p)
			}
			assertContent(t, filepath.Join(target, "config"), []byte("first"))
		})
	}
}

package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

type reconnectFixture struct {
	mu           sync.Mutex
	clients      map[*sftp.Client]func()
	active, peak atomic.Int32
	dials        atomic.Int32
}

func recoverableService(t *testing.T) (*Service, <-chan event, *reconnectFixture) {
	t.Helper()
	f := &reconnectFixture{clients: map[*sftp.Client]func(){}}
	events := make(chan event, 8192)
	dial := func(ctx context.Context) (*sftp.Client, func(), error) {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		serverConn, clientConn := net.Pipe()
		server, err := sftp.NewServer(serverConn)
		if err != nil {
			return nil, nil, err
		}
		go func() { _ = server.Serve() }()
		client, err := sftp.NewClientPipe(clientConn, clientConn)
		if err != nil {
			server.Close()
			return nil, nil, err
		}
		f.dials.Add(1)
		active := f.active.Add(1)
		for {
			old := f.peak.Load()
			if old >= active || f.peak.CompareAndSwap(old, active) {
				break
			}
		}
		var once sync.Once
		closeClient := func() {
			once.Do(func() {
				_ = client.Close()
				_ = server.Close()
				f.active.Add(-1)
				f.mu.Lock()
				delete(f.clients, client)
				f.mu.Unlock()
			})
		}
		f.mu.Lock()
		f.clients[client] = closeClient
		f.mu.Unlock()
		stop := context.AfterFunc(ctx, closeClient)
		return client, func() { stop(); closeClient() }, nil
	}
	s := NewWithDialer(nil, func(string) (ClientDialer, error) { return dial, nil }, func(name string, args ...interface{}) { events <- event{name, args[0]} })
	s.retryDelay = func(int) time.Duration { return time.Millisecond }
	s.progressInterval = 0
	t.Cleanup(func() { s.Close(); f.disconnect() })
	return s, events, f
}

func (f *reconnectFixture) disconnect() {
	f.mu.Lock()
	closeAll := make([]func(), 0, len(f.clients))
	for _, closeClient := range f.clients {
		closeAll = append(closeAll, closeClient)
	}
	f.mu.Unlock()
	for _, closeClient := range closeAll {
		closeClient()
	}
}

func TestInterruptedTransfersResumeOwnedCheckpoint(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			s, events, f := recoverableService(t)
			base := t.TempDir()
			source, target := filepath.Join(base, "source", "sample.bin"), filepath.Join(base, "target")
			content := bytes.Repeat([]byte("checkpoint data 内容\n"), 40000)
			putFile(t, source, content)
			putFile(t, filepath.Join(target, "sample.bin"), []byte("original"))
			var interrupted atomic.Bool
			var checkpoint atomic.Int64
			emit := s.emit
			s.emit = func(name string, values ...interface{}) {
				if p, ok := values[0].(Progress); ok && p.Status == "running" && p.Transferred >= 128*1024 && p.Transferred < p.Total && interrupted.CompareAndSwap(false, true) {
					checkpoint.Store(p.Transferred)
					f.disconnect()
				}
				emit(name, values...)
			}
			var id string
			var err error
			if direction == "upload" {
				id, err = s.Upload("fixture", []string{source}, remoteName(target))
			} else {
				id, err = s.Download("fixture", remoteName(source), target)
			}
			if err != nil {
				t.Fatal(err)
			}
			conflicts, retries, resumed := 0, 0, false
			p := waitTransfer(t, events, id, func(ev event) {
				if c, ok := ev.data.(Conflict); ok {
					conflicts++
					if err := s.ResolveConflict(c.ID, "overwrite"); err != nil {
						t.Fatal(err)
					}
				}
				if p, ok := ev.data.(Progress); ok {
					if p.Status == "retrying" {
						retries++
					}
					if retries > 0 && p.Status == "running" && p.Transferred >= checkpoint.Load() && p.Transferred > 0 {
						resumed = true
					}
				}
			})
			if p.Status != "completed" || conflicts != 1 || retries == 0 || !resumed || !interrupted.Load() {
				t.Fatalf("status=%s conflicts=%d retries=%d resumed=%v", p.Status, conflicts, retries, resumed)
			}
			assertContent(t, filepath.Join(target, "sample.bin"), content)
			assertNoTemporaryFiles(t, base)
		})
	}
}

func TestParallelUploadIsCappedAtFourAndSerializesConflictPolicy(t *testing.T) {
	s, events, f := recoverableService(t)
	base := t.TempDir()
	source, target := filepath.Join(base, "source"), filepath.Join(base, "target")
	for i := range 12 {
		name := fmt.Sprintf("file-%d.txt", i)
		putFile(t, filepath.Join(source, name), bytes.Repeat([]byte("replacement"), 20000))
		putFile(t, filepath.Join(target, "source", name), []byte("original"))
	}
	id, err := s.Upload("fixture", []string{source}, remoteName(target))
	if err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	p := waitTransfer(t, events, id, func(ev event) {
		if c, ok := ev.data.(Conflict); ok {
			conflicts++
			deadline := time.Now().Add(time.Second)
			for f.peak.Load() < 4 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if err := s.ResolveConflict(c.ID, "overwrite_all"); err != nil {
				t.Fatal(err)
			}
		}
	})
	if p.Status != "completed" || conflicts != 1 || f.peak.Load() != 4 {
		t.Fatalf("status=%s conflicts=%d peak=%d", p.Status, conflicts, f.peak.Load())
	}
	assertNoTemporaryFiles(t, base)
}

func TestCancelWhileWaitingToReconnect(t *testing.T) {
	events := make(chan event, 32)
	s := NewWithDialer(nil, func(string) (ClientDialer, error) {
		return func(context.Context) (*sftp.Client, func(), error) {
			return nil, nil, &net.OpError{Op: "dial", Err: errors.New("fixture disconnected")}
		}, nil
	}, func(name string, values ...interface{}) { events <- event{name, values[0]} })
	t.Cleanup(s.Close)
	base := t.TempDir()
	source := filepath.Join(base, "source.txt")
	putFile(t, source, []byte("fixture"))
	id, err := s.Upload("fixture", []string{source}, remoteName(base))
	if err != nil {
		t.Fatal(err)
	}
	p := waitTransfer(t, events, id, func(ev event) {
		if p, ok := ev.data.(Progress); ok && p.Status == "retrying" {
			if err := s.Cancel(id); err != nil {
				t.Fatal(err)
			}
		}
	})
	if p.Status != "cancelled" {
		t.Fatalf("status=%s", p.Status)
	}
	assertResetProgressSnapshot(t, p)
}

func TestResumeUsesUploadSnapshotWhenSourceChangesDuringDisconnect(t *testing.T) {
	s, events, f := recoverableService(t)
	base := t.TempDir()
	source, target := filepath.Join(base, "source.bin"), filepath.Join(base, "target")
	content := bytes.Repeat([]byte("unchanged"), 100000)
	putFile(t, source, content)
	putFile(t, filepath.Join(target, "source.bin"), []byte("keep-original"))
	var dropped atomic.Bool
	emit := s.emit
	s.emit = func(name string, values ...interface{}) {
		if p, ok := values[0].(Progress); ok && p.Status == "running" && p.Transferred >= 128*1024 && dropped.CompareAndSwap(false, true) {
			f.disconnect()
			_ = os.WriteFile(source, []byte("changed while offline"), 0644)
		}
		emit(name, values...)
	}
	id, err := s.Upload("fixture", []string{source}, remoteName(target))
	if err != nil {
		t.Fatal(err)
	}
	p := waitTransfer(t, events, id, func(ev event) {
		if c, ok := ev.data.(Conflict); ok {
			_ = s.ResolveConflict(c.ID, "overwrite")
		}
	})
	if p.Status != "completed" || !dropped.Load() {
		t.Fatalf("status=%s dropped=%v error=%s", p.Status, dropped.Load(), p.Error)
	}
	assertContent(t, filepath.Join(target, "source.bin"), content)
	assertContent(t, source, []byte("changed while offline"))
	assertNoTemporaryFiles(t, base)
}

func TestTransferRetryLimitAndPermanentErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		want    int32
	}{
		{"transport", &net.OpError{Op: "dial", Err: errors.New("offline fixture")}, 61},
		{"permission", os.ErrPermission, 1},
		{"authentication", errors.New("ssh: unable to authenticate"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := make(chan event, 256)
			var calls atomic.Int32
			s := NewWithDialer(nil, func(string) (ClientDialer, error) {
				return func(context.Context) (*sftp.Client, func(), error) { calls.Add(1); return nil, nil, tc.failure }, nil
			}, func(name string, values ...interface{}) { events <- event{name, values[0]} })
			s.retryDelay = func(int) time.Duration { return 0 }
			t.Cleanup(s.Close)
			base := t.TempDir()
			source := filepath.Join(base, "file.txt")
			putFile(t, source, []byte("fixture"))
			id, err := s.Upload("fixture", []string{source}, remoteName(base))
			if err != nil {
				t.Fatal(err)
			}
			p := waitTransfer(t, events, id, nil)
			if p.Status != "failed" || calls.Load() != tc.want {
				t.Fatalf("status=%s dials=%d want=%d", p.Status, calls.Load(), tc.want)
			}
		})
	}
}

func TestCancelActiveTransferKeepsOriginalDestination(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			s, events, _ := recoverableService(t)
			base := t.TempDir()
			source, target := filepath.Join(base, "source.bin"), filepath.Join(base, "target")
			putFile(t, source, bytes.Repeat([]byte("cancel-fixture"), 100000))
			putFile(t, filepath.Join(target, "source.bin"), []byte("original"))
			var cancelled atomic.Bool
			emit := s.emit
			s.emit = func(name string, values ...interface{}) {
				if p, ok := values[0].(Progress); ok && p.Status == "running" && p.Transferred >= 128*1024 && cancelled.CompareAndSwap(false, true) {
					_ = s.Cancel(p.ID)
				}
				emit(name, values...)
			}
			var id string
			var err error
			if direction == "upload" {
				id, err = s.Upload("fixture", []string{source}, remoteName(target))
			} else {
				id, err = s.Download("fixture", remoteName(source), target)
			}
			if err != nil {
				t.Fatal(err)
			}
			p := waitTransfer(t, events, id, func(ev event) {
				if c, ok := ev.data.(Conflict); ok {
					_ = s.ResolveConflict(c.ID, "overwrite")
				}
			})
			if p.Status != "cancelled" || !cancelled.Load() {
				t.Fatalf("status=%s requested=%v", p.Status, cancelled.Load())
			}
			assertContent(t, filepath.Join(target, "source.bin"), []byte("original"))
		})
	}
}

func TestUploadRejectsCollidingSourcesBeforeAnyRemoteMutation(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprintf("directory=%v", directory), func(t *testing.T) {
			s, events, fixture := recoverableService(t)
			base := t.TempDir()
			first, second, target := filepath.Join(base, "a", "config"), filepath.Join(base, "b", "config"), filepath.Join(base, "remote")
			if directory {
				putFile(t, filepath.Join(first, "one.txt"), []byte("first"))
				putFile(t, filepath.Join(second, "two.txt"), []byte("second"))
			} else {
				putFile(t, first, []byte("first"))
				putFile(t, second, []byte("second"))
			}
			id, err := s.Upload("fixture", []string{first, second}, remoteName(target))
			if err != nil {
				t.Fatal(err)
			}
			p := waitTransfer(t, events, id, nil)
			if p.Status != "failed" || fixture.dials.Load() != 0 {
				t.Fatalf("status=%s dials=%d", p.Status, fixture.dials.Load())
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("collision check mutated the remote directory")
			}
		})
	}
}

func TestSourceChangedMidTransferUsesSnapshotOrPreservesDestination(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			s, events, _ := recoverableService(t)
			base := t.TempDir()
			source, target := filepath.Join(base, "sample.bin"), filepath.Join(base, "target")
			content := bytes.Repeat([]byte("original source"), 100000)
			putFile(t, source, content)
			putFile(t, filepath.Join(target, "sample.bin"), []byte("original destination"))
			var changed atomic.Bool
			emit := s.emit
			s.emit = func(name string, values ...interface{}) {
				if p, ok := values[0].(Progress); ok && p.Status == "running" && p.Transferred >= 128*1024 && changed.CompareAndSwap(false, true) {
					_ = os.WriteFile(source, bytes.Repeat([]byte("replacement src"), 100000), 0644)
					if direction == "upload" {
						later := time.Now().Add(3 * time.Second)
						_ = os.Chtimes(source, later, later)
					}
				}
				emit(name, values...)
			}
			var id string
			var err error
			if direction == "upload" {
				id, err = s.Upload("fixture", []string{source}, remoteName(target))
			} else {
				id, err = s.Download("fixture", remoteName(source), target)
			}
			if err != nil {
				t.Fatal(err)
			}
			p := waitTransfer(t, events, id, func(ev event) {
				if c, ok := ev.data.(Conflict); ok {
					_ = s.ResolveConflict(c.ID, "overwrite")
				}
			})
			wantStatus := "failed"
			wantContent := []byte("original destination")
			if direction == "upload" {
				wantStatus, wantContent = "completed", content
			}
			if p.Status != wantStatus || !changed.Load() {
				t.Fatalf("status=%s changed=%v", p.Status, changed.Load())
			}
			assertContent(t, filepath.Join(target, "sample.bin"), wantContent)
			assertNoTemporaryFiles(t, base)
		})
	}
}

func TestLostRenameAcknowledgementPreservesRecoveryPaths(t *testing.T) {
	for _, phase := range []string{"backup", "install"} {
		t.Run(phase, func(t *testing.T) {
			base := t.TempDir()
			target, temp := filepath.Join(base, "target"), filepath.Join(base, "checkpoint")
			putFile(t, target, []byte("original"))
			putFile(t, temp, []byte("replacement"))
			var backup string
			err := replaceSafely(replaceOps{lstat: os.Lstat, remove: os.Remove, rename: func(from, to string) error {
				if from == target {
					backup = to
				}
				if err := os.Rename(from, to); err != nil {
					return err
				}
				if (phase == "backup" && from == target) || (phase == "install" && from == temp) {
					return io.EOF
				}
				return nil
			}}, temp, target)
			if !uncertainCommit(err) || retryable(err) || backup == "" || !strings.Contains(err.Error(), backup) || !strings.Contains(err.Error(), target) {
				t.Fatalf("unsafe ambiguous commit result: %v", err)
			}
			assertContent(t, backup, []byte("original"))
			if phase == "install" {
				assertContent(t, target, []byte("replacement"))
			} else {
				assertContent(t, temp, []byte("replacement"))
			}
		})
	}
}

func TestCancelledAmbiguousCommitStillReportsFailureWithRecoveryPath(t *testing.T) {
	events := make(chan event, 16)
	s := New(func(string) (*sftp.Client, error) { return nil, nil }, func(name string, values ...interface{}) { events <- event{name, values[0]} })
	t.Cleanup(s.Close)
	id, err := s.start("fixture", "upload", "/fixture/target", func(j *job) error {
		j.dial = func(context.Context) (*sftp.Client, func(), error) { return nil, func() {}, nil }
		return s.retry(j, func(*sftp.Client) error {
			j.cancel()
			// A peer worker may have been cancelled before another worker's
			// ambiguous commit is joined into the same batch error.
			return errors.Join(context.Canceled, commitFailure(io.EOF, "/fixture/target", "/fixture/checkpoint", "/fixture/backup"))
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	p := waitTransfer(t, events, id, nil)
	if p.Status != "failed" || !strings.Contains(p.Error, "/fixture/backup") {
		t.Fatalf("commit recovery path hidden by cancellation: %+v", p)
	}
}

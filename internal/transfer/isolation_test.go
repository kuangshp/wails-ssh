package transfer

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func TestManySmallFilesReuseWorkerConnections(t *testing.T) {
	s, events, fixture := recoverableService(t)
	prepare := s.prepare
	s.prepare = func(sessionID string) (ClientDialer, error) {
		dial, err := prepare(sessionID)
		return func(ctx context.Context) (*sftp.Client, func(), error) {
			// A fixed local delay isolates the cost of repeated authentication.
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
			return dial(ctx)
		}, err
	}
	base := t.TempDir()
	source, target := filepath.Join(base, "source"), filepath.Join(base, "remote")
	for i := range 200 {
		putFile(t, filepath.Join(source, fmt.Sprintf("%04d.txt", i)), bytes.Repeat([]byte("s"), 1024))
	}
	started := time.Now()
	id, err := s.Upload("many-files", []string{source}, remoteName(target))
	if err != nil {
		t.Fatal(err)
	}
	p := waitTransfer(t, events, id, nil)
	if p.Status != "completed" {
		t.Fatalf("status=%s error=%s", p.Status, p.Error)
	}
	if fixture.dials.Load() > 5 || fixture.peak.Load() > 4 || p.FilesDone != 200 || p.FilesTotal != 200 || p.Transferred != 200*1024 {
		t.Fatalf("connections or totals regressed: dials=%d peak=%d progress=%+v", fixture.dials.Load(), fixture.peak.Load(), p)
	}
	t.Logf("200 files / 1 KiB / 10ms simulated dial: %s, %d dials, peak %d", time.Since(started), fixture.dials.Load(), fixture.peak.Load())
}

func waitConflict(t *testing.T, events <-chan event, id string) Conflict {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			if c, ok := ev.data.(Conflict); ok && c.ID == id {
				return c
			}
			if p, ok := ev.data.(Progress); ok && p.ID == id && (p.Status == "failed" || p.Status == "completed") {
				t.Fatalf("transfer finished before conflict: %+v", p)
			}
		case <-timer.C:
			t.Fatal("timed out waiting for conflict")
		}
	}
}

func TestOtherSessionCompletesWhileConflictIsUnanswered(t *testing.T) {
	for _, sameSession := range []bool{false, true} {
		for _, direction := range []string{"upload", "download"} {
			t.Run(fmt.Sprintf("sameSession=%v/%s", sameSession, direction), func(t *testing.T) {
				s, events, _ := recoverableService(t)
				base := t.TempDir()
				sourceA, targetA := filepath.Join(base, "a", "config"), filepath.Join(base, "target-a")
				putFile(t, sourceA, []byte("first"))
				putFile(t, filepath.Join(targetA, "config"), []byte("original"))
				var aID string
				var err error
				if direction == "upload" {
					aID, err = s.Upload("session-a", []string{sourceA}, remoteName(targetA))
				} else {
					aID, err = s.Download("session-a", remoteName(sourceA), targetA)
				}
				if err != nil {
					t.Fatal(err)
				}
				conflict := waitConflict(t, events, aID)
				if conflict.SessionID != "session-a" {
					t.Fatalf("conflict lost session identity: %+v", conflict)
				}
				sourceB, targetB := filepath.Join(base, "b", "independent"), filepath.Join(base, "target-b")
				putFile(t, sourceB, []byte("second"))
				if err := os.MkdirAll(targetB, 0755); err != nil {
					t.Fatal(err)
				}
				bSession := "session-b"
				if sameSession {
					bSession = "session-a"
				}
				var bID string
				if direction == "upload" {
					bID, err = s.Download(bSession, remoteName(sourceB), targetB)
				} else {
					bID, err = s.Upload(bSession, []string{sourceB}, remoteName(targetB))
				}
				if err != nil {
					t.Fatal(err)
				}
				if p := waitTransfer(t, events, bID, nil); p.Status != "completed" {
					t.Fatalf("independent job stalled or failed: %+v", p)
				}
				assertContent(t, filepath.Join(targetB, "independent"), []byte("second"))
				if err := s.ResolveConflict(aID, "skip"); err != nil {
					t.Fatalf("A was not still waiting: %v", err)
				}
				if p := waitTransfer(t, events, aID, nil); p.Status != "skipped" {
					t.Fatalf("A status=%s", p.Status)
				}
			})
		}
	}
}

func TestOtherSessionCompletesDuringReconnectBackoff(t *testing.T) {
	s, events, _ := recoverableService(t)
	prepare := s.prepare
	s.prepare = func(id string) (ClientDialer, error) {
		if id == "offline-a" {
			return func(context.Context) (*sftp.Client, func(), error) {
				return nil, nil, &net.OpError{Op: "dial", Err: fmt.Errorf("fixture offline")}
			}, nil
		}
		return prepare(id)
	}
	s.retryDelay = func(int) time.Duration { return time.Hour }
	base := t.TempDir()
	source := filepath.Join(base, "source", "sample")
	putFile(t, source, []byte("data"))
	aID, err := s.Upload("offline-a", []string{source}, remoteName(filepath.Join(base, "a")))
	if err != nil {
		t.Fatal(err)
	}
	for {
		ev := <-events
		if p, ok := ev.data.(Progress); ok && p.ID == aID && p.Status == "retrying" {
			break
		}
	}
	bID, err := s.Upload("healthy-b", []string{source}, remoteName(filepath.Join(base, "b")))
	if err != nil {
		t.Fatal(err)
	}
	if p := waitTransfer(t, events, bID, nil); p.Status != "completed" {
		t.Fatalf("healthy session blocked: %+v", p)
	}
	if err := s.Cancel(aID); err != nil {
		t.Fatal(err)
	}
	if p := waitTransfer(t, events, aID, nil); p.Status != "cancelled" {
		t.Fatalf("offline status=%s", p.Status)
	}
}

func TestSameDestinationLocksAcrossSessionsAndLocalAliases(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			s, events, _ := recoverableService(t)
			s.remoteScope = func(string) (string, error) { return "same-host:22", nil }
			base := t.TempDir()
			first, second, target := filepath.Join(base, "a", "config"), filepath.Join(base, "b", "config"), filepath.Join(base, "target")
			putFile(t, first, []byte("first"))
			putFile(t, second, []byte("second"))
			putFile(t, filepath.Join(target, "config"), []byte("original"))
			aliasParent := filepath.Join(base, "alias")
			symlinkOrSkip(t, base, aliasParent)
			alias := filepath.Join(aliasParent, "target")
			var aID, bID string
			var err error
			if direction == "upload" {
				aID, err = s.Upload("tab-a", []string{first}, remoteName(target))
			} else {
				aID, err = s.Download("tab-a", remoteName(first), target)
			}
			if err != nil {
				t.Fatal(err)
			}
			waitConflict(t, events, aID)
			if direction == "upload" {
				bID, err = s.Upload("tab-b", []string{second}, remoteName(alias))
			} else {
				bID, err = s.Download("tab-b", remoteName(second), alias)
			}
			if err != nil {
				t.Fatal(err)
			}
			// An unrelated destination must finish while the same-target peer waits.
			third := filepath.Join(base, "c", "different")
			putFile(t, third, []byte("independent"))
			cID, err := s.Upload("tab-c", []string{third}, remoteName(target))
			if err != nil {
				t.Fatal(err)
			}
			p := waitTransfer(t, events, cID, func(ev event) {
				if c, ok := ev.data.(Conflict); ok && c.ID == bID {
					t.Fatal("same destination bypassed its conflict lock")
				}
			})
			if p.Status != "completed" {
				t.Fatalf("unrelated destination blocked: %+v", p)
			}
			if err := s.ResolveConflict(aID, "skip"); err != nil {
				t.Fatal(err)
			}
			waitConflict(t, events, bID)
			if err := s.ResolveConflict(bID, "skip"); err != nil {
				t.Fatal(err)
			}
			if p := waitTransfer(t, events, bID, nil); p.Status != "skipped" {
				t.Fatalf("B status=%s", p.Status)
			}
			assertContent(t, filepath.Join(target, "config"), []byte("original"))
		})
	}
}

func TestDifferentHostsDoNotShareDestinationLocks(t *testing.T) {
	s, events, _ := recoverableService(t)
	s.remoteScope = func(id string) (string, error) { return id + ":22", nil }
	base := t.TempDir()
	first, second, target := filepath.Join(base, "a", "config"), filepath.Join(base, "b", "config"), filepath.Join(base, "target")
	putFile(t, first, []byte("first"))
	putFile(t, second, []byte("second"))
	putFile(t, filepath.Join(target, "config"), []byte("original"))
	aID, err := s.Upload("host-a", []string{first}, remoteName(target))
	if err != nil {
		t.Fatal(err)
	}
	waitConflict(t, events, aID)
	bID, err := s.Upload("host-b", []string{second}, remoteName(target))
	if err != nil {
		t.Fatal(err)
	}
	waitConflict(t, events, bID)
	_ = s.ResolveConflict(aID, "skip")
	_ = s.ResolveConflict(bID, "skip")
	statuses := map[string]bool{}
	for len(statuses) < 2 {
		ev := <-events
		if p, ok := ev.data.(Progress); ok && p.Status == "skipped" {
			statuses[p.ID] = true
		}
	}
}

func TestManySmallFileProgressEventsAreThrottled(t *testing.T) {
	s, events, _ := recoverableService(t)
	s.progressInterval = time.Hour
	var count atomic.Int32
	emit := s.emit
	s.emit = func(name string, values ...interface{}) {
		if name == "transfer:progress" {
			count.Add(1)
		}
		emit(name, values...)
	}
	base := t.TempDir()
	source := filepath.Join(base, "source")
	for i := range 300 {
		putFile(t, filepath.Join(source, fmt.Sprintf("%d", i)), []byte("fixture"))
	}
	id, err := s.Upload("bulk", []string{source}, remoteName(filepath.Join(base, "target")))
	if err != nil {
		t.Fatal(err)
	}
	p := waitTransfer(t, events, id, nil)
	if p.Status != "completed" || p.FilesDone != 300 || count.Load() > 5 {
		t.Fatalf("progress flood or wrong final totals: events=%d progress=%+v", count.Load(), p)
	}
}

func TestDirectoryDownloadUsesFourReusableConnections(t *testing.T) {
	s, events, fixture := recoverableService(t)
	base := t.TempDir()
	source, target := filepath.Join(base, "remote"), filepath.Join(base, "downloads")
	for i := range 100 {
		putFile(t, filepath.Join(source, fmt.Sprintf("%03d", i)), bytes.Repeat([]byte("download"), 128))
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	id, err := s.Download("download", remoteName(source), target)
	if err != nil {
		t.Fatal(err)
	}
	p := waitTransfer(t, events, id, nil)
	if p.Status != "completed" || p.FilesDone != 100 || fixture.dials.Load() > 5 || fixture.peak.Load() > 4 {
		t.Fatalf("unexpected download pool: dials=%d peak=%d progress=%+v", fixture.dials.Load(), fixture.peak.Load(), p)
	}
	assertContent(t, filepath.Join(target, "remote", "099"), bytes.Repeat([]byte("download"), 128))
	assertNoTemporaryFiles(t, base)
}

func TestLocalDestinationLockNormalizesCaseAndUnicodeAliases(t *testing.T) {
	for _, platform := range []string{"darwin", "windows"} {
		left := localLockName("/Target/RÉPORT", platform)
		right := localLockName("/target/re\u0301port", platform)
		if left != right {
			t.Fatalf("%s case/Unicode aliases use different locks", platform)
		}
	}
	if localLockName("/target/report", "linux") == localLockName("/target/REPORT", "linux") {
		t.Fatal("case-sensitive platform lost distinct lock keys")
	}
	if localLockName(`C:\target\report. `, "windows") != localLockName(`c:/TARGET/REPORT`, "windows") {
		t.Fatal("Windows trailing-dot aliases use different locks")
	}
}

func TestCaseVariantDownloadsWaitForTheSameLocalDestination(t *testing.T) {
	s, events, _ := recoverableService(t)
	base := t.TempDir()
	target := filepath.Join(base, "target")
	putFile(t, filepath.Join(target, "config"), []byte("original"))
	upper, err := os.Stat(filepath.Join(target, "CONFIG"))
	lower, lowerErr := os.Stat(filepath.Join(target, "config"))
	if err != nil || lowerErr != nil || !os.SameFile(upper, lower) {
		t.Skip("fixture volume is case sensitive")
	}
	first, second := filepath.Join(base, "a", "config"), filepath.Join(base, "b", "CONFIG")
	putFile(t, first, []byte("first"))
	putFile(t, second, []byte("second"))
	aID, err := s.Download("tab-a", remoteName(first), target)
	if err != nil {
		t.Fatal(err)
	}
	waitConflict(t, events, aID)
	bID, err := s.Download("tab-b", remoteName(second), target)
	if err != nil {
		t.Fatal(err)
	}
	third := filepath.Join(base, "c", "unrelated")
	putFile(t, third, []byte("independent"))
	cID, err := s.Download("tab-c", remoteName(third), target)
	if err != nil {
		t.Fatal(err)
	}
	p := waitTransfer(t, events, cID, func(ev event) {
		if c, ok := ev.data.(Conflict); ok && c.ID == bID {
			t.Fatal("case alias bypassed target lock")
		}
	})
	if p.Status != "completed" {
		t.Fatalf("unrelated download failed: %+v", p)
	}
	if err := s.ResolveConflict(aID, "skip"); err != nil {
		t.Fatal(err)
	}
	waitConflict(t, events, bID)
	if err := s.ResolveConflict(bID, "skip"); err != nil {
		t.Fatal(err)
	}
	if p := waitTransfer(t, events, bID, nil); p.Status != "skipped" {
		t.Fatalf("B status=%s", p.Status)
	}
	assertContent(t, filepath.Join(target, "config"), []byte("original"))
}

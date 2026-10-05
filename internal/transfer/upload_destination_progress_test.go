package transfer

import (
	"bytes"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestSubmittedUploadDestinationSurvivesDraftChangesAndRetry(t *testing.T) {
	s, events, fixture := recoverableService(t)
	base := t.TempDir()
	source := filepath.Join(base, "local", "assets")
	firstTarget, nextTarget := filepath.Join(base, "first"), filepath.Join(base, "next")
	content := bytes.Repeat([]byte("original upload"), 100000)
	putFile(t, filepath.Join(source, "index.js"), content)
	putFile(t, filepath.Join(source, "index.css"), []byte("original styles"))
	putFile(t, filepath.Join(base, "other-file"), []byte("next upload"))
	var interrupted atomic.Bool
	emit := s.emit
	s.emit = func(name string, values ...interface{}) {
		if p, ok := values[0].(Progress); ok && p.Status == "running" && p.Transferred >= 128*1024 && p.Transferred < p.Total && interrupted.CompareAndSwap(false, true) {
			fixture.disconnect()
		}
		emit(name, values...)
	}
	sources, destination := []string{source}, remoteName(firstTarget)
	id, err := s.Upload("destination-fixture", sources, destination)
	if err != nil {
		t.Fatal(err)
	}
	// The form and terminal can now navigate and prepare a different batch.
	// Neither the queued source list nor target of this job may reference it.
	sources[0], destination = filepath.Join(base, "other-file"), remoteName(nextTarget)
	states := map[string]bool{}
	result := waitTransfer(t, events, id, func(ev event) {
		if p, ok := ev.data.(Progress); ok && p.ID == id {
			if p.Destination != remoteName(firstTarget) {
				t.Fatalf("job target followed current file/draft: %+v", p)
			}
			states[p.Status] = true
		}
	})
	if result.Status != "completed" || result.FilesDone != 2 || !interrupted.Load() || !states["preparing"] || !states["retrying"] {
		t.Fatalf("upload/retry did not finish at fixed target: %+v, states=%v", result, states)
	}
	assertContent(t, filepath.Join(firstTarget, "assets", "index.js"), content)
	assertContent(t, filepath.Join(firstTarget, "assets", "index.css"), []byte("original styles"))
	if _, err := os.Stat(nextTarget); !os.IsNotExist(err) {
		t.Fatalf("upload reached new form destination %s: %v", destination, err)
	}
}

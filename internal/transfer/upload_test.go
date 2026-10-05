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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

type delayedReply struct {
	data []byte
	due  time.Time
}

// Delay replies without serially sleeping in Write: concurrent requests see
// overlapping round trips, as on a network, rather than a bandwidth throttle.
type delayedSFTPConn struct {
	net.Conn
	delay   time.Duration
	pending chan delayedReply
	done    chan struct{}
	once    sync.Once
}

func newDelayedSFTPConn(conn net.Conn, delay time.Duration) *delayedSFTPConn {
	c := &delayedSFTPConn{Conn: conn, delay: delay, pending: make(chan delayedReply, 128), done: make(chan struct{})}
	go func() {
		for {
			select {
			case <-c.done:
				return
			case reply := <-c.pending:
				timer := time.NewTimer(time.Until(reply.due))
				select {
				case <-c.done:
					timer.Stop()
					return
				case <-timer.C:
				}
				if _, err := c.Conn.Write(reply.data); err != nil {
					_ = c.Close()
					return
				}
			}
		}
	}()
	return c
}

func (c *delayedSFTPConn) Write(p []byte) (int, error) {
	reply := delayedReply{data: append([]byte(nil), p...), due: time.Now().Add(c.delay)}
	select {
	case <-c.done:
		return 0, net.ErrClosed
	case c.pending <- reply:
		return len(p), nil
	}
}

func (c *delayedSFTPConn) Close() error {
	c.once.Do(func() { close(c.done); _ = c.Conn.Close() })
	return nil
}

func memorySFTP(t testing.TB, handlers sftp.Handlers, delay time.Duration) (*sftp.Client, func()) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	server := sftp.NewRequestServer(newDelayedSFTPConn(serverConn, delay), handlers)
	go func() { _ = server.Serve() }()
	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	closeClient := func() { _ = client.Close(); _ = server.Close() }
	t.Cleanup(closeClient)
	return client, closeClient
}

type holeFileWriter struct {
	sftp.FileWriter
	failed     atomic.Bool
	wroteAfter atomic.Bool
	failAt     int64
}

func (h *holeFileWriter) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	w, err := h.FileWriter.Filewrite(r)
	if err != nil {
		return nil, err
	}
	return &holeWriter{WriterAt: w, fixture: h}, nil
}

type holeWriter struct {
	io.WriterAt
	fixture *holeFileWriter
}

func (w *holeWriter) WriteAt(p []byte, off int64) (int, error) {
	h := w.fixture
	if off == h.failAt && h.failed.CompareAndSwap(false, true) {
		return 0, sftp.ErrSSHFxConnectionLost
	}
	n, err := w.WriterAt.WriteAt(p, off)
	if off > h.failAt && n > 0 {
		h.wroteAfter.Store(true)
	}
	return n, err
}

func (w *holeWriter) Close() error {
	if closer, ok := w.WriterAt.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type recordingCommands struct {
	sftp.FileCmder
	removes   atomic.Int32
	truncates atomic.Int32
	truncated atomic.Int64
}

func (c *recordingCommands) Filecmd(r *sftp.Request) error {
	if r.Method == "Remove" {
		c.removes.Add(1)
	}
	if r.Method == "Setstat" && r.AttrFlags().Size {
		c.truncates.Add(1)
		c.truncated.Store(int64(r.Attributes().Size))
	}
	return c.FileCmder.Filecmd(r)
}

func TestUploadPipelineResumesConfirmedPrefixAfterHole(t *testing.T) {
	handlers := sftp.InMemHandler()
	holes := &holeFileWriter{FileWriter: handlers.FilePut, failAt: uploadWindow + 32*1024}
	commands := &recordingCommands{FileCmder: handlers.FileCmd}
	handlers.FilePut, handlers.FileCmd = holes, commands
	client, closeFirst := memorySFTP(t, handlers, 5*time.Millisecond)
	base := t.TempDir()
	content := bytes.Repeat([]byte("nonzero pipeline bytes\n"), 90000)
	putFile(t, filepath.Join(base, "source"), content)
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	s := New(nil, nil)
	j := newJob(context.Background(), func() {}, Progress{ID: "hole", Status: "running"}, nil)
	err = s.uploadPath(j, client, root, "source", "/target")
	if !retryable(err) || !holes.failed.Load() || !holes.wroteAfter.Load() {
		t.Fatalf("did not create an out-of-order write hole: err=%v failed=%v later=%v", err, holes.failed.Load(), holes.wroteAfter.Load())
	}
	checkpoint := j.uploadOffsets["/target"]
	part, err := client.Stat(j.temps["/target"])
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint != uploadWindow || part.Size() <= holes.failAt {
		t.Fatalf("checkpoint=%d remote size=%d; expected a hole beyond a confirmed window", checkpoint, part.Size())
	}
	// A new transport sees the same server filesystem and job-owned checkpoint.
	closeFirst()
	client, _ = memorySFTP(t, handlers, 0)
	if err := s.uploadPath(j, client, root, "source", "/target"); err != nil {
		t.Fatal(err)
	}
	if commands.truncates.Load() != 1 || commands.truncated.Load() != checkpoint {
		t.Fatalf("uncertain window was not truncated: count=%d offset=%d", commands.truncates.Load(), commands.truncated.Load())
	}
	result, err := client.Open("/target")
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	got, err := io.ReadAll(result)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("resumed file contains missing or mixed data: bytes=%d err=%v", len(got), err)
	}
	if commands.removes.Load() != 0 {
		t.Fatalf("successful upload made %d unnecessary REMOVE requests", commands.removes.Load())
	}
}

func TestSmallUploadDoesNotRemoveCommittedCheckpoint(t *testing.T) {
	handlers := sftp.InMemHandler()
	commands := &recordingCommands{FileCmder: handlers.FileCmd}
	handlers.FileCmd = commands
	client, _ := memorySFTP(t, handlers, 0)
	base := t.TempDir()
	putFile(t, filepath.Join(base, "small"), []byte("small file"))
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	j := newJob(context.Background(), func() {}, Progress{ID: "small"}, nil)
	if err := New(nil, nil).uploadPath(j, client, root, "small", "/small"); err != nil {
		t.Fatal(err)
	}
	if commands.removes.Load() != 0 {
		t.Fatalf("successful upload issued %d redundant REMOVE requests", commands.removes.Load())
	}
}

type pausedFileWriter struct {
	sftp.FileWriter
	started chan struct{}
	release chan struct{}
	writes  atomic.Int32
}

func (p *pausedFileWriter) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	w, err := p.FileWriter.Filewrite(r)
	return &pausedWriter{WriterAt: w, fixture: p}, err
}

type pausedWriter struct {
	io.WriterAt
	fixture *pausedFileWriter
}

func (w *pausedWriter) WriteAt(p []byte, off int64) (int, error) {
	if w.fixture.writes.Add(1) == 2 {
		close(w.fixture.started)
	}
	<-w.fixture.release
	return w.WriterAt.WriteAt(p, off)
}

func (w *pausedWriter) Close() error {
	if closer, ok := w.WriterAt.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func TestCancelUploadWithPipelineRequestsInFlight(t *testing.T) {
	handlers := sftp.InMemHandler()
	paused := &pausedFileWriter{FileWriter: handlers.FilePut, started: make(chan struct{}), release: make(chan struct{})}
	handlers.FilePut = paused
	defer close(paused.release)
	client, closeClient := memorySFTP(t, handlers, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := context.AfterFunc(ctx, closeClient)
	defer stop()
	base := t.TempDir()
	putFile(t, filepath.Join(base, "source"), bytes.Repeat([]byte("x"), 2*uploadWindow))
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	j := newJob(ctx, cancel, Progress{}, func(context.Context) (*sftp.Client, func(), error) { return client, closeClient, nil })
	s := New(nil, nil)
	done := make(chan error, 1)
	go func() {
		done <- s.retry(j, func(client *sftp.Client) error { return s.uploadPath(j, client, root, "source", "/target") })
	}()
	select {
	case <-paused.started:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not pipeline multiple requests")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel returned %v", err)
		}
		if j.uploadOffsets["/target"] != 0 || j.completed["/target"] {
			t.Fatal("an unacknowledged window was committed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not interrupt in-flight requests")
	}
}

// Compare the previous 128 KiB copy loop with the pipelined implementation over
// a real local SFTP protocol and 20 ms simulated reply latency. This isolates
// request scheduling; it is not an Internet, SSH encryption, or disk benchmark.
func BenchmarkUploadDataLatency(b *testing.B) {
	content := bytes.Repeat([]byte("x"), 4*1024*1024)
	for _, pipeline := range []bool{false, true} {
		name := "sequential"
		if pipeline {
			name = "pipeline16"
		}
		b.Run(name, func(b *testing.B) {
			client, _ := memorySFTP(b, sftp.InMemHandler(), 20*time.Millisecond)
			s := New(nil, nil)
			b.SetBytes(int64(len(content)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				target, err := client.OpenFile("/benchmark", os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
				if err != nil {
					b.Fatal(err)
				}
				j := newJob(context.Background(), func() {}, Progress{Total: int64(len(content))}, nil)
				b.StartTimer()
				if pipeline {
					err = s.copyUpload(j, target, bytes.NewReader(content), "/benchmark")
				} else {
					err = s.copy(j, target, bytes.NewReader(content))
				}
				b.StopTimer()
				closeErr := target.Close()
				if err != nil || closeErr != nil {
					b.Fatalf("upload=%v close=%v", err, closeErr)
				}
				b.StartTimer()
			}
		})
	}
}

// Both paths transfer one packet per file. The legacy variant adds exactly the
// now-removed cleanup of the part filename after a successful rename.
func BenchmarkSmallUploadLatency(b *testing.B) {
	for _, legacyCleanup := range []bool{true, false} {
		b.Run(fmt.Sprintf("legacy_cleanup=%v", legacyCleanup), func(b *testing.B) {
			client, _ := memorySFTP(b, sftp.InMemHandler(), 20*time.Millisecond)
			base := b.TempDir()
			if err := os.WriteFile(filepath.Join(base, "small"), bytes.Repeat([]byte("s"), 1024), 0644); err != nil {
				b.Fatal(err)
			}
			root, err := os.OpenRoot(base)
			if err != nil {
				b.Fatal(err)
			}
			defer root.Close()
			s := New(nil, nil)
			b.SetBytes(1024)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				remote := fmt.Sprintf("/small-%d", i)
				j := newJob(context.Background(), func() {}, Progress{}, nil)
				if err := s.uploadPath(j, client, root, "small", remote); err != nil {
					b.Fatal(err)
				}
				if legacyCleanup {
					_ = client.Remove(j.temps[remote])
				}
			}
		})
	}
}

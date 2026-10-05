package transfer

import (
	"context"
	"errors"
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

// Pause the server's next response only after the filesystem operation has
// completed. Closing this transport loses an actual SFTP acknowledgement.
type peerAckConn struct {
	net.Conn
	ctx     context.Context
	hold    atomic.Bool
	waiting chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (c *peerAckConn) Write(p []byte) (int, error) {
	if c.hold.Swap(false) {
		close(c.waiting)
		select {
		case <-c.release:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}

func (c *peerAckConn) Close() error {
	c.once.Do(func() { close(c.closed); _ = c.Conn.Close() })
	return nil
}

func peerFailureJob(t *testing.T) (*Service, *job, *sync.Map) {
	t.Helper()
	var connections sync.Map
	dial := func(ctx context.Context) (*sftp.Client, func(), error) {
		serverConn, clientConn := net.Pipe()
		gate := &peerAckConn{Conn: serverConn, ctx: ctx, waiting: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
		server, err := sftp.NewServer(gate)
		if err != nil {
			_ = gate.Close()
			_ = clientConn.Close()
			return nil, nil, err
		}
		go func() { _ = server.Serve() }()
		client, err := sftp.NewClientPipe(clientConn, clientConn)
		if err != nil {
			_ = gate.Close()
			_ = clientConn.Close()
			_ = server.Close()
			return nil, nil, err
		}
		connections.Store(client, gate)
		var once sync.Once
		closeClient := func() {
			once.Do(func() {
				// Close the raw transport before the server's serialized writer.
				_ = gate.Close()
				_ = client.Close()
				_ = server.Close()
			})
		}
		stop := context.AfterFunc(ctx, closeClient)
		cleanup := func() { stop(); closeClient() }
		t.Cleanup(cleanup)
		return client, cleanup, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := newJob(ctx, cancel, Progress{ID: "peer-failure", Status: "running"}, dial)
	s := New(nil, nil)
	s.jobs[j.progress.ID] = j
	t.Cleanup(func() { s.Close(); cancel() })
	return s, j, &connections
}

func peerCommitFile(target, temp string, connections *sync.Map, commitConn chan<- *peerAckConn) transferFile {
	return transferFile{path: target, target: target, size: 11, run: func(child *job, client *sftp.Client) error {
		entry, _ := connections.Load(client)
		gate := entry.(*peerAckConn)
		err := replaceSafely(replaceOps{
			lstat: client.Lstat,
			atomic: func(from, to string) error {
				// This worker has no other requests between the stat and rename.
				gate.hold.Store(true)
				commitConn <- gate
				commitConn <- gate
				return client.PosixRename(from, to)
			},
		}, remoteName(temp), remoteName(target))
		if err == nil {
			child.copied++
			child.completed[target] = true
		}
		return err
	}}
}

func TestPeerFailurePreservesInFlightRenameAcknowledgement(t *testing.T) {
	s, j, connections := peerFailureJob(t)
	base := t.TempDir()
	target, temp := filepath.Join(base, "target"), filepath.Join(base, "checkpoint")
	putFile(t, target, []byte("original"))
	putFile(t, temp, []byte("replacement"))
	commitConn := make(chan *peerAckConn, 2)
	peerContext := make(chan context.Context, 1)
	missing := &os.PathError{Op: "openat", Path: "assets/removed.js", Err: os.ErrNotExist}
	peer := transferFile{path: "missing", target: "missing", run: func(child *job, _ *sftp.Client) error {
		peerContext <- child.ctx
		gate := <-commitConn
		<-gate.waiting
		return missing
	}}
	done := make(chan error, 1)
	go func() {
		done <- s.parallelFiles(j, []transferFile{peer, peerCommitFile(target, temp, connections, commitConn)})
	}()
	var gate *peerAckConn
	select {
	case gate = <-commitConn:
	case <-time.After(3 * time.Second):
		t.Fatal("rename did not start")
	}
	<-gate.waiting
	ctx := <-peerContext
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("peer failure did not stop further work")
	}
	// The soft-stop context is cancelled, but the transport must remain usable
	// until the successful rename acknowledgement reaches its waiting client.
	if gate.ctx.Err() != nil {
		t.Fatal("peer failure cancelled another worker's transport")
	}
	close(gate.release)
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrNotExist) || uncertainCommit(err) {
			t.Fatalf("local failure produced an unrelated commit error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("batch did not finish after rename acknowledgement")
	}
	if j.copied != 1 || j.files[target].Status != "completed" {
		t.Fatalf("acknowledged commit was lost to soft cancellation: copied=%d file=%+v", j.copied, j.files[target])
	}
	assertContent(t, target, []byte("replacement"))
}

func TestExplicitCancelStillClosesCommittingTransport(t *testing.T) {
	s, j, connections := peerFailureJob(t)
	base := t.TempDir()
	target, temp := filepath.Join(base, "target"), filepath.Join(base, "checkpoint")
	putFile(t, target, []byte("original"))
	putFile(t, temp, []byte("replacement"))
	commitConn := make(chan *peerAckConn, 2)
	done := make(chan error, 1)
	go func() {
		done <- s.parallelFiles(j, []transferFile{peerCommitFile(target, temp, connections, commitConn)})
	}()
	var gate *peerAckConn
	select {
	case gate = <-commitConn:
	case <-time.After(3 * time.Second):
		t.Fatal("rename did not start")
	}
	<-gate.waiting
	if err := s.Cancel(j.progress.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("explicit cancellation did not close the network connection")
	}
	select {
	case err := <-done:
		// Unlike a peer's soft stop, explicit cancellation can genuinely lose a
		// commit reply. Its recovery information must not be suppressed.
		if !uncertainCommit(err) {
			t.Fatalf("lost commit acknowledgement was hidden: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("explicit cancellation did not interrupt the pending network request")
	}
}

func TestPeerFailureWakesConflictAndRetryWaits(t *testing.T) {
	s, j, _ := peerFailureJob(t)
	ready := make(chan struct{}, 2)
	var conflictReady, retryReady sync.Once
	s.retryDelay = func(int) time.Duration { return time.Minute }
	s.emit = func(name string, values ...interface{}) {
		if name == "transfer:conflict" {
			conflictReady.Do(func() { ready <- struct{}{} })
		}
		if p, ok := values[0].(Progress); ok && p.Status == "retrying" {
			retryReady.Do(func() { ready <- struct{}{} })
		}
	}
	files := []transferFile{
		{path: "missing", target: "missing", run: func(*job, *sftp.Client) error {
			<-ready
			<-ready
			return os.ErrNotExist
		}},
		{path: "conflict", target: "conflict", run: func(child *job, _ *sftp.Client) error {
			_, err := s.overwrite(child, "source", "destination")
			return err
		}},
		{path: "retry", target: "retry", run: func(*job, *sftp.Client) error { return io.ErrUnexpectedEOF }},
	}
	done := make(chan error, 1)
	go func() { done <- s.parallelFiles(j, files) }()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected the original local error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("peer failure left a conflict or retry backoff waiting")
	}
}

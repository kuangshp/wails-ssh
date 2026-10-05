package transfer

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/sftp"
)

func newJob(ctx context.Context, cancel context.CancelFunc, progress Progress, dial ClientDialer) *job {
	return &job{ctx: ctx, cancel: cancel, progress: progress, dial: dial,
		completed: map[string]bool{}, approved: map[string]bool{}, temps: map[string]string{},
		uploadOffsets:  map[string]int64{},
		sourceVersions: map[string]string{}, files: map[string]Progress{}}
}

func (s *Service) start(sessionID, direction, name string, run func(*job) error) (string, error) {
	dial, err := s.prepare(sessionID)
	if err != nil {
		return "", err
	}
	scope, err := s.remoteScope(sessionID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := newJob(ctx, cancel, Progress{ID: uniqueID(), SessionID: sessionID, Direction: direction, Path: name, Status: "queued"}, dial)
	if direction == "upload" {
		// Path advances through the current files; the submitted destination must
		// remain independent from terminal navigation and subsequent uploads.
		j.progress.Destination = name
	}
	j.scope = scope
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return "", errors.New("transfer service is closed")
	}
	s.jobs[j.progress.ID] = j
	s.mu.Unlock()
	s.emit("transfer:progress", j.progress)
	go func() {
		defer cancel()
		j.progress.Status = "running"
		s.report(j, true)
		err := run(j)
		if err != nil && j.ctx.Err() != nil && !uncertainCommit(err) {
			err = j.ctx.Err()
		}
		switch {
		case errors.Is(err, context.Canceled) && !uncertainCommit(err):
			j.progress.Status, j.progress.Error = "cancelled", ""
		case err != nil:
			j.progress.Status, j.progress.Error = "failed", err.Error()
		case j.copied == 0 && j.skipped > 0:
			j.progress.Status = "skipped"
		default:
			j.progress.Status = "completed"
		}
		j.progress.ActiveFiles, j.progress.Attempt, j.progress.DelaySeconds = 0, 0, 0
		s.mu.Lock()
		delete(s.jobs, j.progress.ID)
		s.mu.Unlock()
		s.report(j, true)
	}()
	return j.progress.ID, nil
}

func (s *Service) report(j *job, force bool) {
	if j.parent != nil {
		parent := j.parent
		parent.mu.Lock()
		previous := parent.files[j.progress.Path]
		parent.files[j.progress.Path] = j.progress
		parent.progress.Transferred += j.progress.Transferred - previous.Transferred
		parent.progress.Total += j.progress.Total - previous.Total
		parent.progress.ActiveFiles += activeFile(j.progress.Status) - activeFile(previous.Status)
		parent.progress.FilesDone += finishedFile(j.progress.Status) - finishedFile(previous.Status)
		parent.progress.FilesSkipped += skippedFile(j.progress.Status) - skippedFile(previous.Status)
		parent.retryingFiles += retryingFile(j.progress.Status) - retryingFile(previous.Status)
		parent.progress.Path = j.progress.Path
		parent.progress.Status, parent.progress.Error = "running", ""
		if parent.retryingFiles > 0 {
			parent.progress.Status = "retrying"
			if j.progress.Status == "retrying" {
				parent.progress.Attempt, parent.progress.DelaySeconds, parent.progress.Error = j.progress.Attempt, j.progress.DelaySeconds, j.progress.Error
			}
		} else {
			parent.progress.Attempt, parent.progress.DelaySeconds = 0, 0
		}
		parent.progress.FilesTotal = len(parent.files)
		// Small files may finish in microseconds. Throttle intermediate per-file
		// events while keeping failures and retry transitions immediate.
		urgent := (j.progress.Status == "retrying" && previous.Status != "retrying") || j.progress.Status == "failed"
		if urgent || (force && len(parent.files) == 1) || time.Since(parent.lastEmit) >= s.progressInterval {
			parent.lastEmit = time.Now()
			s.emit("transfer:progress", parent.progress)
		}
		parent.mu.Unlock()
		return
	}
	if force || time.Since(j.lastEmit) >= s.progressInterval {
		j.lastEmit = time.Now()
		s.emit("transfer:progress", j.progress)
	}
}

func activeFile(status string) int {
	if status == "running" || status == "retrying" {
		return 1
	}
	return 0
}
func finishedFile(status string) int {
	if status == "completed" || status == "skipped" {
		return 1
	}
	return 0
}
func skippedFile(status string) int {
	if status == "skipped" {
		return 1
	}
	return 0
}
func retryingFile(status string) int {
	if status == "retrying" {
		return 1
	}
	return 0
}

type workerConnection struct {
	ctx         context.Context
	dial        ClientDialer
	client      *sftp.Client
	closeClient func()
}

func (w *workerConnection) connect() (*sftp.Client, error) {
	if w.client != nil {
		return w.client, nil
	}
	client, closeClient, err := w.dial(w.ctx)
	if err != nil {
		return nil, err
	}
	w.client, w.closeClient = client, closeClient
	return client, nil
}

func (w *workerConnection) close() {
	if w.closeClient != nil {
		w.closeClient()
	}
	w.client, w.closeClient = nil, nil
}

// Each attempt resumes the same owned part files. Permission, host-key and
// authentication errors are terminal; only transport failures are retried.
func (s *Service) retry(j *job, run func(*sftp.Client) error) error {
	worker := &workerConnection{ctx: j.ctx, dial: j.dial}
	defer worker.close()
	return s.retryOn(j, worker, run)
}

func (s *Service) retryOn(j *job, worker *workerConnection, run func(*sftp.Client) error) error {
	for attempt := 0; ; attempt++ {
		if err := j.ctx.Err(); err != nil {
			return err
		}
		client, err := worker.connect()
		if err == nil {
			j.progress.Status, j.progress.Error, j.progress.Attempt, j.progress.DelaySeconds = "running", "", 0, 0
			s.report(j, true)
			err = run(client)
		}
		if err != nil {
			worker.close()
		}
		// A successful run may have committed its file while a peer requested a
		// soft stop. Preserve that acknowledged success in per-file accounting.
		if err != nil && j.ctx.Err() != nil && !uncertainCommit(err) {
			return j.ctx.Err()
		}
		if err == nil || !retryable(err) || attempt >= 60 {
			return err
		}
		delay := s.retryDelay(attempt + 1)
		j.progress.Status, j.progress.Error, j.progress.Attempt, j.progress.DelaySeconds = "retrying", err.Error(), attempt+1, int(delay/time.Second)
		s.report(j, true)
		timer := time.NewTimer(delay)
		select {
		case <-j.ctx.Done():
			timer.Stop()
			return j.ctx.Err()
		case <-timer.C:
		}
	}
}

func retryable(err error) bool {
	if uncertainCommit(err) {
		return false
	}
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var network net.Error
	if errors.As(err, &network) {
		return true
	}
	// pkg/sftp represents SSH_FX_CONNECTION_LOST with a StatusError.
	var status *sftp.StatusError
	if errors.As(err, &status) {
		return status.Code == 6 || status.Code == 7
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection lost") || strings.Contains(message, "connection reset") || strings.Contains(message, "unexpected eof")
}

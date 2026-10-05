// Package transfer implements SFTP browsing and cancellable file transfers.
package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"wails-ssh/internal/model"
)

type Progress struct {
	ID           string `json:"id"`
	SessionID    string `json:"sessionId"`
	Direction    string `json:"direction"`
	Path         string `json:"path"`
	Destination  string `json:"destination,omitempty"`
	Status       string `json:"status"`
	Transferred  int64  `json:"transferred"`
	Total        int64  `json:"total"`
	Error        string `json:"error"`
	Attempt      int    `json:"attempt"`
	DelaySeconds int    `json:"delaySeconds"`
	ActiveFiles  int    `json:"activeFiles"`
	FilesTotal   int    `json:"filesTotal,omitempty"`
	FilesDone    int    `json:"filesDone,omitempty"`
	FilesSkipped int    `json:"filesSkipped,omitempty"`
}

type ClientDialer func(context.Context) (*sftp.Client, func(), error)

type Conflict struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Source    string `json:"source"`
	Target    string `json:"target"`
}

type Service struct {
	getClient        func(string) (*sftp.Client, error)
	prepare          func(string) (ClientDialer, error)
	retryDelay       func(int) time.Duration
	progressInterval time.Duration
	emit             func(string, ...interface{})
	mu               sync.Mutex
	jobs             map[string]*job
	closed           bool
	remoteScope      func(string) (string, error)
	targetMu         sync.Mutex
	targets          map[string]*targetLock
}

type job struct {
	ctx            context.Context
	cancel         context.CancelFunc
	progress       Progress
	mu             sync.Mutex
	pending        chan string
	policy         string
	copied         int
	skipped        int
	lastEmit       time.Time
	conflictMu     sync.Mutex
	parent         *job
	dial           ClientDialer
	completed      map[string]bool
	approved       map[string]bool
	temps          map[string]string
	uploadOffsets  map[string]int64
	sourceVersions map[string]string
	sourcePath     string
	uploadMode     *os.FileMode
	files          map[string]Progress
	scope          string
	retryingFiles  int
}

func New(getClient func(string) (*sftp.Client, error), emit func(string, ...interface{})) *Service {
	if emit == nil {
		emit = func(string, ...interface{}) {}
	}
	return NewWithDialer(getClient, func(id string) (ClientDialer, error) {
		return func(context.Context) (*sftp.Client, func(), error) {
			client, err := getClient(id)
			return client, func() {}, err
		}, nil
	}, emit)
}

func NewWithDialer(getClient func(string) (*sftp.Client, error), prepare func(string) (ClientDialer, error), emit func(string, ...interface{}), scope ...func(string) (string, error)) *Service {
	if emit == nil {
		emit = func(string, ...interface{}) {}
	}
	getScope := func(id string) (string, error) { return id, nil }
	if len(scope) > 0 && scope[0] != nil {
		getScope = scope[0]
	}
	return &Service{getClient: getClient, prepare: prepare, emit: emit, remoteScope: getScope, jobs: make(map[string]*job), targets: make(map[string]*targetLock), progressInterval: 100 * time.Millisecond, retryDelay: func(attempt int) time.Duration { return time.Duration(min(attempt, 5)) * 2 * time.Second }}
}

func uniqueID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

func (s *Service) List(sessionID, dir string) (model.Directory, error) {
	client, err := s.getClient(sessionID)
	if err != nil {
		return model.Directory{}, err
	}
	if strings.TrimSpace(dir) == "" {
		dir, err = client.Getwd()
		if err != nil {
			return model.Directory{}, err
		}
	}
	dir, err = client.RealPath(dir)
	if err != nil {
		return model.Directory{}, err
	}
	entries, err := client.ReadDir(dir)
	if err != nil {
		return model.Directory{}, err
	}
	result := model.Directory{Path: dir, Entries: make([]model.RemoteEntry, 0, len(entries))}
	for _, entry := range entries {
		if !safeName(entry.Name()) {
			continue
		}
		result.Entries = append(result.Entries, model.RemoteEntry{
			Name: entry.Name(), Path: path.Join(dir, entry.Name()),
			IsDir: entry.IsDir(), IsSymlink: entry.Mode()&os.ModeSymlink != 0,
			Size: entry.Size(), ModifiedAt: entry.ModTime().UTC().Format(time.RFC3339),
			Mode: fmt.Sprintf("%04o", permissionBits(entry.Mode())),
		})
	}
	sort.SliceStable(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return result, nil
}

func (s *Service) Mkdir(sessionID, name string) error {
	if err := writablePath(name); err != nil {
		return err
	}
	client, err := s.getClient(sessionID)
	if err != nil {
		return err
	}
	return client.Mkdir(name)
}

func (s *Service) CreateFile(sessionID, name string) error {
	if err := writablePath(name); err != nil {
		return err
	}
	client, err := s.getClient(sessionID)
	if err != nil {
		return err
	}
	file, err := client.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	return file.Close()
}

func (s *Service) Rename(sessionID, source, target string) error {
	if err := writablePath(source); err != nil {
		return err
	}
	if err := writablePath(target); err != nil {
		return err
	}
	client, err := s.getClient(sessionID)
	if err != nil {
		return err
	}
	if path.Clean(source) == path.Clean(target) {
		return nil
	}
	if _, err = client.Lstat(target); err == nil {
		return fmt.Errorf("destination already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	// SFTP v3 Rename must fail if the destination exists.
	return client.Rename(source, target)
}

func (s *Service) Chmod(sessionID, name, mode string) error {
	if err := writablePath(name); err != nil {
		return err
	}
	if len(mode) < 3 || len(mode) > 4 || strings.IndexFunc(mode, func(r rune) bool { return r < '0' || r > '7' }) >= 0 {
		return errors.New("permissions must contain 3 or 4 octal digits, for example 0644")
	}
	bits, err := strconv.ParseUint(mode, 8, 12)
	if err != nil {
		return err
	}
	client, err := s.getClient(sessionID)
	if err != nil {
		return err
	}
	info, err := client.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("changing symbolic-link permissions is not supported")
	}
	perms := os.FileMode(bits & 0777)
	if bits&04000 != 0 {
		perms |= os.ModeSetuid
	}
	if bits&02000 != 0 {
		perms |= os.ModeSetgid
	}
	if bits&01000 != 0 {
		perms |= os.ModeSticky
	}
	return client.Chmod(name, perms)
}

func (s *Service) Delete(sessionID, name string) error {
	if err := writablePath(name); err != nil {
		return err
	}
	client, err := s.getClient(sessionID)
	if err != nil {
		return err
	}
	return removeRemote(client, path.Clean(name))
}

func removeRemote(client *sftp.Client, name string) error {
	info, err := client.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return client.Remove(name)
	}
	entries, err := client.ReadDir(name)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !safeName(entry.Name()) {
			return fmt.Errorf("unsafe remote filename: %q", entry.Name())
		}
		if err := removeRemote(client, path.Join(name, entry.Name())); err != nil {
			return err
		}
	}
	return client.RemoveDirectory(name)
}

func (s *Service) Cancel(id string) error {
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		// A progress event can finish the job while the user clicks Stop.
		// Cancelling an already finished job is an idempotent success.
		return nil
	}
	j.cancel()
	return nil
}

// CancelSession wakes conflict prompts as well as active copies when their tab
// or SSH transport is closed. It never closes the shared SFTP client itself.
func (s *Service) CancelSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.progress.SessionID == sessionID {
			j.cancel()
		}
	}
}

func (s *Service) ResolveConflict(id, choice string) error {
	switch choice {
	case "overwrite", "skip", "overwrite_all", "skip_all", "cancel":
	default:
		return errors.New("invalid conflict resolution")
	}
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		return errors.New("transfer is no longer active")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.pending == nil {
		return errors.New("transfer has no pending conflict")
	}
	select {
	case j.pending <- choice:
		return nil
	default:
		return errors.New("conflict already resolved")
	}
}

// Close cancels transfers; the owning SSH service closes clients to unblock any network I/O.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, j := range s.jobs {
		j.cancel()
	}
}

func writablePath(name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsRune(name, 0) {
		return errors.New("a nonempty path is required")
	}
	clean := path.Clean(name)
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return errors.New("parent traversal is not allowed in a mutation path")
		}
	}
	if clean == "/" || clean == "." || clean == ".." {
		return errors.New("refusing to modify a filesystem root or working directory")
	}
	return nil
}

func safeName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

func permissionBits(mode os.FileMode) uint32 {
	bits := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		bits |= 04000
	}
	if mode&os.ModeSetgid != 0 {
		bits |= 02000
	}
	if mode&os.ModeSticky != 0 {
		bits |= 01000
	}
	return bits
}

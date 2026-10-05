package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"wails-ssh/internal/sshclient"
	"wails-ssh/internal/transfer"
)

type runResult struct {
	Run                 int                `json:"run"`
	Config              config             `json:"config"`
	GoVersion           string             `json:"go_version"`
	Platform            string             `json:"platform"`
	GOMAXPROCS          int                `json:"gomaxprocs"`
	ReplyDelayMS        float64            `json:"sftp_reply_delay_ms"`
	TotalBytes          int64              `json:"total_bytes"`
	WallSeconds         float64            `json:"wall_seconds"`
	EffectiveMiBPS      float64            `json:"effective_mib_per_second"`
	SourceSetupSeconds  float64            `json:"source_setup_seconds"`
	VerificationSeconds float64            `json:"verification_seconds"`
	Runtime             runtimeResult      `json:"runtime"`
	ProgressEvents      int64              `json:"progress_events"`
	SSHConnectionsPeak  int64              `json:"ssh_connections_peak"`
	SSHConnectionsTotal int64              `json:"ssh_connections_total"`
	Sessions            []sessionResult    `json:"session_results"`
	Verification        verificationResult `json:"verification"`
	Error               string             `json:"error,omitempty"`
}

type sessionResult struct {
	Session                   int     `json:"session"`
	Status                    string  `json:"status"`
	PrepareToFirstDialSeconds float64 `json:"prepare_to_first_dial_seconds"`
	TransferDialAttempts      int     `json:"transfer_dial_attempts"`
	FilesDone                 int     `json:"files_done"`
	FilesTotal                int     `json:"files_total"`
	Transferred               int64   `json:"transferred_bytes"`
	Error                     string  `json:"error,omitempty"`
}

type sessionTiming struct {
	index     int
	preparing time.Time
	firstDial time.Time
	dials     int
	complete  chan transfer.Progress
}

type progressCollector struct {
	mu       sync.Mutex
	sessions map[string]*sessionTiming
	events   atomic.Int64
}

func (c *progressCollector) emit(name string, args ...interface{}) {
	if name != "transfer:progress" || len(args) == 0 {
		return
	}
	p, ok := args[0].(transfer.Progress)
	if !ok {
		return
	}
	c.events.Add(1)
	c.mu.Lock()
	session := c.sessions[p.SessionID]
	if session != nil && p.Status == "preparing" && session.preparing.IsZero() {
		session.preparing = time.Now()
	}
	c.mu.Unlock()
	if session != nil && (p.Status == "completed" || p.Status == "failed" || p.Status == "cancelled" || p.Status == "skipped") {
		select {
		case session.complete <- p:
		default:
		}
	}
}

func (c *progressCollector) dial(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if session := c.sessions[id]; session != nil {
		if session.firstDial.IsZero() {
			session.firstDial = time.Now()
		}
		session.dials++
	}
}

type expectedFile struct {
	name   string
	digest string
}

type fileVerification struct {
	Session        int    `json:"session"`
	Path           string `json:"path"`
	SHA256         string `json:"sha256,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	Match          bool   `json:"match"`
	Error          string `json:"error,omitempty"`
}

type verificationResult struct {
	ExpectedFiles int                `json:"expected_files"`
	CheckedFiles  int                `json:"checked_files"`
	MatchedFiles  int                `json:"matched_files"`
	AllMatched    bool               `json:"all_matched"`
	Files         []fileVerification `json:"files"`
}

func benchmark(parent context.Context, cfg config, run int) (result runResult, resultErr error) {
	result = runResult{Run: run, Config: cfg, GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		GOMAXPROCS: runtime.GOMAXPROCS(0), ReplyDelayMS: float64(cfg.Delay) / float64(time.Millisecond), TotalBytes: int64(cfg.Files) * cfg.Size * int64(cfg.Sessions)}
	ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
	defer cancel()
	root, err := os.MkdirTemp("", "wails-transferbench-")
	if err != nil {
		return result, err
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove benchmark files: %w", err))
		}
	}()
	source, remote := filepath.Join(root, "payload"), filepath.Join(root, "server")
	for _, dir := range []string{source, remote} {
		if err := os.Mkdir(dir, 0700); err != nil {
			return result, err
		}
	}
	setupStarted := time.Now()
	manifest, err := generateSource(ctx, source, cfg)
	result.SourceSetupSeconds = time.Since(setupStarted).Seconds()
	if err != nil {
		return result, err
	}
	server, err := startServer(remote, cfg.Delay)
	if err != nil {
		return result, err
	}
	defer server.Close()
	manager := sshclient.New(filepath.Join(root, "known_hosts"), nil)
	defer manager.Close()
	profile := server.profile(0)
	if err := manager.TrustHost(profile.Host, profile.Port, string(ssh.MarshalAuthorizedKey(server.key.PublicKey()))); err != nil {
		return result, err
	}
	collector := &progressCollector{sessions: make(map[string]*sessionTiming)}
	service := transfer.NewWithDialer(manager.SFTP, func(id string) (transfer.ClientDialer, error) {
		dial, err := manager.TransferDialer(id)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context) (*sftp.Client, func(), error) {
			collector.dial(id)
			return dial(ctx)
		}, nil
	}, collector.emit, manager.TransferScope)
	defer service.Close()
	stopOnTimeout := context.AfterFunc(ctx, func() { service.Close(); manager.Close(); server.Close() })
	defer stopOnTimeout()

	// GC before the measured interval makes repeated runs comparable. Source
	// generation, SHA verification and JSON serialization are outside this window.
	runtime.GC()
	stopSampling := sampleRuntime()
	started := time.Now()
	completed := make(chan sessionResult, cfg.Sessions)
	for index := range cfg.Sessions {
		go func() {
			sr := sessionResult{Session: index + 1, Status: "failed", PrepareToFirstDialSeconds: -1}
			defer func() { completed <- sr }()
			connection, err := manager.Connect(server.profile(index), server.secret, 80, 24)
			if err != nil {
				sr.Error = err.Error()
				return
			}
			timing := &sessionTiming{index: index, complete: make(chan transfer.Progress, 1)}
			collector.mu.Lock()
			collector.sessions[connection.ID] = timing
			collector.mu.Unlock()
			destination := filepath.ToSlash(filepath.Join(remote, fmt.Sprintf("session-%04d", index+1)))
			if runtime.GOOS == "windows" {
				destination = "/" + destination
			}
			if _, err := service.Upload(connection.ID, []string{source}, destination); err != nil {
				sr.Error = err.Error()
				return
			}
			select {
			case progress := <-timing.complete:
				sr.Status, sr.Error = progress.Status, progress.Error
				sr.FilesDone, sr.FilesTotal, sr.Transferred = progress.FilesDone, progress.FilesTotal, progress.Transferred
			case <-ctx.Done():
				sr.Error = ctx.Err().Error()
			}
			collector.mu.Lock()
			sr.TransferDialAttempts = timing.dials
			if !timing.preparing.IsZero() && !timing.firstDial.IsZero() {
				sr.PrepareToFirstDialSeconds = timing.firstDial.Sub(timing.preparing).Seconds()
			}
			collector.mu.Unlock()
		}()
	}
	result.Sessions = make([]sessionResult, cfg.Sessions)
	for range cfg.Sessions {
		session := <-completed
		result.Sessions[session.Session-1] = session
		if session.Status != "completed" {
			resultErr = errors.Join(resultErr, fmt.Errorf("session %d %s: %s", session.Session, session.Status, session.Error))
		}
	}
	result.WallSeconds = time.Since(started).Seconds()
	result.Runtime = stopSampling()
	if resultErr == nil {
		result.EffectiveMiBPS = float64(result.TotalBytes) / (1024 * 1024) / result.WallSeconds
	}
	result.ProgressEvents = collector.events.Load()
	result.SSHConnectionsPeak, result.SSHConnectionsTotal = server.peak.Load(), server.total.Load()
	service.Close()
	manager.Close()
	server.Close()
	verificationStarted := time.Now()
	result.Verification = verifyFiles(ctx, remote, manifest, cfg.Sessions)
	result.VerificationSeconds = time.Since(verificationStarted).Seconds()
	if !result.Verification.AllMatched {
		resultErr = errors.Join(resultErr, fmt.Errorf("SHA-256 verification passed for %d/%d files", result.Verification.MatchedFiles, result.Verification.ExpectedFiles))
	}
	return result, resultErr
}

func generateSource(ctx context.Context, directory string, cfg config) ([]expectedFile, error) {
	manifest := make([]expectedFile, 0, cfg.Files)
	buffer := make([]byte, 128*1024)
	for index := range cfg.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := fmt.Sprintf("file-%08d.bin", index)
		file, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		// Fixed per-file seeds make contents repeatable and distinguish every file;
		// streaming avoids keeping a large payload in the process heap.
		source := mathrand.New(mathrand.NewSource(int64(index) + 112358))
		_, copyErr := io.CopyBuffer(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx, source}, cfg.Size), buffer)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return nil, err
		}
		manifest = append(manifest, expectedFile{name: name, digest: hex.EncodeToString(hash.Sum(nil))})
	}
	return manifest, nil
}

func verifyFiles(ctx context.Context, remote string, manifest []expectedFile, sessions int) verificationResult {
	result := verificationResult{ExpectedFiles: len(manifest) * sessions, Files: make([]fileVerification, 0, len(manifest)*sessions)}
	buffer := make([]byte, 128*1024)
	for session := 1; session <= sessions; session++ {
		for _, expected := range manifest {
			if ctx.Err() != nil {
				return result
			}
			name := filepath.Join(fmt.Sprintf("session-%04d", session), "payload", expected.name)
			verification := fileVerification{Session: session, Path: filepath.ToSlash(name)}
			file, err := os.Open(filepath.Join(remote, name))
			if err == nil {
				hash := sha256.New()
				_, copyErr := io.CopyBuffer(hash, contextReader{ctx, file}, buffer)
				err = errors.Join(copyErr, file.Close())
				if err == nil {
					verification.SHA256 = hex.EncodeToString(hash.Sum(nil))
					verification.Match = verification.SHA256 == expected.digest
				}
			}
			if err != nil {
				verification.Error = err.Error()
			}
			if verification.Match {
				result.MatchedFiles++
			} else {
				verification.ExpectedSHA256 = expected.digest
			}
			result.CheckedFiles++
			result.Files = append(result.Files, verification)
		}
	}
	result.AllMatched = result.MatchedFiles == result.ExpectedFiles
	return result
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}

type runtimeResult struct {
	Scope                   string  `json:"scope"`
	SampleIntervalMS        int     `json:"sample_interval_ms"`
	BaselineHeapBytes       uint64  `json:"baseline_heap_bytes"`
	PeakHeapBytes           uint64  `json:"sampled_peak_heap_bytes"`
	EndHeapBytes            uint64  `json:"end_heap_bytes"`
	BaselineGoroutines      int     `json:"baseline_goroutines"`
	PeakGoroutines          int     `json:"sampled_peak_goroutines"`
	EndGoroutines           int     `json:"end_goroutines"`
	TotalAllocBytes         uint64  `json:"total_alloc_bytes"`
	GCCount                 uint32  `json:"gc_count"`
	GCPauseSeconds          float64 `json:"gc_pause_seconds"`
	AfterCleanupGCHeapBytes uint64  `json:"after_cleanup_gc_heap_bytes"`
	AfterCleanupGoroutines  int     `json:"after_cleanup_goroutines"`
}

func sampleRuntime() func() runtimeResult {
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	result := runtimeResult{Scope: "production transfer backend + loopback SSH/SFTP server; excludes UI, source generation and final SHA verification", SampleIntervalMS: 20,
		BaselineHeapBytes: baseline.HeapAlloc, PeakHeapBytes: baseline.HeapAlloc, BaselineGoroutines: runtime.NumGoroutine()}
	result.PeakGoroutines = result.BaselineGoroutines
	stop, finished := make(chan struct{}), make(chan runtimeResult, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		sample := func() runtime.MemStats {
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			result.PeakHeapBytes = max(result.PeakHeapBytes, stats.HeapAlloc)
			result.PeakGoroutines = max(result.PeakGoroutines, runtime.NumGoroutine())
			return stats
		}
		for {
			select {
			case <-ticker.C:
				sample()
			case <-stop:
				stats := sample()
				result.EndHeapBytes, result.EndGoroutines = stats.HeapAlloc, runtime.NumGoroutine()
				result.TotalAllocBytes = stats.TotalAlloc - baseline.TotalAlloc
				result.GCCount = stats.NumGC - baseline.NumGC
				result.GCPauseSeconds = float64(stats.PauseTotalNs-baseline.PauseTotalNs) / float64(time.Second)
				finished <- result
				return
			}
		}
	}()
	return func() runtimeResult { close(stop); return <-finished }
}

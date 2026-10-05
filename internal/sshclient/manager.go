// Package sshclient owns verified SSH connections, PTYs, and their SFTP clients.
package sshclient

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"wails-ssh/internal/model"
)

const connectionTimeout = 12 * time.Second

// Manager keeps each tab's SSH and SFTP channels on the same authenticated connection.
// Close also cancels connections that are still dialing or authenticating.
type Manager struct {
	mu             sync.RWMutex
	sessions       map[string]*managedSession
	closed         bool
	ctx            context.Context
	cancel         context.CancelFunc
	knownMu        sync.Mutex
	knownHostsPath string
	emit           func(string, ...interface{})
	transferMu     sync.Mutex
	transferStarts map[string]*handshakeGate
}

type managedSession struct {
	mu                        sync.Mutex
	writeMu                   sync.Mutex
	resizeMu                  sync.Mutex
	client                    *ssh.Client
	sftp                      *sftp.Client
	sftpErr                   error
	pty                       *ssh.Session
	stdin                     io.WriteCloser
	cols, rows                int
	starting, started, closed bool
	done                      chan struct{}
	once                      sync.Once
	output                    *terminalWriter
	closeError                string
	profile                   model.Profile
	secret                    string
}

// New does not touch the host-key file until a connection or explicit trust request.
func New(knownHostsPath string, emit func(string, ...interface{})) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	if emit == nil {
		emit = func(string, ...interface{}) {}
	}
	return &Manager{sessions: make(map[string]*managedSession), ctx: ctx, cancel: cancel, knownHostsPath: knownHostsPath, emit: emit}
}

func (m *Manager) Connect(profile model.Profile, secret string, cols, rows int) (model.Connection, error) {
	var result model.Connection
	host, err := validHost(profile.Host, profile.Port)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(profile.Username) == "" {
		return result, errors.New("SSH 用户名不能为空")
	}
	if err := validSize(cols, rows); err != nil {
		return result, err
	}
	if m.ctx.Err() != nil {
		return result, errors.New("SSH 管理器已关闭")
	}
	auth, err := authentication(profile, secret)
	if err != nil {
		return result, err
	}
	callback, err := m.hostKeyCallback(host, profile.Port)
	if err != nil {
		return result, err
	}
	address := net.JoinHostPort(host, strconv.Itoa(profile.Port))
	ctx, cancel := context.WithTimeout(m.ctx, connectionTimeout)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return result, fmt.Errorf("无法连接 %s: %w", address, err)
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stopCancel()
	deadline, _ := ctx.Deadline()
	_ = raw.SetDeadline(deadline)
	conn, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{
		User: profile.Username, Auth: auth, HostKeyCallback: callback, Timeout: connectionTimeout,
	})
	if err != nil {
		_ = raw.Close()
		return result, fmt.Errorf("SSH 连接失败: %w", err)
	}
	client := ssh.NewClient(conn, channels, requests)
	owned := false
	defer func() {
		if !owned {
			_ = client.Close()
		}
	}()
	files, filesErr := sftp.NewClient(client)
	home := "."
	if filesErr == nil {
		if canonical, pathErr := files.RealPath("."); pathErr == nil {
			home = canonical
		}
	}
	if ctx.Err() != nil || time.Now().After(deadline) {
		return result, errors.New("SSH 初始化超时或已取消")
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return result, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return result, err
	}
	if !stopCancel() || ctx.Err() != nil {
		return result, errors.New("SSH 初始化超时或已取消")
	}
	id := hex.EncodeToString(token[:])
	s := &managedSession{client: client, sftp: files, sftpErr: filesErr, cols: cols, rows: rows, done: make(chan struct{}), profile: profile, secret: secret}
	s.output = newTerminalWriter(s.done)
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return result, errors.New("SSH 管理器已关闭")
	}
	m.sessions[id] = s
	owned = true
	m.mu.Unlock()
	go m.pumpOutput(id, s)
	go func() {
		err := client.Wait()
		s.mu.Lock()
		shellOwnsClosure := s.starting || s.started
		s.mu.Unlock()
		// A running shell's Wait drains stdout/stderr before announcing closure.
		if !shellOwnsClosure {
			m.finish(id, s, err)
		}
	}()
	go m.keepalive(id, s)
	return model.Connection{ID: id, ProfileID: profile.ID, Name: profile.Name, Home: home}, nil
}

// Start is deliberately separate from Connect: the UI must subscribe to terminal
// events and mount its terminal before starting the shell so the first prompt is kept.
func (m *Manager) Start(id string) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("连接已关闭")
	}
	if s.started {
		s.mu.Unlock()
		return nil
	}
	if s.starting {
		s.mu.Unlock()
		return errors.New("终端正在启动")
	}
	s.starting = true
	s.mu.Unlock()
	timer := time.AfterFunc(connectionTimeout, func() { m.finish(id, s, errors.New("终端启动超时")) })
	defer timer.Stop()
	fail := func(err error) error {
		m.finish(id, s, err)
		return fmt.Errorf("启动终端失败: %w", err)
	}
	// Listeners are mounted before Start. Queue the welcome information before
	// creating the PTY so it cannot overwrite or split the shell's first prompt.
	if overview := m.readServerOverview(s); overview != "" {
		if _, err := io.WriteString(s.output, overview); err != nil {
			return fail(err)
		}
	}
	s.mu.Lock()
	cancelled := s.closed || m.ctx.Err() != nil
	s.mu.Unlock()
	if cancelled {
		return fail(errors.New("连接已关闭"))
	}
	pty, err := s.client.NewSession()
	if err != nil {
		return fail(err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = pty.Close()
		return errors.New("连接已关闭")
	}
	s.pty = pty
	cols, rows := s.cols, s.rows
	s.mu.Unlock()
	stdin, err := pty.StdinPipe()
	if err != nil {
		return fail(err)
	}
	pty.Stdout, pty.Stderr = s.output, s.output
	if err := pty.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); err != nil {
		return fail(err)
	}
	if err := pty.Shell(); err != nil {
		return fail(err)
	}
	s.resizeMu.Lock()
	defer s.resizeMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("连接已关闭")
	}
	s.stdin, s.started, s.starting = stdin, true, false
	latestCols, latestRows := s.cols, s.rows
	s.mu.Unlock()
	if latestCols != cols || latestRows != rows {
		if err := pty.WindowChange(latestRows, latestCols); err != nil {
			return fail(err)
		}
	}
	go func() { m.finish(id, s, pty.Wait()) }()
	return nil
}

func (m *Manager) Write(id, data string) error {
	if len(data) > 1024*1024 {
		return errors.New("单次终端输入超过 1 MiB")
	}
	s, err := m.get(id)
	if err != nil {
		return err
	}
	// Serialise input calls, preserving pasted data and partial writes.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	input, ready := s.stdin, s.started && !s.closed
	s.mu.Unlock()
	if !ready {
		return errors.New("终端尚未就绪")
	}
	_, err = io.Copy(input, strings.NewReader(data))
	return err
}

func (m *Manager) Resize(id string, cols, rows int) error {
	if err := validSize(cols, rows); err != nil {
		return err
	}
	s, err := m.get(id)
	if err != nil {
		return err
	}
	s.resizeMu.Lock()
	defer s.resizeMu.Unlock()
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	pty, ready := s.pty, s.started && !s.closed
	s.mu.Unlock()
	if !ready {
		return nil
	}
	return pty.WindowChange(rows, cols)
}

func (m *Manager) SFTP(id string) (*sftp.Client, error) {
	s, err := m.get(id)
	if err != nil {
		return nil, err
	}
	if s.sftp == nil {
		return nil, fmt.Errorf("服务器未提供 SFTP: %w", s.sftpErr)
	}
	return s.sftp, nil
}

func (m *Manager) SSH(id string) (*ssh.Client, error) {
	s, err := m.get(id)
	if err != nil {
		return nil, err
	}
	return s.client, nil
}

func (m *Manager) Disconnect(id string) error {
	s, err := m.get(id)
	if err != nil {
		return nil
	} // Closing a tab is idempotent.
	m.finish(id, s, nil)
	return nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	sessions := make(map[string]*managedSession, len(m.sessions))
	for id, s := range m.sessions {
		sessions[id] = s
	}
	m.mu.Unlock()
	m.cancel()
	for id, s := range sessions {
		m.finish(id, s, nil)
	}
}

func (m *Manager) get(id string) (*managedSession, error) {
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	if s == nil {
		return nil, errors.New("SSH 会话不存在或已断开")
	}
	return s, nil
}

func (m *Manager) finish(id string, s *managedSession, err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		if err != nil {
			s.closeError = err.Error()
		}
		s.mu.Unlock()
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		close(s.done)
		// Close the transport first to unblock pending SSH/SFTP requests and writes.
		_ = s.client.Close()
		if s.sftp != nil {
			_ = s.sftp.Close()
		}
		s.output.close()
	})
}

func (m *Manager) keepalive(id string, s *managedSession) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			result := make(chan error, 1)
			go func() { _, _, err := s.client.SendRequest("keepalive@openssh.com", true, nil); result <- err }()
			timer := time.NewTimer(10 * time.Second)
			select {
			case <-s.done:
				timer.Stop()
				return
			case err := <-result:
				timer.Stop()
				if err != nil {
					m.finish(id, s, err)
					return
				}
			case <-timer.C:
				m.finish(id, s, errors.New("SSH 心跳超时，连接已断开"))
				return
			}
		}
	}
}

func authentication(profile model.Profile, secret string) ([]ssh.AuthMethod, error) {
	switch profile.AuthKind {
	case "", "password":
		return []ssh.AuthMethod{ssh.Password(secret), ssh.KeyboardInteractive(func(_ string, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = secret
			}
			return answers, nil
		})}, nil
	case "private_key", "privateKey", "key":
		keyPath := profile.KeyPath
		if strings.HasPrefix(keyPath, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			keyPath = filepath.Join(home, strings.TrimPrefix(keyPath, "~/"))
		}
		data, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("读取 SSH 私钥失败: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(data)
		var passphrase *ssh.PassphraseMissingError
		if errors.As(err, &passphrase) {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(secret))
		}
		if err != nil {
			return nil, fmt.Errorf("解析 SSH 私钥失败: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("不支持的 SSH 认证方式: %s", profile.AuthKind)
	}
}

func validHost(host string, port int) (string, error) {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" || strings.ContainsAny(host, " \t\r\n\x00,*!?[]#") {
		return "", errors.New("SSH 主机地址无效")
	}
	if port < 1 || port > 65535 {
		return "", errors.New("SSH 端口必须在 1–65535 之间")
	}
	return host, nil
}

func validSize(cols, rows int) error {
	if cols < 1 || cols > 10000 || rows < 1 || rows > 10000 {
		return errors.New("终端尺寸必须在 1–10000 之间")
	}
	return nil
}

// knownHosts combines this application's trust file with the user's OpenSSH file.
// It fails closed on malformed or unreadable files instead of bypassing verification.
func (m *Manager) knownHosts() (ssh.HostKeyCallback, error) {
	paths := []string{}
	candidates := []string{m.knownHostsPath}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".ssh", "known_hosts"))
	}
	seen := make(map[string]bool)
	for _, path := range candidates {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if _, err := os.Stat(path); err == nil {
			paths = append(paths, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return knownhosts.New(paths...)
}

func (m *Manager) hostKeyCallback(host string, port int) (ssh.HostKeyCallback, error) {
	m.knownMu.Lock()
	check, err := m.knownHosts()
	m.knownMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("读取已信任主机失败: %w", err)
	}
	return func(address string, remote net.Addr, key ssh.PublicKey) error {
		err := check(address, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			payload, _ := json.Marshal(map[string]interface{}{
				"host": host, "port": port, "key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), "fingerprint": ssh.FingerprintSHA256(key),
			})
			return errors.New("HOST_KEY_UNKNOWN:" + string(payload))
		}
		return fmt.Errorf("服务器主机密钥与已信任记录不一致或已被撤销，连接已拒绝 (%s): %w", ssh.FingerprintSHA256(key), err)
	}, nil
}

// TrustHost records only a new host; it never silently replaces an existing key.
func (m *Manager) TrustHost(host string, port int, key string) error {
	host, err := validHost(host, port)
	if err != nil {
		return err
	}
	publicKey, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		return errors.New("无效的 SSH 主机公钥")
	}
	if m.knownHostsPath == "" {
		return errors.New("未配置主机密钥存储路径")
	}
	m.knownMu.Lock()
	defer m.knownMu.Unlock()
	check, err := m.knownHosts()
	if err != nil {
		return err
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	err = check(address, &net.TCPAddr{IP: net.IPv4zero, Port: port}, publicKey)
	if err == nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) != 0 {
		return errors.New("该主机已有不同的密钥记录，不能直接覆盖")
	}
	if err := os.MkdirAll(filepath.Dir(m.knownHostsPath), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(m.knownHostsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	// A leading newline also handles an existing file without a trailing newline.
	_, err = io.WriteString(file, "\n"+knownhosts.Line([]string{address}, publicKey)+"\n")
	if err != nil {
		return err
	}
	return file.Sync()
}

// The bounded queue applies backpressure to SSH reads. Raw bytes are base64 encoded
// only when emitted, preserving split UTF-8 sequences and terminal control bytes.
type terminalWriter struct {
	mu     sync.Mutex
	chunks chan []byte
	done   <-chan struct{}
	closed bool
}

func newTerminalWriter(done <-chan struct{}) *terminalWriter {
	return &terminalWriter{chunks: make(chan []byte, 32), done: done}
}

func (w *terminalWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	written := 0
	for len(data) > 0 {
		n := min(len(data), 8192)
		chunk := append([]byte(nil), data[:n]...)
		select {
		case <-w.done:
			return written, io.ErrClosedPipe
		case w.chunks <- chunk:
			written += n
			data = data[n:]
		}
	}
	return written, nil
}

func (w *terminalWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		close(w.chunks)
	}
}

func (m *Manager) pumpOutput(id string, s *managedSession) {
	ticker := time.NewTicker(16 * time.Millisecond)
	defer ticker.Stop()
	buffer := make([]byte, 0, 64*1024)
	flush := func() {
		if len(buffer) == 0 {
			return
		}
		m.emit("terminal:data", map[string]string{"sessionId": id, "data": base64.StdEncoding.EncodeToString(buffer)})
		buffer = buffer[:0]
	}
	for {
		select {
		case chunk, ok := <-s.output.chunks:
			if !ok {
				flush()
				s.mu.Lock()
				message := s.closeError
				s.mu.Unlock()
				m.emit("terminal:closed", map[string]string{"sessionId": id, "error": message})
				return
			}
			buffer = append(buffer, chunk...)
			if len(buffer) >= 64*1024 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

package sshclient

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"wails-ssh/internal/model"
)

type fixture struct {
	listener    net.Listener
	key         ssh.Signer
	userKey     ed25519.PrivateKey
	root        string
	initial     chan [2]int
	resized     chan [2]int
	shells      atomic.Int32
	mu          sync.Mutex
	connections []net.Conn
	wg          sync.WaitGroup
	execHandler func(ssh.Channel, *ssh.Request, string, <-chan *ssh.Request)
}

func server(t *testing.T, sftpEnabled bool) *fixture {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	_, userKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	userSigner, err := ssh.NewSignerFromKey(userKey)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{listener: listener, key: signer, userKey: userKey, root: t.TempDir(), initial: make(chan [2]int, 8), resized: make(chan [2]int, 8)}
	config := &ssh.ServerConfig{
		PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if metadata.User() != "test" || string(password) != "secret" {
				return nil, fmt.Errorf("denied")
			}
			return nil, nil
		},
		PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if metadata.User() != "test" || !bytes.Equal(key.Marshal(), userSigner.PublicKey().Marshal()) {
				return nil, fmt.Errorf("denied")
			}
			return nil, nil
		},
	}
	config.AddHostKey(signer)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.connections = append(f.connections, raw)
			f.mu.Unlock()
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				defer raw.Close()
				conn, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer conn.Close()
				go func() {
					for request := range requests {
						_ = request.Reply(request.Type == "keepalive@openssh.com", nil)
					}
				}()
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						_ = incoming.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					f.wg.Add(1)
					go func() { defer f.wg.Done(); f.handleChannel(channel, requests, sftpEnabled) }()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = f.listener.Close()
		f.mu.Lock()
		for _, conn := range f.connections {
			_ = conn.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func (f *fixture) handleChannel(channel ssh.Channel, requests <-chan *ssh.Request, sftpEnabled bool) {
	defer channel.Close()
	for request := range requests {
		switch request.Type {
		case "subsystem":
			var payload struct{ Name string }
			_ = ssh.Unmarshal(request.Payload, &payload)
			if !sftpEnabled || payload.Name != "sftp" {
				_ = request.Reply(false, nil)
				continue
			}
			_ = request.Reply(true, nil)
			files, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(f.root))
			if err == nil {
				_ = files.Serve()
				_ = files.Close()
			}
			return
		case "pty-req":
			var payload struct {
				Term                      string
				Cols, Rows, Width, Height uint32
				Modes                     string
			}
			if ssh.Unmarshal(request.Payload, &payload) != nil {
				_ = request.Reply(false, nil)
				continue
			}
			f.initial <- [2]int{int(payload.Cols), int(payload.Rows)}
			_ = request.Reply(true, nil)
		case "window-change":
			var payload struct{ Cols, Rows, Width, Height uint32 }
			_ = ssh.Unmarshal(request.Payload, &payload)
			f.resized <- [2]int{int(payload.Cols), int(payload.Rows)}
		case "shell":
			f.shells.Add(1)
			_ = request.Reply(true, nil)
			// stdout and SSH extended stderr are copied by independent goroutines.
			// Keep the greeting in one write: this shared fixture must not require
			// cross-stream ordering between two halves of a UTF-8 character.
			_, _ = channel.Write([]byte("你\r\n"))
			_, _ = channel.Stderr().Write([]byte("stderr\r\n"))
			go func() {
				data := make([]byte, 4096)
				for {
					n, err := channel.Read(data)
					if err != nil {
						return
					}
					if strings.Contains(string(data[:n]), "exit") {
						_, _ = channel.Write(bytes.Repeat([]byte("z"), 512*1024))
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						_ = channel.Close()
						return
					}
					_, _ = channel.Write(data[:n])
				}
			}()
		case "exec":
			var payload struct{ Command string }
			_ = ssh.Unmarshal(request.Payload, &payload)
			f.mu.Lock()
			handler := f.execHandler
			f.mu.Unlock()
			if handler != nil {
				handler(channel, request, payload.Command, requests)
				return
			}
			_ = request.Reply(true, nil)
			_, _ = channel.Write([]byte("ubuntu\t8\t17179869184\t107374182400"))
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func (f *fixture) profile() model.Profile {
	host, portText, _ := net.SplitHostPort(f.listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return model.Profile{ID: 7, Name: "Local test", Host: host, Port: port, Username: "test", AuthKind: "password"}
}

type capturedEvent struct {
	name string
	data map[string]string
}

func events() (chan capturedEvent, func(string, ...interface{})) {
	values := make(chan capturedEvent, 256)
	return values, func(name string, args ...interface{}) { values <- capturedEvent{name, args[0].(map[string]string)} }
}

func trustedManager(t *testing.T, f *fixture, emit func(string, ...interface{})) *Manager {
	t.Helper()
	m := New(filepath.Join(t.TempDir(), "known_hosts"), emit)
	p := f.profile()
	if err := m.TrustHost(p.Host, p.Port, string(ssh.MarshalAuthorizedKey(f.key.PublicKey()))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func nextEvent(t *testing.T, values <-chan capturedEvent) capturedEvent {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for event")
		return capturedEvent{}
	}
}

func readUntil(t *testing.T, values <-chan capturedEvent, needles ...string) string {
	t.Helper()
	var output strings.Builder
	defer func() {
		if t.Failed() {
			t.Logf("waiting for %q; received %q", needles, output.String())
		}
	}()
	complete := func() bool {
		for _, needle := range needles {
			if !strings.Contains(output.String(), needle) {
				return false
			}
		}
		return true
	}
	for !complete() {
		event := nextEvent(t, values)
		if event.name != "terminal:data" {
			t.Fatalf("unexpected event: %+v", event)
		}
		data, err := base64.StdEncoding.DecodeString(event.data["data"])
		if err != nil {
			t.Fatal(err)
		}
		output.Write(data)
	}
	return output.String()
}

func TestPasswordSessionPTYOutputResizeSFTPAndFacts(t *testing.T) {
	f := server(t, true)
	values, emit := events()
	m := trustedManager(t, f, emit)
	connection, err := m.Connect(f.profile(), "secret", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if connection.ProfileID != 7 || connection.Home != f.root {
		t.Fatalf("unexpected connection: %+v", connection)
	}
	if err := m.Write(connection.ID, "too early"); err == nil {
		t.Fatal("accepted input before Start")
	}
	if f.shells.Load() != 0 {
		t.Fatal("Connect started the shell before listeners were ready")
	}
	if err := m.Resize(connection.ID, 100, 36); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(connection.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case size := <-f.initial:
		if size != [2]int{100, 36} {
			t.Fatalf("initial size: %v", size)
		}
	case <-time.After(time.Second):
		t.Fatal("missing PTY request")
	}
	if err := m.Start(connection.ID); err != nil {
		t.Fatal(err)
	}
	if f.shells.Load() != 1 {
		t.Fatal("Start created duplicate shells")
	}
	output := readUntil(t, values, "你\r\n", "stderr\r\n")
	if !strings.Contains(output, "你\r\n") {
		t.Fatalf("UTF-8 corrupted: %q", output)
	}
	if err := m.Write(connection.ID, "echo 中文\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, values, "echo 中文\r")
	if err := m.Resize(connection.ID, 120, 40); err != nil {
		t.Fatal(err)
	}
	select {
	case size := <-f.resized:
		if size != [2]int{120, 40} {
			t.Fatalf("resized: %v", size)
		}
	case <-time.After(time.Second):
		t.Fatal("missing resize")
	}
	files, err := m.SFTP(connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	file, err := files.Create(filepath.Join(f.root, "transfer.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "file bytes"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	data, err := os.ReadFile(filepath.Join(f.root, "transfer.txt"))
	if err != nil || string(data) != "file bytes" {
		t.Fatalf("SFTP round trip: %q %v", data, err)
	}
	facts, err := m.ReadSystemFacts(connection.ID)
	if err != nil || facts.OSID != "ubuntu" || facts.CPUCores != 8 || facts.MemoryBytes != 17179869184 || facts.DiskBytes != 107374182400 {
		t.Fatalf("facts: %+v %v", facts, err)
	}
	if err := m.Disconnect(connection.ID); err != nil {
		t.Fatal(err)
	}
	closed := nextEvent(t, values)
	if closed.name != "terminal:closed" || closed.data["sessionId"] != connection.ID || closed.data["error"] != "" {
		t.Fatalf("close: %+v", closed)
	}
	if _, err := m.SSH(connection.ID); err == nil {
		t.Fatal("closed session retained")
	}
	if err := m.Disconnect(connection.ID); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownHostTrustAndMismatch(t *testing.T) {
	f := server(t, true)
	m := New(filepath.Join(t.TempDir(), "known_hosts"), nil)
	defer m.Close()
	p := f.profile()
	_, err := m.Connect(p, "secret", 80, 24)
	if err == nil {
		t.Fatal("unknown host accepted")
	}
	_, payload, found := strings.Cut(err.Error(), "HOST_KEY_UNKNOWN:")
	if !found {
		t.Fatalf("missing host key prompt: %v", err)
	}
	var unknown struct {
		Host             string
		Port             int
		Key, Fingerprint string
	}
	if err := json.Unmarshal([]byte(payload), &unknown); err != nil {
		t.Fatal(err)
	}
	if unknown.Host != p.Host || unknown.Port != p.Port || unknown.Fingerprint != ssh.FingerprintSHA256(f.key.PublicKey()) {
		t.Fatalf("incorrect prompt: %+v", unknown)
	}
	if err := m.TrustHost(unknown.Host, unknown.Port, unknown.Key); err != nil {
		t.Fatal(err)
	}
	if err := m.TrustHost(unknown.Host, unknown.Port, unknown.Key); err != nil {
		t.Fatal("trust not idempotent:", err)
	}
	stat, err := os.Stat(m.knownHostsPath)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("known_hosts permissions: %v %v", stat, err)
	}
	connection, err := m.Connect(p, "secret", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Disconnect(connection.ID)
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	other, _ := ssh.NewSignerFromKey(otherKey)
	if err := m.TrustHost(p.Host, p.Port, string(ssh.MarshalAuthorizedKey(other.PublicKey()))); err == nil {
		t.Fatal("changed host key overwrote trust")
	}
	wrong := New(filepath.Join(t.TempDir(), "known_hosts"), nil)
	defer wrong.Close()
	if err := wrong.TrustHost(p.Host, p.Port, string(ssh.MarshalAuthorizedKey(other.PublicKey()))); err != nil {
		t.Fatal(err)
	}
	_, err = wrong.Connect(p, "secret", 80, 24)
	if err == nil || strings.Contains(err.Error(), "HOST_KEY_UNKNOWN:") {
		t.Fatalf("mismatch was treated as unknown: %v", err)
	}
}

func TestEncryptedPrivateKeyAndTerminalWithoutSFTP(t *testing.T) {
	f := server(t, false)
	m := trustedManager(t, f, nil)
	key, err := ssh.MarshalPrivateKeyWithPassphrase(f.userKey, "test", []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(key), 0600); err != nil {
		t.Fatal(err)
	}
	p := f.profile()
	p.AuthKind, p.KeyPath = "private_key", path
	if _, err := m.Connect(p, "wrong", 80, 24); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	connection, err := m.Connect(p, "passphrase", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Home != "." {
		t.Fatalf("unexpected home: %s", connection.Home)
	}
	if _, err := m.SFTP(connection.ID); err == nil {
		t.Fatal("missing subsystem succeeded")
	}
	if err := m.Start(connection.ID); err != nil {
		t.Fatal(err)
	}
}

func TestShellExitFlushesAllOutputBeforeClosed(t *testing.T) {
	f := server(t, true)
	values, emit := events()
	m := trustedManager(t, f, emit)
	connection, err := m.Connect(f.profile(), "secret", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(connection.ID); err != nil {
		t.Fatal(err)
	}
	readUntil(t, values, "你\r\n", "stderr\r\n")
	if err := m.Write(connection.ID, "exit\r"); err != nil {
		t.Fatal(err)
	}
	total := 0
	for {
		event := nextEvent(t, values)
		if event.name == "terminal:closed" {
			if event.data["error"] != "" {
				t.Fatalf("clean exit error: %s", event.data["error"])
			}
			break
		}
		data, err := base64.StdEncoding.DecodeString(event.data["data"])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, bytes.Repeat([]byte("z"), len(data))) {
			t.Fatal("terminal output changed")
		}
		total += len(data)
	}
	if total != 512*1024 {
		t.Fatalf("lost final output: %d bytes", total)
	}
}

func TestCloseCancelsPendingHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() { conn, _ := listener.Accept(); accepted <- conn }()
	host, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	m := New(filepath.Join(t.TempDir(), "known_hosts"), nil)
	result := make(chan error, 1)
	go func() {
		_, err := m.Connect(model.Profile{Host: host, Port: port, Username: "test", AuthKind: "password"}, "secret", 80, 24)
		result <- err
	}()
	var raw net.Conn
	select {
	case raw = <-accepted:
		defer raw.Close()
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	m.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel handshake")
	}
}

func TestTerminalWriterPreservesSplitUTF8AcrossEvents(t *testing.T) {
	values, emit := events()
	m := &Manager{emit: emit}
	s := &managedSession{output: newTerminalWriter(make(chan struct{}))}
	defer s.output.close()
	go m.pumpOutput("split-utf8", s)
	var output bytes.Buffer
	for _, chunk := range [][]byte{{0xe4, 0xbd}, {0xa0, '\r', '\n'}} {
		if _, err := s.output.Write(chunk); err != nil {
			t.Fatal(err)
		}
		// Drain each fragment before writing the next, so the split really spans
		// distinct base64 events without an unrelated stderr stream in between.
		event := nextEvent(t, values)
		if event.name != "terminal:data" || event.data["sessionId"] != "split-utf8" {
			t.Fatalf("unexpected output event: %+v", event)
		}
		data, err := base64.StdEncoding.DecodeString(event.data["data"])
		if err != nil || !bytes.Equal(data, chunk) {
			t.Fatalf("fragment changed: %x, error: %v", data, err)
		}
		output.Write(data)
	}
	s.output.close()
	if event := nextEvent(t, values); event.name != "terminal:closed" {
		t.Fatalf("missing closure after output: %+v", event)
	}
	if output.String() != "你\r\n" {
		t.Fatalf("split UTF-8 corrupted: %q", output.String())
	}
}

func TestTerminalWriterBackpressureAndCancellation(t *testing.T) {
	done := make(chan struct{})
	writer := newTerminalWriter(done)
	finished := make(chan error, 1)
	go func() { _, err := writer.Write(make([]byte, 1024*1024)); finished <- err }()
	select {
	case <-finished:
		t.Fatal("writer did not apply backpressure")
	case <-time.After(20 * time.Millisecond):
	}
	close(done)
	select {
	case err := <-finished:
		if err != io.ErrClosedPipe {
			t.Fatalf("cancelled write: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled writer blocked")
	}
	writer.close()
}

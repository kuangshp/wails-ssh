package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"wails-ssh/internal/model"
)

type loopbackServer struct {
	listener net.Listener
	key      ssh.Signer
	secret   string
	root     string
	delay    time.Duration
	mu       sync.Mutex
	closed   bool
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
	once     sync.Once
	active   atomic.Int64
	peak     atomic.Int64
	total    atomic.Int64
}

func startServer(root string, delay time.Duration) (*loopbackServer, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, err
	}
	var password [32]byte
	if _, err := rand.Read(password[:]); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &loopbackServer{listener: listener, key: signer, secret: hex.EncodeToString(password[:]), root: root, delay: delay, conns: make(map[net.Conn]struct{})}
	configuration := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if meta.User() != "transferbench" || subtle.ConstantTimeCompare(password, []byte(s.secret)) != 1 {
			return nil, errors.New("benchmark authentication rejected")
		}
		return nil, nil
	}}
	configuration.AddHostKey(signer)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				_ = raw.Close()
				return
			}
			s.conns[raw] = struct{}{}
			s.wg.Add(1)
			s.mu.Unlock()
			go s.serveConnection(raw, configuration)
		}
	}()
	return s, nil
}

func (s *loopbackServer) profile(index int) model.Profile {
	host, portText, _ := net.SplitHostPort(s.listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return model.Profile{ID: int64(index + 1), Name: "Disposable loopback benchmark", Host: host, Port: port, Username: "transferbench", AuthKind: "password"}
}

func (s *loopbackServer) serveConnection(raw net.Conn, configuration *ssh.ServerConfig) {
	defer s.wg.Done()
	defer func() {
		_ = raw.Close()
		s.mu.Lock()
		delete(s.conns, raw)
		s.mu.Unlock()
	}()
	conn, channels, requests, err := ssh.NewServerConn(raw, configuration)
	if err != nil {
		return
	}
	defer conn.Close()
	s.total.Add(1)
	current := s.active.Add(1)
	defer s.active.Add(-1)
	for previous := s.peak.Load(); current > previous; previous = s.peak.Load() {
		if s.peak.CompareAndSwap(previous, current) {
			break
		}
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for request := range requests {
			_ = request.Reply(request.Type == "keepalive@openssh.com", nil)
		}
	}()
	for incoming := range channels {
		if incoming.ChannelType() != "session" {
			_ = incoming.Reject(ssh.UnknownChannelType, "only benchmark SFTP sessions are supported")
			continue
		}
		channel, requests, err := incoming.Accept()
		if err != nil {
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveChannel(channel, requests)
		}()
	}
}

func (s *loopbackServer) serveChannel(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for request := range requests {
		var subsystem struct{ Name string }
		if request.Type != "subsystem" || ssh.Unmarshal(request.Payload, &subsystem) != nil || subsystem.Name != "sftp" {
			_ = request.Reply(false, nil)
			continue
		}
		_ = request.Reply(true, nil)
		var transport io.ReadWriteCloser = channel
		if s.delay > 0 {
			delayed := newDelayedReplies(channel, s.delay)
			transport = delayed
			defer func() { _ = delayed.Close(); <-delayed.finished }()
		}
		server, err := sftp.NewServer(transport, sftp.WithServerWorkingDirectory(s.root))
		if err == nil {
			_ = server.Serve()
			_ = server.Close()
		}
		return
	}
}

func (s *loopbackServer) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		_ = s.listener.Close()
		for conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
}

type delayedReply struct {
	data []byte
	due  time.Time
}

// Every reply receives its own release timestamp when enqueued. The single
// writer preserves byte order, but overlapping requests wait concurrently:
// sixteen WRITEs cost one delay window, not sixteen serialized sleeps.
type delayedReplies struct {
	io.ReadWriteCloser
	delay    time.Duration
	pending  chan delayedReply
	done     chan struct{}
	finished chan struct{}
	once     sync.Once
}

func newDelayedReplies(channel io.ReadWriteCloser, delay time.Duration) *delayedReplies {
	d := &delayedReplies{ReadWriteCloser: channel, delay: delay, pending: make(chan delayedReply, 256), done: make(chan struct{}), finished: make(chan struct{})}
	go d.deliver()
	return d
}

func (d *delayedReplies) Write(data []byte) (int, error) {
	reply := delayedReply{data: append([]byte(nil), data...), due: time.Now().Add(d.delay)}
	select {
	case <-d.done:
		return 0, net.ErrClosed
	case d.pending <- reply:
		return len(data), nil
	}
}

func (d *delayedReplies) deliver() {
	defer close(d.finished)
	for {
		select {
		case <-d.done:
			return
		case reply := <-d.pending:
			if remaining := time.Until(reply.due); remaining > 0 {
				timer := time.NewTimer(remaining)
				select {
				case <-d.done:
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			for len(reply.data) > 0 {
				n, err := d.ReadWriteCloser.Write(reply.data)
				if err != nil || n == 0 {
					_ = d.Close()
					return
				}
				reply.data = reply.data[n:]
			}
		}
	}
}

func (d *delayedReplies) Close() error {
	d.once.Do(func() { close(d.done); _ = d.ReadWriteCloser.Close() })
	return nil
}

package sshclient

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// TransferScope identifies destinations shared by two tabs to the same server.
func (m *Manager) TransferScope(id string) (string, error) {
	s, err := m.get(id)
	if err != nil {
		return "", err
	}
	// Use the endpoint of the authenticated transport, so two DNS aliases for
	// the same server cannot acquire different destination locks.
	if address := s.client.RemoteAddr(); address != nil {
		return address.String(), nil
	}
	host, err := validHost(s.profile.Host, s.profile.Port)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(strings.ToLower(host), strconv.Itoa(s.profile.Port)), nil
}

// TransferDialer captures only the credentials of an already authenticated tab.
// Each worker gets its own transport, so a PTY disconnect does not destroy its
// SFTP checkpoint. Credentials remain in memory for the lifetime of that job.
func (m *Manager) TransferDialer(id string) (func(context.Context) (*sftp.Client, func(), error), error) {
	s, err := m.get(id)
	if err != nil {
		return nil, err
	}
	profile, secret := s.profile, s.secret
	return func(jobCtx context.Context) (*sftp.Client, func(), error) {
		ctx, cancel := context.WithCancel(jobCtx)
		stopManager := context.AfterFunc(m.ctx, cancel)
		fail := func(err error) (*sftp.Client, func(), error) {
			cancel()
			stopManager()
			return nil, nil, err
		}
		if m.ctx.Err() != nil {
			return fail(m.ctx.Err())
		}
		host, err := validHost(profile.Host, profile.Port)
		if err != nil {
			return fail(err)
		}
		auth, err := authentication(profile, secret)
		if err != nil {
			return fail(err)
		}
		callback, err := m.hostKeyCallback(host, profile.Port)
		if err != nil {
			return fail(err)
		}
		address := net.JoinHostPort(strings.ToLower(host), strconv.Itoa(profile.Port))
		releaseHandshake, err := m.transferHandshake(ctx, address)
		if err != nil {
			return fail(err)
		}
		defer releaseHandshake()
		dialCtx, stopDial := context.WithTimeout(ctx, connectionTimeout)
		defer stopDial()
		raw, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", address)
		if err != nil {
			return fail(err)
		}
		stopConnection := context.AfterFunc(ctx, func() { _ = raw.Close() })
		var once sync.Once
		cleanup := func() {
			once.Do(func() { cancel(); stopManager(); stopConnection(); _ = raw.Close() })
		}
		deadline, _ := dialCtx.Deadline()
		_ = raw.SetDeadline(deadline)
		tracked := newTransferConn(raw)
		conn, channels, requests, err := ssh.NewClientConn(tracked, address, &ssh.ClientConfig{User: profile.Username, Auth: auth, HostKeyCallback: callback, Timeout: connectionTimeout})
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("SFTP SSH 连接失败: %w", err)
		}
		client := ssh.NewClient(conn, channels, requests)
		files, err := sftp.NewClient(client)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		if err = raw.SetDeadline(time.Time{}); err != nil {
			cleanup()
			return nil, nil, err
		}
		go keepTransferAlive(ctx, func() error { _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); return err }, tracked.lastActivity, cleanup, 20*time.Second, 60*time.Second)
		return files, cleanup, nil
	}, nil
}

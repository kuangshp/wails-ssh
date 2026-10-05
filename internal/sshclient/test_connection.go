package sshclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"wails-ssh/internal/model"
)

// TestConnection authenticates and executes the same no-op used by the original
// application. It never creates a terminal tab, starts SFTP, or records a visit.
func (m *Manager) TestConnection(profile model.Profile, secret string) error {
	host, err := validHost(profile.Host, profile.Port)
	if err != nil {
		return err
	}
	if strings.TrimSpace(profile.Username) == "" {
		return errors.New("SSH 用户名不能为空")
	}
	if m.ctx.Err() != nil {
		return errors.New("SSH 管理器已关闭")
	}
	auth, err := authentication(profile, secret)
	if err != nil {
		return err
	}
	callback, err := m.hostKeyCallback(host, profile.Port)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(m.ctx, connectionTimeout)
	defer cancel()
	address := net.JoinHostPort(host, strconv.Itoa(profile.Port))
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("无法连接 %s: %w", address, err)
	}
	defer raw.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stopCancel()
	deadline, _ := ctx.Deadline()
	if err := raw.SetDeadline(deadline); err != nil {
		return err
	}
	conn, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{
		User: profile.Username, Auth: auth, HostKeyCallback: callback, Timeout: connectionTimeout,
	})
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	client := ssh.NewClient(conn, channels, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("无法建立测试会话: %w", err)
	}
	defer session.Close()
	if err := session.Run("true"); err != nil {
		return fmt.Errorf("测试命令失败: %w", err)
	}
	return nil
}

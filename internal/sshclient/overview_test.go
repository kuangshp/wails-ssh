package sshclient

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"
)

func setOverviewHandler(f *fixture, handler func(ssh.Channel, *ssh.Request, string, <-chan *ssh.Request)) {
	f.mu.Lock()
	f.execHandler = handler
	f.mu.Unlock()
}

func sendOverview(channel ssh.Channel, request *ssh.Request, host string) {
	_ = request.Reply(true, nil)
	_, _ = fmt.Fprintf(channel, "unrelated login banner\n%sos\tUbuntu 24.04\n%shost\t%s\n%scores\t8\n", overviewPrefix, overviewPrefix, host, overviewPrefix)
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
}

func TestOverviewPrecedesShellAndRepeatedStartDoesNotRepeat(t *testing.T) {
	f := server(t, true)
	var reads atomic.Int32
	setOverviewHandler(f, func(channel ssh.Channel, request *ssh.Request, command string, _ <-chan *ssh.Request) {
		reads.Add(1)
		if !strings.Contains(command, overviewPrefix) {
			t.Error("wrong overview command")
		}
		sendOverview(channel, request, "test-host")
	})
	values, emit := events()
	m := trustedManager(t, f, emit)
	connection, err := m.Connect(f.profile(), "secret", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Start(connection.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.Write(connection.ID, "shell-ready\r"); err != nil {
		t.Fatal(err)
	}
	// The fixture intentionally splits a UTF-8 character while writing stderr
	// from another SSH stream. Use a stdout echo barrier, not a combined Unicode
	// needle that can be interrupted by that independent stderr stream.
	output := readUntil(t, values, "shell-ready\r", "stderr\r\n")
	welcome := strings.Index(output, "── 服务器欢迎信息 ──")
	shell := strings.Index(output, "shell-ready\r")
	if !strings.HasPrefix(output, "\x1b[1;32m── 服务器欢迎信息 ──") || welcome >= shell || !strings.Contains(output, "主机名称：test-host\r\n") || !strings.Contains(output, "CPU 核心：8\r\n") {
		t.Fatalf("welcome missing or reordered: %q", output)
	}
	if strings.Contains(output, "unrelated login banner") || !strings.Contains(output, "内存使用：未知\r\n") {
		t.Fatalf("unexpected metadata parsing: %q", output)
	}
	if err = m.Start(connection.ID); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 1 || f.shells.Load() != 1 {
		t.Fatalf("duplicate startup: metadata %d shells %d", reads.Load(), f.shells.Load())
	}
	if err = m.Write(connection.ID, "still connected\r"); err != nil {
		t.Fatal(err)
	}
	if output = readUntil(t, values, "still connected\r"); strings.Contains(output, "服务器欢迎信息") {
		t.Fatalf("repeated welcome: %q", output)
	}
}

func TestOverviewFailureAndTimeoutLeaveShellAvailable(t *testing.T) {
	for _, mode := range []string{"rejected", "failed", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := server(t, true)
			closed := make(chan struct{})
			setOverviewHandler(f, func(channel ssh.Channel, request *ssh.Request, _ string, requests <-chan *ssh.Request) {
				defer close(closed)
				if mode == "rejected" {
					_ = request.Reply(false, nil)
					return
				}
				_ = request.Reply(true, nil)
				if mode == "failed" {
					_, _ = channel.Stderr().Write([]byte("command unavailable"))
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{127}))
					return
				}
				for range requests { // Timeout must close only this channel.
				}
			})
			values, emit := events()
			m := trustedManager(t, f, emit)
			connection, err := m.Connect(f.profile(), "secret", 80, 24)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if err = m.Start(connection.ID); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(started)
			if elapsed > overviewTimeout+time.Second {
				t.Fatalf("startup exceeded best-effort deadline: %s", elapsed)
			}
			if mode == "timeout" && elapsed < overviewTimeout {
				t.Fatalf("fixture did not exercise the metadata timeout: %s", elapsed)
			}
			if err = m.Write(connection.ID, "shell-ready\r"); err != nil {
				t.Fatal(err)
			}
			output := readUntil(t, values, "shell-ready\r", "stderr\r\n")
			if strings.Contains(output, "服务器欢迎信息") || strings.Contains(output, "command unavailable") {
				t.Fatalf("failed metadata leaked into shell: %q", output)
			}
			if err = m.Write(connection.ID, "healthy\r"); err != nil {
				t.Fatal(err)
			}
			readUntil(t, values, "healthy\r")
			if files, err := m.SFTP(connection.ID); err != nil {
				t.Fatal(err)
			} else if _, err := files.ReadDir("."); err != nil {
				t.Fatalf("metadata failure closed SFTP: %v", err)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("metadata command channel was not cleaned up")
			}
		})
	}
}

func TestOverviewDisconnectCancelsPendingRead(t *testing.T) {
	for _, closeManager := range []bool{false, true} {
		t.Run(fmt.Sprint("manager=", closeManager), func(t *testing.T) {
			f := server(t, false)
			entered, closed := make(chan struct{}), make(chan struct{})
			setOverviewHandler(f, func(channel ssh.Channel, request *ssh.Request, _ string, requests <-chan *ssh.Request) {
				_ = request.Reply(true, nil)
				close(entered)
				for range requests {
				}
				close(closed)
			})
			m := trustedManager(t, f, nil)
			connection, err := m.Connect(f.profile(), "secret", 80, 24)
			if err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() { finished <- m.Start(connection.ID) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("overview did not begin")
			}
			if closeManager {
				m.Close()
			} else if err = m.Disconnect(connection.ID); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("started shell after disconnect")
				}
			case <-time.After(time.Second):
				t.Fatal("disconnect did not cancel startup")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("metadata command remained open")
			}
			if f.shells.Load() != 0 {
				t.Fatal("shell ran after cancellation")
			}
		})
	}
}

func TestOverviewSessionsAreIsolated(t *testing.T) {
	f := server(t, true)
	var calls atomic.Int32
	setOverviewHandler(f, func(channel ssh.Channel, request *ssh.Request, _ string, requests <-chan *ssh.Request) {
		sendOverview(channel, request, fmt.Sprintf("host-%d", calls.Add(1)))
	})
	values, emit := events()
	m := trustedManager(t, f, emit)
	for i := 1; i <= 2; i++ {
		connection, err := m.Connect(f.profile(), "secret", 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		if err = m.Start(connection.ID); err != nil {
			t.Fatal(err)
		}
		if err = m.Write(connection.ID, "shell-ready\r"); err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		for !strings.Contains(output.String(), "shell-ready\r") || !strings.Contains(output.String(), "stderr\r\n") {
			event := nextEvent(t, values)
			if event.name != "terminal:data" || event.data["sessionId"] != connection.ID {
				t.Fatalf("output leaked to another tab: %+v", event)
			}
			data, err := base64.StdEncoding.DecodeString(event.data["data"])
			if err != nil {
				t.Fatal(err)
			}
			output.Write(data)
		}
		if !strings.Contains(output.String(), fmt.Sprintf("主机名称：host-%d\r\n", i)) || strings.Count(output.String(), "服务器欢迎信息") != 1 {
			t.Fatalf("wrong welcome: %q", output.String())
		}
	}
}

func TestOverviewValuesAreBoundedAndCannotControlTerminal(t *testing.T) {
	output := formatServerOverview("arbitrary stdout\n" + overviewPrefix + "os\tUbuntu\n" + overviewPrefix + "host\t\x1b]52;c;clipboard\a\r\x00\u009b\u202e" + strings.Repeat("界", 1000) + "\n")
	if !strings.Contains(output, "操作系统：Ubuntu\r\n") || len(output) > 2000 || !strings.Contains(output, "…") {
		t.Fatalf("unexpected bounded output: %q", output)
	}
	withoutStyle := strings.ReplaceAll(strings.ReplaceAll(output, "\x1b[1;32m", ""), "\x1b[0m", "")
	for _, r := range withoutStyle {
		if r != '\r' && r != '\n' && !unicode.IsPrint(r) {
			t.Fatalf("terminal control survived: U+%04X", r)
		}
	}
	if strings.Contains(strings.ReplaceAll(output, "\r\n", ""), "\n") || strings.Count(output, "未知") != 8 {
		t.Fatalf("incorrect line endings/defaults: %q", output)
	}
	if formatServerOverview("random login output") != "" {
		t.Fatal("non-metadata stdout displayed as welcome")
	}
}

package sshclient

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

// SystemFacts is best-effort Linux metadata, matching the original application's fields.
type SystemFacts struct {
	OSID        string
	CPUCores    int
	MemoryBytes int64
	DiskBytes   int64
}

// ReadSystemFacts uses a separate SSH channel. It never changes the interactive
// shell's directory or mixes command output into the terminal stream.
func (m *Manager) ReadSystemFacts(id string) (SystemFacts, error) {
	var facts SystemFacts
	managed, err := m.get(id)
	if err != nil {
		return facts, err
	}
	client := managed.client
	type outcome struct {
		data string
		err  error
	}
	result := make(chan outcome, 1)
	cancel := make(chan struct{})
	go func() {
		session, err := client.NewSession()
		if err != nil {
			result <- outcome{err: err}
			return
		}
		defer session.Close()
		finished := make(chan struct{})
		defer close(finished)
		go func() {
			select {
			case <-cancel:
				_ = session.Close()
			case <-finished:
			}
		}()
		var output bytes.Buffer
		session.Stdout = &cappedWriter{writer: &output, remaining: 4096}
		session.Stderr = io.Discard
		err = session.Run(`. /etc/os-release 2>/dev/null || true; cores=$(getconf _NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null || echo 0); memory=$(awk '/MemTotal:/ {printf "%.0f", $2 * 1024; exit}' /proc/meminfo 2>/dev/null); disk=$(df -B1 -P / 2>/dev/null | awk 'NR==2 {print $2}'); printf '%s\t%s\t%s\t%s' "${ID:-unknown}" "${cores:-0}" "${memory:-0}" "${disk:-0}"`)
		result <- outcome{data: output.String(), err: err}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	defer close(cancel)
	select {
	case value := <-result:
		if value.err != nil {
			return facts, value.err
		}
		fields := strings.Split(strings.TrimSpace(value.data), "\t")
		if len(fields) != 4 {
			return facts, errors.New("无法识别服务器系统信息")
		}
		facts.OSID = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
				return r
			}
			return -1
		}, strings.ToLower(fields[0]))
		facts.CPUCores, _ = strconv.Atoi(fields[1])
		facts.MemoryBytes, _ = strconv.ParseInt(fields[2], 10, 64)
		facts.DiskBytes, _ = strconv.ParseInt(fields[3], 10, 64)
		return facts, nil
	case <-timer.C:
		// Close the command channel first. A server that also ignores channel
		// closure must have its transport closed to release NewSession/Run.
		go func() {
			cleanup := time.NewTimer(time.Second)
			defer cleanup.Stop()
			select {
			case <-result:
			case <-managed.done:
			case <-cleanup.C:
				m.finish(id, managed, errors.New("服务器系统信息通道无响应，SSH 连接已关闭"))
			}
		}()
		return facts, errors.New("读取服务器系统信息超时")
	case <-m.ctx.Done():
		return facts, errors.New("SSH 管理器已关闭")
	}
}

type cappedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *cappedWriter) Write(data []byte) (int, error) {
	n := min(len(data), w.remaining)
	if n > 0 {
		if _, err := w.writer.Write(data[:n]); err != nil {
			return 0, err
		}
		w.remaining -= n
	}
	return len(data), nil
}

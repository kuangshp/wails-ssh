package sshclient

import (
	"bytes"
	"context"
	"io"
	"strings"
	"time"
	"unicode"
)

const overviewTimeout = 3 * time.Second
const overviewPrefix = "__WAILS_SSH_OVERVIEW__"
const overviewOutputLimit = 16 * 1024

// These commands run in their own channel, before the interactive shell starts.
// Missing Linux utilities produce empty values rather than failing the login.
const overviewCommand = `
LC_ALL=C
export LC_ALL
if [ -r /etc/os-release ]; then . /etc/os-release 2>/dev/null; fi
os_name=${PRETTY_NAME:-}
kernel=$(uname -sr 2>/dev/null)
architecture=$(uname -m 2>/dev/null)
host_name=$(hostname 2>/dev/null)
cpu_model=$(awk -F: '/model name|Hardware|Processor/ {gsub(/^[ \t]+/, "", $2); print $2; exit}' /proc/cpuinfo 2>/dev/null)
cpu_cores=$(getconf _NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null)
memory=$(free -h 2>/dev/null | awk 'NR==2 {print $3 " / " $2}')
disk=$(df -hP / 2>/dev/null | awk 'NR==2 {print $3 " / " $2 " (" $5 ")"}')
ip_address=$(hostname -I 2>/dev/null | awk '{$1=$1; print}')
running=$(awk '{days=int($1/86400); hours=int(($1%86400)/3600); minutes=int(($1%3600)/60); printf "%d天 %d小时 %d分钟", days, hours, minutes}' /proc/uptime 2>/dev/null)
printf '__WAILS_SSH_OVERVIEW__os\t%s\n' "$os_name"
printf '__WAILS_SSH_OVERVIEW__host\t%s\n' "$host_name"
printf '__WAILS_SSH_OVERVIEW__kernel\t%s\n' "$kernel"
printf '__WAILS_SSH_OVERVIEW__arch\t%s\n' "$architecture"
printf '__WAILS_SSH_OVERVIEW__cpu\t%s\n' "$cpu_model"
printf '__WAILS_SSH_OVERVIEW__cores\t%s\n' "$cpu_cores"
printf '__WAILS_SSH_OVERVIEW__memory\t%s\n' "$memory"
printf '__WAILS_SSH_OVERVIEW__disk\t%s\n' "$disk"
printf '__WAILS_SSH_OVERVIEW__ip\t%s\n' "$ip_address"
printf '__WAILS_SSH_OVERVIEW__uptime\t%s\n' "$running"
`

var overviewFields = [...]struct{ key, label string }{
	{"os", "操作系统"}, {"host", "主机名称"}, {"kernel", "内核版本"},
	{"arch", "系统架构"}, {"cpu", "处理器"}, {"cores", "CPU 核心"},
	{"memory", "内存使用"}, {"disk", "根盘使用"}, {"ip", "IP 地址"}, {"uptime", "运行时间"},
}

// readServerOverview is best effort. Cancellation closes only its command
// channel, never the SSH transport shared with the terminal and transfers.
func (m *Manager) readServerOverview(s *managedSession) string {
	ctx, cancel := context.WithTimeout(m.ctx, overviewTimeout)
	defer cancel()
	result := make(chan string, 1)
	go func() {
		// SSH has no cancellable NewSession. Start's starting/started guard means
		// at most one pending overview exists per connection; disconnect releases
		// a server that ignores channel-open, without delaying shell startup here.
		session, err := s.client.NewSession()
		if err != nil {
			result <- ""
			return
		}
		defer session.Close()
		stopCancel := context.AfterFunc(ctx, func() { _ = session.Close() })
		defer stopCancel()
		if ctx.Err() != nil {
			return
		}
		var output bytes.Buffer
		session.Stdout = &cappedWriter{writer: &output, remaining: overviewOutputLimit}
		session.Stderr = io.Discard
		if err := session.Run(overviewCommand); err != nil {
			result <- ""
			return
		}
		result <- formatServerOverview(output.String())
	}()
	select {
	case overview := <-result:
		return overview
	case <-ctx.Done():
		return ""
	case <-s.done:
		return ""
	}
}

func formatServerOverview(output string) string {
	values := make(map[string]string, len(overviewFields))
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, overviewPrefix) {
			continue // Login-script banners and other stdout are not metadata.
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, overviewPrefix), "\t")
		if !ok {
			continue
		}
		for _, field := range overviewFields {
			if key == field.key {
				values[key] = cleanOverviewValue(value)
				break
			}
		}
	}
	if len(values) == 0 {
		return ""
	}
	var banner strings.Builder
	banner.WriteString("\x1b[1;32m── 服务器欢迎信息 ──\x1b[0m\r\n")
	for _, field := range overviewFields {
		value := values[field.key]
		if value == "" {
			value = "未知"
		}
		banner.WriteString(field.label + "：" + value + "\r\n")
	}
	banner.WriteString("\r\n")
	return banner.String()
}

func cleanOverviewValue(value string) string {
	var clean strings.Builder
	length := 0
	for _, r := range strings.ToValidUTF8(value, "") {
		if !unicode.IsPrint(r) {
			continue // Strip C0/C1, escape, bidi and other format controls.
		}
		if length == 256 {
			clean.WriteRune('…')
			break
		}
		clean.WriteRune(r)
		length++
	}
	return strings.TrimSpace(clean.String())
}

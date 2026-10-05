package storage

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"wails-ssh/internal/model"
)

// ParseSSHConfig imports concrete Host blocks and the common connection fields.
// Includes, Match, ProxyJump and wildcard inheritance require OpenSSH itself and
// are intentionally not interpreted by this direct SSH client.
func ParseSSHConfig(r io.Reader, home string) ([]model.Profile, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	result := []model.Profile{}
	aliases := []string{}
	p := model.Profile{}
	lineNumber := 0
	flush := func() {
		for _, alias := range aliases {
			if strings.ContainsAny(alias, "*?!") {
				continue
			}
			next := p
			next.Name = alias
			if next.Host == "" {
				next.Host = alias
			}
			if next.Username == "" {
				next.Username = "root"
			}
			next.AuthKind = "password"
			if next.KeyPath != "" {
				next.AuthKind = "private_key"
			}
			result = append(result, next)
		}
	}
	for scanner.Scan() {
		lineNumber++
		fields, err := configFields(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("SSH config 第 %d 行：%w", lineNumber, err)
		}
		if len(fields) < 2 {
			continue
		}
		key := strings.ToLower(fields[0])
		value := strings.Join(fields[1:], " ")
		if key == "host" {
			flush()
			aliases = fields[1:]
			p = model.Profile{Port: 22}
			continue
		}
		if key == "match" {
			flush()
			aliases = nil
			p = model.Profile{Port: 22}
			continue
		}
		if len(aliases) == 0 {
			continue
		}
		switch key {
		case "hostname":
			if p.Host == "" {
				p.Host = value
			}
		case "user":
			if p.Username == "" {
				p.Username = value
			}
		case "port":
			v, err := strconv.Atoi(value)
			if err != nil || v < 1 || v > 65535 {
				return nil, fmt.Errorf("SSH config 第 %d 行的端口无效", lineNumber)
			}
			p.Port = v
		case "identityfile":
			if p.KeyPath == "" {
				if strings.HasPrefix(value, "~/") {
					value = filepath.Join(home, value[2:])
				}
				p.KeyPath = value
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	for i := range result {
		if err := Validate(&result[i]); err != nil {
			return nil, fmt.Errorf("连接 %q：%w", result[i].Name, err)
		}
	}
	return result, nil
}

func configFields(line string) ([]string, error) {
	fields := []string{}
	var field strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if field.Len() > 0 {
			fields = append(fields, field.String())
			field.Reset()
		}
	}
	for _, r := range line {
		if escaped {
			if r != '"' && r != '\'' && r != '\\' && r != ' ' && r != '#' {
				field.WriteRune('\\')
			}
			field.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				field.WriteRune(r)
			}
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			continue
		}
		if r == '#' {
			break
		}
		if unicode.IsSpace(r) || (r == '=' && (len(fields) == 0 || (len(fields) == 1 && field.Len() == 0))) {
			flush()
			continue
		}
		field.WriteRune(r)
	}
	if escaped {
		field.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("引号未闭合")
	}
	flush()
	return fields, nil
}

func (s *Store) ImportProfiles(profiles []model.Profile) ([]model.Profile, error) {
	for i := range profiles {
		if err := Validate(&profiles[i]); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	imported := []model.Profile{}
	for _, p := range profiles {
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM server_profiles WHERE name=? AND host=? AND port=? AND username=?", p.Name, p.Host, p.Port, p.Username).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			continue
		}
		result, err := tx.Exec(`INSERT INTO server_profiles(name,host,port,username,auth_kind,key_path,group_name,remark) VALUES(?,?,?,?,?,?,?,?)`, p.Name, p.Host, p.Port, p.Username, p.AuthKind, p.KeyPath, p.GroupName, p.Remark)
		if err != nil {
			return nil, err
		}
		p.ID, err = result.LastInsertId()
		if err != nil {
			return nil, err
		}
		imported = append(imported, p)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return imported, nil
}

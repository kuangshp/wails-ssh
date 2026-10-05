package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"wails-ssh/internal/model"
)

type Store struct{ db *sql.DB }

func Open(filename string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	file.Close()
	if err := os.Chmod(filename, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	schema := `PRAGMA busy_timeout=5000;
 PRAGMA foreign_keys=ON;
 CREATE TABLE IF NOT EXISTS server_profiles (
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, host TEXT NOT NULL,
 port INTEGER NOT NULL DEFAULT 22, username TEXT NOT NULL, auth_kind TEXT NOT NULL,
 key_path TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT '', remark TEXT NOT NULL DEFAULT '',
 secret TEXT NOT NULL DEFAULT '', last_connected_at TEXT NOT NULL DEFAULT '', os_id TEXT NOT NULL DEFAULT '',
 cpu_cores INTEGER NOT NULL DEFAULT 0, memory_bytes INTEGER NOT NULL DEFAULT 0, disk_bytes INTEGER NOT NULL DEFAULT 0,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
 CREATE TABLE IF NOT EXISTS server_groups (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE);
 CREATE TABLE IF NOT EXISTS command_history (id INTEGER PRIMARY KEY AUTOINCREMENT, profile_id INTEGER NOT NULL REFERENCES server_profiles(id) ON DELETE CASCADE,
 command TEXT NOT NULL, use_count INTEGER NOT NULL DEFAULT 1, last_used_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(profile_id,command));`
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }

const profileColumns = `id,name,host,port,username,auth_kind,key_path,group_name,remark,last_connected_at,os_id,cpu_cores,memory_bytes,disk_bytes,(secret <> '')`

type scanner interface{ Scan(...any) error }

func scanProfile(row scanner) (p model.Profile, err error) {
	err = row.Scan(&p.ID, &p.Name, &p.Host, &p.Port, &p.Username, &p.AuthKind, &p.KeyPath, &p.GroupName, &p.Remark, &p.LastConnectedAt, &p.OSID, &p.CPUCores, &p.MemoryBytes, &p.DiskBytes, &p.HasSecret)
	return
}
func (s *Store) List() ([]model.Profile, error) {
	rows, err := s.db.Query("SELECT " + profileColumns + " FROM server_profiles ORDER BY name COLLATE NOCASE,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := []model.Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}
func (s *Store) Get(id int64) (model.Profile, error) {
	return scanProfile(s.db.QueryRow("SELECT "+profileColumns+" FROM server_profiles WHERE id=?", id))
}
func (s *Store) Secret(id int64) (string, error) {
	var secret string
	err := s.db.QueryRow("SELECT secret FROM server_profiles WHERE id=?", id).Scan(&secret)
	return secret, err
}

func Validate(p *model.Profile) error {
	p.Name = strings.TrimSpace(p.Name)
	p.Host = strings.TrimSpace(p.Host)
	p.Username = strings.TrimSpace(p.Username)
	p.GroupName = strings.TrimSpace(p.GroupName)
	p.KeyPath = strings.TrimSpace(p.KeyPath)
	if p.Name == "" || p.Host == "" || p.Username == "" {
		return errors.New("名称、主机地址和用户名不能为空")
	}
	if strings.ContainsAny(p.Host, "\x00\r\n\t /\\") || strings.ContainsAny(p.Username, "\x00\r\n") {
		return errors.New("主机地址或用户名包含无效字符")
	}
	// Accept bracketed IPv6 input while storing the address without brackets.
	if strings.HasPrefix(p.Host, "[") && strings.HasSuffix(p.Host, "]") {
		p.Host = strings.Trim(p.Host, "[]")
	}
	if strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil {
		return errors.New("请将端口填写在端口字段中")
	}
	if p.Port < 1 || p.Port > 65535 {
		return errors.New("端口必须在 1–65535 之间")
	}
	if p.AuthKind != "password" && p.AuthKind != "private_key" {
		return errors.New("不支持的认证方式")
	}
	if p.AuthKind == "private_key" && p.KeyPath == "" {
		return errors.New("请选择私钥文件")
	}
	if p.ID < 0 {
		return errors.New("连接 ID 无效")
	}
	return nil
}
func (s *Store) Save(p model.Profile, secret string, keepSecret bool) (model.Profile, error) {
	if err := Validate(&p); err != nil {
		return p, err
	}
	if p.ID == 0 {
		result, err := s.db.Exec(`INSERT INTO server_profiles(name,host,port,username,auth_kind,key_path,group_name,remark,secret) VALUES(?,?,?,?,?,?,?,?,?)`, p.Name, p.Host, p.Port, p.Username, p.AuthKind, p.KeyPath, p.GroupName, p.Remark, secret)
		if err != nil {
			return p, err
		}
		p.ID, err = result.LastInsertId()
		if err != nil {
			return p, err
		}
	} else {
		result, err := s.db.Exec(`UPDATE server_profiles SET name=?,host=?,port=?,username=?,auth_kind=?,key_path=?,group_name=?,remark=?,secret=CASE WHEN ? THEN secret ELSE ? END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, p.Name, p.Host, p.Port, p.Username, p.AuthKind, p.KeyPath, p.GroupName, p.Remark, keepSecret, secret, p.ID)
		if err != nil {
			return p, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return p, err
		}
		if n == 0 {
			return p, errors.New("连接不存在")
		}
	}
	return s.Get(p.ID)
}
func (s *Store) Delete(id int64) error {
	_, err := s.db.Exec("DELETE FROM server_profiles WHERE id=?", id)
	return err
}
func (s *Store) MarkConnected(id int64) error {
	_, err := s.db.Exec("UPDATE server_profiles SET last_connected_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=?", id)
	return err
}

func (s *Store) UpdateSystemFacts(id int64, osID string, cores int, memory, disk int64) error {
	_, err := s.db.Exec("UPDATE server_profiles SET os_id=?,cpu_cores=?,memory_bytes=?,disk_bytes=? WHERE id=?", osID, max(0, cores), max(int64(0), memory), max(int64(0), disk), id)
	return err
}
func (s *Store) Groups() ([]string, error) {
	rows, err := s.db.Query("SELECT name FROM server_groups UNION SELECT group_name FROM server_profiles WHERE group_name<>'' ORDER BY 1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		groups = append(groups, name)
	}
	return groups, rows.Err()
}
func (s *Store) CreateGroup(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("分组名称不能为空")
	}
	_, err := s.db.Exec("INSERT OR IGNORE INTO server_groups(name) VALUES(?)", name)
	return err
}
func (s *Store) History(id int64) ([]string, error) {
	rows, err := s.db.Query("SELECT command FROM command_history WHERE profile_id=? ORDER BY last_used_at DESC,id DESC LIMIT 200", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	commands := []string{}
	for rows.Next() {
		var cmd string
		if err := rows.Scan(&cmd); err != nil {
			return nil, err
		}
		commands = append(commands, cmd)
	}
	return commands, rows.Err()
}
func (s *Store) RecordCommand(id int64, command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	if len(command) > 65536 {
		return errors.New("命令过长")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// SQLite CURRENT_TIMESTAMP has one-second precision; quickly reusing an older
	// command otherwise leaves it below newer row IDs after the next app launch.
	usedAt := time.Now().UTC().Format("2006-01-02 15:04:05.000000000")
	if _, err = tx.Exec(`INSERT INTO command_history(profile_id,command,last_used_at) VALUES(?,?,?) ON CONFLICT(profile_id,command) DO UPDATE SET use_count=use_count+1,last_used_at=excluded.last_used_at`, id, command, usedAt); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM command_history WHERE profile_id=? AND id NOT IN (SELECT id FROM command_history WHERE profile_id=? ORDER BY last_used_at DESC,id DESC LIMIT 200)`, id, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ImportLegacy reads the Rust application's database without ever modifying it.
// All rows are validated before committing; repeated imports deduplicate by connection identity.
func (s *Store) ImportLegacy(filename string) (int, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return 0, err
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: "mode=ro"}
	source, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return 0, err
	}
	defer source.Close()
	rows, err := source.Query("PRAGMA table_info(server_profiles)")
	if err != nil {
		return 0, err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return 0, err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, name := range []string{"name", "host", "port", "username", "auth_kind"} {
		if !columns[name] {
			return 0, fmt.Errorf("不是有效的云桥数据库：缺少 %s", name)
		}
	}
	names := []string{"name", "host", "port", "username", "auth_kind", "key_path", "group_name", "remark", "secret", "last_connected_at", "os_id", "cpu_cores", "memory_bytes", "disk_bytes"}
	selected := make([]string, len(names))
	for i, name := range names {
		if columns[name] {
			selected[i] = name
		} else if i >= 11 {
			selected[i] = "0"
		} else {
			selected[i] = "''"
		}
	}
	rows, err = source.Query("SELECT " + strings.Join(selected, ",") + " FROM server_profiles")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type imported struct {
		p      model.Profile
		secret string
	}
	items := []imported{}
	for rows.Next() {
		var item imported
		p := &item.p
		if err = rows.Scan(&p.Name, &p.Host, &p.Port, &p.Username, &p.AuthKind, &p.KeyPath, &p.GroupName, &p.Remark, &item.secret, &p.LastConnectedAt, &p.OSID, &p.CPUCores, &p.MemoryBytes, &p.DiskBytes); err != nil {
			return 0, err
		}
		if err = Validate(p); err != nil {
			return 0, fmt.Errorf("连接 %q：%w", p.Name, err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count := 0
	for _, item := range items {
		p := item.p
		var exists int
		if err = tx.QueryRow("SELECT count(*) FROM server_profiles WHERE name=? AND host=? AND port=? AND username=?", p.Name, p.Host, p.Port, p.Username).Scan(&exists); err != nil {
			return 0, err
		}
		if exists > 0 {
			continue
		}
		if _, err = tx.Exec(`INSERT INTO server_profiles(name,host,port,username,auth_kind,key_path,group_name,remark,secret,last_connected_at,os_id,cpu_cores,memory_bytes,disk_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.Name, p.Host, p.Port, p.Username, p.AuthKind, p.KeyPath, p.GroupName, p.Remark, item.secret, p.LastConnectedAt, p.OSID, p.CPUCores, p.MemoryBytes, p.DiskBytes); err != nil {
			return 0, err
		}
		count++
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

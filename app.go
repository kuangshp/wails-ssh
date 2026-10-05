package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"wails-ssh/internal/localfiles"
	"wails-ssh/internal/model"
	"wails-ssh/internal/remotearchive"
	"wails-ssh/internal/sshclient"
	"wails-ssh/internal/storage"
	"wails-ssh/internal/transfer"
)

const version = "0.1.0"

type App struct {
	ctx          context.Context
	store        *storage.Store
	ssh          *sshclient.Manager
	transfers    *transfer.Service
	dataDir      string
	initErr      error
	shutdownOnce sync.Once
}

func NewApp() *App { return &App{} }
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	dir, err := os.UserConfigDir()
	if err != nil {
		a.initErr = err
		return
	}
	a.dataDir = filepath.Join(dir, "WailsSSH")
	a.store, err = storage.Open(filepath.Join(a.dataDir, "servers.db"))
	if err != nil {
		a.initErr = err
		return
	}
	emit := func(event string, args ...interface{}) {
		if ctx.Err() == nil {
			runtime.EventsEmit(ctx, event, args...)
		}
	}
	a.ssh = sshclient.New(filepath.Join(a.dataDir, "known_hosts"), emit)
	a.transfers = transfer.NewWithDialer(a.ssh.SFTP, func(id string) (transfer.ClientDialer, error) {
		dial, err := a.ssh.TransferDialer(id)
		return transfer.ClientDialer(dial), err
	}, emit, a.ssh.TransferScope)
}
func (a *App) shutdown(ctx context.Context) {
	a.shutdownOnce.Do(func() {
		if a.transfers != nil {
			a.transfers.Close()
		}
		if a.ssh != nil {
			a.ssh.Close()
		}
		if a.store != nil {
			a.store.Close()
		}
	})
}
func (a *App) ready() error {
	if a.initErr != nil {
		return a.initErr
	}
	if a.store == nil {
		return errors.New("应用尚未初始化")
	}
	return nil
}
func (a *App) GetAppInfo() (model.AppInfo, error) {
	if err := a.ready(); err != nil {
		return model.AppInfo{}, err
	}
	return model.AppInfo{Version: version, DataDir: a.dataDir}, nil
}
func (a *App) ListProfiles() ([]model.Profile, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.store.List()
}
func (a *App) SaveProfile(p model.Profile, secret string, keepSecret bool) (model.Profile, error) {
	if err := a.ready(); err != nil {
		return p, err
	}
	return a.store.Save(p, secret, keepSecret)
}
func (a *App) DeleteProfile(id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.store.Delete(id)
}
func (a *App) ListGroups() ([]string, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.store.Groups()
}
func (a *App) CreateGroup(name string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.store.CreateGroup(name)
}
func (a *App) CommandHistory(id int64) ([]string, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.store.History(id)
}
func (a *App) RecordCommand(id int64, command string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.store.RecordCommand(id, command)
}
func (a *App) PickPrivateKey() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择 PEM / OpenSSH 私钥", ShowHiddenFiles: true})
}
func (a *App) ListLocalDirectory(directory string) (localfiles.Directory, error) {
	return localfiles.ListDirectory(directory)
}
func (a *App) PickDownloadDirectory() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择下载位置", CanCreateDirectories: true})
}
func (a *App) ImportSSHConfig() ([]model.Profile, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	filename, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "导入 OpenSSH config", DefaultDirectory: filepath.Join(home, ".ssh"), ShowHiddenFiles: true})
	if err != nil {
		return nil, err
	}
	if filename == "" {
		return []model.Profile{}, nil
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	profiles, err := storage.ParseSSHConfig(file, home)
	if err != nil {
		return nil, err
	}
	return a.store.ImportProfiles(profiles)
}
func (a *App) ImportLegacyDatabase() (int, error) {
	if err := a.ready(); err != nil {
		return 0, err
	}
	filename, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "导入旧版云桥 servers.db", Filters: []runtime.FileFilter{{DisplayName: "SQLite 数据库", Pattern: "*.db;*.sqlite;*.sqlite3"}}})
	if err != nil || filename == "" {
		return 0, err
	}
	return a.store.ImportLegacy(filename)
}
func (a *App) Connect(profileID int64, secret string, cols, rows int) (model.Connection, error) {
	if err := a.ready(); err != nil {
		return model.Connection{}, err
	}
	p, err := a.store.Get(profileID)
	if err != nil {
		return model.Connection{}, err
	}
	if secret == "" {
		secret, err = a.store.Secret(profileID)
		if err != nil {
			return model.Connection{}, err
		}
	}
	connection, err := a.ssh.Connect(p, secret, cols, rows)
	if err != nil {
		return connection, err
	}
	if err = a.store.MarkConnected(profileID); err != nil {
		a.ssh.Disconnect(connection.ID)
		return model.Connection{}, err
	}
	go func() {
		facts, err := a.ssh.ReadSystemFacts(connection.ID)
		if err == nil && a.ctx.Err() == nil {
			if a.store.UpdateSystemFacts(profileID, facts.OSID, facts.CPUCores, facts.MemoryBytes, facts.DiskBytes) == nil {
				runtime.EventsEmit(a.ctx, "profile:updated", profileID)
			}
		}
	}()
	return connection, nil
}

// TestConnection tests the current editor values without saving them. An existing
// secret is read only when the editor explicitly requests preserving it.
func (a *App) TestConnection(p model.Profile, secret string, keepSecret bool) error {
	if err := a.ready(); err != nil {
		return err
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = "连接测试"
	}
	if err := storage.Validate(&p); err != nil {
		return err
	}
	secret, err := a.testSecret(p, secret, keepSecret)
	if err != nil {
		return err
	}
	if p.AuthKind == "password" && secret == "" {
		return errors.New("请先填写登录密码")
	}
	return a.ssh.TestConnection(p, secret)
}

func (a *App) testSecret(p model.Profile, secret string, keepSecret bool) (string, error) {
	if secret != "" || !keepSecret || p.ID == 0 {
		return secret, nil
	}
	saved, err := a.store.Get(p.ID)
	if err != nil {
		return "", err
	}
	if saved.AuthKind != p.AuthKind || (p.AuthKind == "private_key" && saved.KeyPath != p.KeyPath) {
		return "", nil
	}
	return a.store.Secret(p.ID)
}

// CopyProfileSecret keeps saved credentials out of frontend bridge responses.
func (a *App) CopyProfileSecret(profileID int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	secret, err := a.store.Secret(profileID)
	if err != nil {
		return err
	}
	if secret == "" {
		return errors.New("该连接没有保存密码或私钥口令")
	}
	return runtime.ClipboardSetText(a.ctx, secret)
}

// RevealProfileSecret is used only by the editor's explicit reveal action.
// Normal profile responses continue to contain only HasSecret.
func (a *App) RevealProfileSecret(profileID int64) (string, error) {
	_, secret, err := a.profileCredentials(profileID)
	return secret, err
}

// CopyProfileCredentials assembles the saved connection details in the backend;
// copying does not send its password or private-key passphrase to the frontend.
func (a *App) CopyProfileCredentials(profileID int64) error {
	p, secret, err := a.profileCredentials(profileID)
	if err != nil {
		return err
	}
	return runtime.ClipboardSetText(a.ctx, formatProfileCredentials(p, secret))
}

func (a *App) profileCredentials(profileID int64) (model.Profile, string, error) {
	if err := a.ready(); err != nil {
		return model.Profile{}, "", err
	}
	p, err := a.store.Get(profileID)
	if err != nil {
		return model.Profile{}, "", err
	}
	secret, err := a.store.Secret(profileID)
	if err != nil {
		return model.Profile{}, "", err
	}
	if secret == "" {
		if p.AuthKind == "private_key" {
			return model.Profile{}, "", errors.New("该连接没有保存私钥口令")
		}
		return model.Profile{}, "", errors.New("该连接没有保存密码")
	}
	return p, secret, nil
}

func formatProfileCredentials(p model.Profile, secret string) string {
	label := "密码"
	if p.AuthKind == "private_key" {
		label = "私钥口令"
	}
	return fmt.Sprintf("IP: %s\n端口: %d\n账号: %s\n%s: %s", p.Host, p.Port, p.Username, label, secret)
}

func (a *App) StartTerminal(id string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.ssh.Start(id)
}
func (a *App) Disconnect(id string) error {
	if err := a.ready(); err != nil {
		return err
	}
	a.transfers.CancelSession(id)
	return a.ssh.Disconnect(id)
}
func (a *App) WriteTerminal(id, data string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.ssh.Write(id, data)
}
func (a *App) ResizeTerminal(id string, cols, rows int) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.ssh.Resize(id, cols, rows)
}
func (a *App) TrustHost(host string, port int, key string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.ssh.TrustHost(host, port, key)
}
func (a *App) ListRemote(id, path string) (model.Directory, error) {
	if err := a.ready(); err != nil {
		return model.Directory{}, err
	}
	return a.transfers.List(id, path)
}
func (a *App) CreateRemote(id, path string, isDir bool) error {
	if err := a.ready(); err != nil {
		return err
	}
	if isDir {
		return a.transfers.Mkdir(id, path)
	}
	return a.transfers.CreateFile(id, path)
}
func (a *App) RenameRemote(id, oldPath, newPath string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.transfers.Rename(id, oldPath, newPath)
}
func (a *App) ChmodRemote(id, path, mode string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.transfers.Chmod(id, path, mode)
}
func (a *App) DeleteRemote(id, path string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.transfers.Delete(id, path)
}
func (a *App) Upload(id string, sources []string, remoteDir string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	return a.transfers.Upload(id, sources, remoteDir)
}
func (a *App) Download(id, remotePath, localDir string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	return a.transfers.Download(id, remotePath, localDir)
}
func (a *App) CancelTransfer(id string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.transfers.Cancel(id)
}
func (a *App) ResolveConflict(id, choice string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.transfers.ResolveConflict(id, choice)
}
func (a *App) CompressRemote(id, path string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	client, err := a.ssh.SSH(id)
	if err != nil {
		return "", err
	}
	return remotearchive.Run(a.ctx, client, path, true)
}
func (a *App) ExtractRemote(id, path string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	client, err := a.ssh.SSH(id)
	if err != nil {
		return "", err
	}
	return remotearchive.Run(a.ctx, client, path, false)
}

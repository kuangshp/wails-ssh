package transfer

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type targetLock struct {
	token chan struct{}
	refs  int
}

// Only writes to the exact same destination wait on one another. The map mutex
// is held for bookkeeping, never for file I/O, conflicts, or network retries.
func (s *Service) lockTarget(ctx context.Context, key string) (func(), error) {
	s.targetMu.Lock()
	lock := s.targets[key]
	if lock == nil {
		lock = &targetLock{token: make(chan struct{}, 1)}
		s.targets[key] = lock
	}
	lock.refs++
	s.targetMu.Unlock()
	releaseRef := func() {
		s.targetMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.targets, key)
		}
		s.targetMu.Unlock()
	}
	select {
	case lock.token <- struct{}{}:
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() { once.Do(func() { <-lock.token; releaseRef() }) }, nil
}

func remoteTarget(scope, name string) string { return "remote\x00" + scope + "\x00" + path.Clean(name) }
func localTarget(root, name string) string {
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	return "local\x00" + localLockName(filepath.Join(root, name), runtime.GOOS)
}

// macOS and Windows commonly use case-insensitive volumes. Fold only the lock
// key, never the actual filename, so REPORT/report and decomposed Unicode names
// cannot bypass serialization. On a case-sensitive volume this conservatively
// serializes those aliases too; both distinct files still retain their names.
func localLockName(name, platform string) string {
	if platform != "darwin" && platform != "windows" {
		return name
	}
	if platform == "windows" {
		parts := strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' })
		for i := range parts {
			parts[i] = strings.TrimRight(parts[i], " .")
		}
		name = strings.Join(parts, "/")
	}
	return cases.Fold().String(norm.NFC.String(name))
}

// Not all SFTP servers resolve symlinks in REALPATH (pkg/sftp's server only
// makes paths absolute). Resolve directory ancestors explicitly and cache the
// result during this job's directory setup, without following a destination
// file or accepting a symlink in place of a requested destination directory.
func canonicalRemoteDirectory(client *sftp.Client, name string, cache map[string]string, depth int) (string, error) {
	if depth > 40 {
		return "", fmt.Errorf("too many symbolic links in remote directory: %s", name)
	}
	if canonical, ok := cache[path.Clean(name)]; ok {
		return canonical, nil
	}
	absolute, err := client.RealPath(name)
	if err != nil {
		return "", err
	}
	if !path.IsAbs(absolute) {
		return "", fmt.Errorf("server returned a non-absolute directory: %s", absolute)
	}
	if canonical, ok := cache[absolute]; ok {
		return canonical, nil
	}
	resolved, original := "/", "/"
	for _, component := range strings.Split(strings.Trim(path.Clean(absolute), "/"), "/") {
		if component == "" {
			continue
		}
		original = path.Join(original, component)
		if canonical, ok := cache[original]; ok {
			resolved = canonical
			continue
		}
		candidate := path.Join(resolved, component)
		info, err := client.Lstat(candidate)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := client.ReadLink(candidate)
			if err != nil {
				return "", err
			}
			if !path.IsAbs(link) {
				link = path.Join(path.Dir(candidate), link)
			}
			resolved, err = canonicalRemoteDirectory(client, link, cache, depth+1)
			if err != nil {
				return "", err
			}
		} else {
			if !info.IsDir() {
				return "", fmt.Errorf("remote ancestor is not a directory: %s", candidate)
			}
			resolved = candidate
		}
		cache[original] = resolved
	}
	cache[absolute], cache[path.Clean(name)] = resolved, resolved
	return resolved, nil
}

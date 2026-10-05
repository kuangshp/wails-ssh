package transfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/pkg/sftp"
)

type uploadFile struct {
	root          *os.Root
	local, remote string
	size          int64
	source        string
	info          os.FileInfo
	mode          os.FileMode
}

type uploadDirectory struct {
	root  *os.Root
	local string
	info  os.FileInfo
}

func collectUpload(ctx context.Context, root *os.Root, local, remote string, dirs *[]string, files *[]uploadFile, sourceDirs ...*[]uploadDirectory) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := root.Lstat(local)
	if err != nil {
		return fmt.Errorf("无法读取本地上传路径 %q，请确认文件存在且可读取: %w", filepath.Join(root.Name(), local), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symbolic links are not transferred: %s", local)
	}
	if info.IsDir() {
		*dirs = append(*dirs, remote)
		if len(sourceDirs) > 0 {
			*sourceDirs[0] = append(*sourceDirs[0], uploadDirectory{root, local, info})
		}
		dir, err := root.Open(local)
		if err != nil {
			return fmt.Errorf("无法读取本地上传目录 %q: %w", filepath.Join(root.Name(), local), err)
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !safeName(entry.Name()) {
				return fmt.Errorf("unsafe local filename: %q", entry.Name())
			}
			if err := collectUpload(ctx, root, filepath.Join(local, entry.Name()), path.Join(remote, entry.Name()), dirs, files, sourceDirs...); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("only regular files and directories can be transferred: %s", local)
	}
	*files = append(*files, uploadFile{root: root, local: local, remote: remote, size: info.Size(), source: filepath.Join(root.Name(), local), info: info})
	return nil
}

func (s *Service) parallelUpload(j *job, sources []string, remoteDir string) error {
	j.progress.Status = "preparing"
	s.report(j, true)
	var roots []*os.Root
	defer func() {
		for _, root := range roots {
			root.Close()
		}
	}()
	directories, files := []string{remoteDir}, []uploadFile{}
	var sourceDirectories []uploadDirectory
	topSources := make(map[string]string, len(sources))
	rootByDir := make(map[string]*os.Root)
	for _, source := range sources {
		absolute, err := filepath.Abs(source)
		if err != nil {
			return err
		}
		target := path.Join(remoteDir, filepath.Base(absolute))
		if previous, exists := topSources[target]; exists && previous != absolute {
			return fmt.Errorf("multiple local sources have the same remote destination: %s (%s, %s)", target, previous, absolute)
		}
		topSources[target] = absolute
		root := rootByDir[filepath.Dir(absolute)]
		if root == nil {
			root, err = os.OpenRoot(filepath.Dir(absolute))
			if err != nil {
				return err
			}
			rootByDir[filepath.Dir(absolute)] = root
			roots = append(roots, root)
		}
		if err := collectUpload(j.ctx, root, filepath.Base(absolute), path.Join(remoteDir, filepath.Base(absolute)), &directories, &files, &sourceDirectories); err != nil {
			return err
		}
	}
	// Reject distinct sources mapping to the same remote name before creating
	// any directories or opening a transport. Silently choosing one loses data.
	targets := make(map[string]string, len(files))
	for _, file := range files {
		source := filepath.Join(file.root.Name(), file.local)
		if previous, exists := targets[file.remote]; exists && previous != source {
			return fmt.Errorf("multiple local sources have the same remote destination: %s (%s, %s)", file.remote, previous, source)
		}
		targets[file.remote] = source
	}
	// Freeze the selected batch before touching the server. A build tool may
	// replace assets while queued files wait for a worker or an overwrite prompt.
	prepared, release, err := snapshotUpload(j.ctx, files)
	if err != nil {
		return err
	}
	defer release()
	for _, dir := range sourceDirectories {
		if err := j.ctx.Err(); err != nil {
			return err
		}
		current, err := dir.root.Lstat(dir.local)
		if err != nil || !current.IsDir() || !os.SameFile(dir.info, current) || !dir.info.ModTime().Equal(current.ModTime()) {
			return fmt.Errorf("准备上传时本地目录发生变化，请等待文件生成完成后重试: %s", filepath.Join(dir.root.Name(), dir.local))
		}
	}
	files = prepared
	canonicalDirs := make(map[string]string, len(directories))
	if err := s.retry(j, func(client *sftp.Client) error {
		for _, dir := range directories {
			if err := j.ctx.Err(); err != nil {
				return err
			}
			canonical, err := s.ensureRemoteDirectory(j, client, dir, canonicalDirs)
			if err != nil {
				return err
			}
			canonicalDirs[path.Clean(dir)] = canonical
		}
		return nil
	}); err != nil {
		return err
	}
	jobs := make([]transferFile, 0, len(files))
	for _, file := range files {
		canonical := path.Join(canonicalDirs[path.Dir(file.remote)], path.Base(file.remote))
		// Use the same fixed address for bookkeeping, locking and every retry.
		// An ancestor symlink may change after directory preparation finishes.
		jobs = append(jobs, transferFile{path: canonical, size: file.size, target: remoteTarget(j.scope, canonical), run: func(child *job, client *sftp.Client) error {
			child.sourcePath = file.source
			mode := file.mode
			child.uploadMode = &mode
			return s.uploadPath(child, client, file.root, file.local, canonical)
		}})
	}
	return s.parallelFiles(j, jobs)
}

type transferFile struct {
	path, target string
	size         int64
	run          func(*job, *sftp.Client) error
	cleanup      func(*job, error)
}

func (s *Service) parallelFiles(j *job, files []transferFile) error {
	queue := make(chan transferFile, len(files))
	for _, file := range files {
		if _, exists := j.files[file.path]; exists {
			continue
		}
		j.files[file.path] = Progress{Path: file.path, Total: file.size, Status: "queued"}
		j.progress.Total += file.size
		queue <- file
	}
	close(queue)
	var wg sync.WaitGroup
	var firstError error
	var errorMu sync.Mutex
	template := j.progress
	workerCtx, stopWorkers := context.WithCancel(j.ctx)
	defer stopWorkers()
	for range min(4, len(j.files)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A peer failure stops work at safe boundaries, but must not cut off
			// another file's rename acknowledgement. Only explicit job cancellation
			// owns the transport lifetime; child operations use the soft-stop context.
			worker := &workerConnection{ctx: j.ctx, dial: j.dial}
			defer worker.close()
			for file := range queue {
				errorMu.Lock()
				failed := firstError != nil
				errorMu.Unlock()
				if failed || j.ctx.Err() != nil {
					return
				}
				progress := template
				progress.Path, progress.Status, progress.Total, progress.Transferred = file.path, "running", file.size, 0
				child := newJob(workerCtx, j.cancel, progress, j.dial)
				child.parent = j
				child.scope = j.scope
				unlock, err := s.lockTarget(workerCtx, file.target)
				if err == nil {
					err = s.retryOn(child, worker, func(client *sftp.Client) error {
						return file.run(child, client)
					})
					unlock()
				}
				if file.cleanup != nil {
					file.cleanup(child, err)
				}
				child.progress.Status = "completed"
				if child.skipped > 0 {
					child.progress.Status = "skipped"
				}
				if err != nil {
					child.progress.Status, child.progress.Error = "failed", err.Error()
				}
				s.report(child, true)
				j.mu.Lock()
				j.copied += child.copied
				j.skipped += child.skipped
				j.mu.Unlock()
				if err != nil {
					errorMu.Lock()
					if firstError == nil {
						firstError = err
						stopWorkers()
					} else if uncertainCommit(err) {
						// Preserve every ambiguous commit location even when another
						// worker failed first and cancelled its peers.
						firstError = errors.Join(firstError, err)
					}
					errorMu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	if j.ctx.Err() != nil && !uncertainCommit(firstError) {
		return j.ctx.Err()
	}
	return firstError
}

func (s *Service) ensureRemoteDirectory(j *job, client *sftp.Client, dir string, cache map[string]string) (string, error) {
	parent, err := canonicalRemoteDirectory(client, path.Dir(dir), cache, 0)
	if err != nil {
		return "", err
	}
	canonical := path.Join(parent, path.Base(dir))
	unlock, err := s.lockTarget(j.ctx, remoteTarget(j.scope, canonical))
	if err != nil {
		return "", err
	}
	defer unlock()
	info, err := client.Lstat(canonical)
	if os.IsNotExist(err) {
		if err = client.Mkdir(canonical); err == nil {
			return canonical, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
		info, err = client.Lstat(canonical)
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("destination is not a regular directory: %s", dir)
	}
	return canonical, nil
}

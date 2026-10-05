package transfer

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/pkg/sftp"
)

type downloadFile struct {
	remote, local string
	size          int64
}

func collectDownload(ctx context.Context, client *sftp.Client, remote, local string, info os.FileInfo, directories *[]string, files *[]downloadFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symbolic links are not transferred: %s", remote)
	}
	if info.IsDir() {
		*directories = append(*directories, local)
		entries, err := client.ReadDir(remote)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !safeName(entry.Name()) {
				return fmt.Errorf("unsafe remote filename: %q", entry.Name())
			}
			if err := collectDownload(ctx, client, path.Join(remote, entry.Name()), filepath.Join(local, entry.Name()), entry, directories, files); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("only regular files and directories can be transferred: %s", remote)
	}
	*files = append(*files, downloadFile{remote, local, info.Size()})
	return nil
}

func (s *Service) parallelDownload(j *job, remote, localDir, name string) error {
	// os.Root keeps every destination and checkpoint below the selected folder.
	root, err := os.OpenRoot(localDir)
	if err != nil {
		return err
	}
	defer root.Close()
	var directories []string
	var files []downloadFile
	if err := s.retry(j, func(client *sftp.Client) error {
		directories, files = nil, nil
		info, err := client.Lstat(remote)
		if err != nil {
			return err
		}
		return collectDownload(j.ctx, client, remote, name, info, &directories, &files)
	}); err != nil {
		return err
	}
	for _, dir := range directories {
		if err := s.ensureLocalDirectory(j.ctx, root, dir); err != nil {
			return err
		}
	}
	jobs := make([]transferFile, 0, len(files))
	for _, file := range files {
		jobs = append(jobs, transferFile{path: file.remote, size: file.size, target: localTarget(root.Name(), file.local),
			run: func(child *job, client *sftp.Client) error {
				return s.downloadPath(child, client, file.remote, root, file.local)
			},
			cleanup: func(child *job, err error) {
				if !uncertainCommit(err) {
					for _, temp := range child.temps {
						_ = root.Remove(temp)
					}
				}
			},
		})
	}
	return s.parallelFiles(j, jobs)
}

func (s *Service) ensureLocalDirectory(ctx context.Context, root *os.Root, dir string) error {
	unlock, err := s.lockTarget(ctx, localTarget(root.Name(), dir))
	if err != nil {
		return err
	}
	defer unlock()
	info, err := root.Lstat(dir)
	if os.IsNotExist(err) {
		if err = root.Mkdir(dir, 0755); err == nil {
			return nil
		}
		if !os.IsExist(err) {
			return err
		}
		info, err = root.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination is not a regular directory: %s", dir)
	}
	return nil
}

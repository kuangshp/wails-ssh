package transfer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
)

func (s *Service) Upload(sessionID string, sources []string, remoteDir string) (string, error) {
	if len(sources) == 0 {
		return "", errors.New("select at least one file or directory")
	}
	if strings.TrimSpace(remoteDir) == "" {
		return "", errors.New("remote destination is required")
	}
	if !path.IsAbs(remoteDir) {
		return "", errors.New("上传目标必须是绝对路径，例如 /home/app；可点击“使用当前目录”选择")
	}
	sources = append([]string(nil), sources...)
	for _, source := range sources {
		if source == "" || !safeName(filepath.Base(filepath.Clean(source))) {
			return "", fmt.Errorf("invalid source path: %q", source)
		}
	}
	return s.start(sessionID, "upload", remoteDir, func(j *job) error {
		return s.parallelUpload(j, sources, remoteDir)
	})
}

func (s *Service) Download(sessionID, remotePath, localDir string) (string, error) {
	if strings.TrimSpace(remotePath) == "" || strings.TrimSpace(localDir) == "" {
		return "", errors.New("remote source and local destination are required")
	}
	name := path.Base(path.Clean(remotePath))
	if !safeName(name) {
		return "", errors.New("select a named remote file or directory")
	}
	return s.start(sessionID, "download", remotePath, func(j *job) error {
		return s.parallelDownload(j, remotePath, localDir, name)
	})
}

func (s *Service) overwrite(j *job, source, target string) (bool, error) {
	ctx := j.ctx
	if j.parent != nil {
		j = j.parent
	}
	j.conflictMu.Lock()
	defer j.conflictMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if j.policy != "" {
		return j.policy == "overwrite", nil
	}
	reply := make(chan string, 1)
	j.mu.Lock()
	j.pending = reply
	j.mu.Unlock()
	defer func() { j.mu.Lock(); j.pending = nil; j.mu.Unlock() }()
	s.emit("transfer:conflict", Conflict{ID: j.progress.ID, SessionID: j.progress.SessionID, Source: source, Target: target})
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case choice := <-reply:
		switch choice {
		case "overwrite_all":
			j.policy = "overwrite"
			return true, nil
		case "skip_all":
			j.policy = "skip"
			return false, nil
		case "overwrite":
			return true, nil
		case "cancel":
			j.cancel()
			return false, context.Canceled
		default:
			return false, nil
		}
	}
}

func (s *Service) uploadPath(j *job, client *sftp.Client, root *os.Root, local, remote string) (resultErr error) {
	if err := j.ctx.Err(); err != nil {
		return err
	}
	if j.completed[remote] {
		return nil
	}
	info, err := root.Lstat(local)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symbolic links are not transferred: %s", local)
	}
	if j.parent != nil && info.IsDir() {
		return errors.New("local source changed from a file to a directory during upload")
	}
	if info.IsDir() {
		destInfo, err := client.Lstat(remote)
		if os.IsNotExist(err) {
			if err := client.Mkdir(remote); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if !destInfo.IsDir() || destInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination is not a regular directory: %s", remote)
		}
		dir, err := root.Open(local)
		if err != nil {
			return err
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
			if err := s.uploadPath(j, client, root, filepath.Join(local, entry.Name()), path.Join(remote, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("only regular files and directories can be transferred: %s", local)
	}
	defer func() {
		if resultErr != nil && !retryable(resultErr) && !uncertainCommit(resultErr) {
			if temp := j.temps[remote]; temp != "" {
				_ = client.Remove(temp)
			}
		}
	}()
	version := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	if previous, ok := j.sourceVersions[remote]; ok && previous != version {
		return errors.New("local source changed while resuming upload")
	}
	j.sourceVersions[remote] = version
	destInfo, err := client.Lstat(remote)
	approvedOverwrite := j.approved[remote]
	if err == nil {
		if !destInfo.Mode().IsRegular() {
			return fmt.Errorf("destination is not a regular file: %s", remote)
		}
		allow := approvedOverwrite
		if !allow {
			source := j.sourcePath
			if source == "" {
				source = filepath.Join(root.Name(), local)
			}
			allow, err = s.overwrite(j, source, remote)
		}
		if err != nil {
			return err
		}
		if !allow {
			j.skipped++
			j.completed[remote] = true
			return nil
		}
		approvedOverwrite = true
		j.approved[remote] = true
	} else if !os.IsNotExist(err) {
		return err
	}
	input, err := root.Open(local)
	if err != nil {
		return err
	}
	defer input.Close()
	temp, resuming := j.temps[remote]
	if !resuming {
		temp = path.Join(path.Dir(remote), ".wails-ssh-"+uniqueID()+".part")
		j.temps[remote] = temp
	}
	offset := int64(0)
	partialSize := int64(0)
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if resuming {
		partial, err := client.Lstat(temp)
		if err == nil {
			if !partial.Mode().IsRegular() {
				return errors.New("upload checkpoint is not a regular file")
			}
			partialSize = partial.Size()
			// Concurrent WRITE requests can leave later blocks beyond a failed
			// block. Only the fully acknowledged prefix belongs to this job.
			offset = min(partialSize, j.uploadOffsets[remote])
			flags = os.O_WRONLY
			if partialSize > info.Size() {
				return errors.New("upload checkpoint exceeds source size")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	output, err := client.OpenFile(temp, flags)
	if err != nil {
		return err
	}
	if partialSize > offset {
		if err := output.Truncate(offset); err != nil {
			output.Close()
			return err
		}
	}
	j.uploadOffsets[remote] = offset
	if _, err := input.Seek(offset, io.SeekStart); err != nil {
		output.Close()
		return err
	}
	if _, err := output.Seek(offset, io.SeekStart); err != nil {
		output.Close()
		return err
	}
	j.progress.Path, j.progress.Total, j.progress.Transferred = remote, info.Size(), offset
	s.report(j, true)
	err = s.copyUpload(j, output, input, remote)
	closeErr := output.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	mode := info.Mode().Perm()
	if j.uploadMode != nil {
		mode = *j.uploadMode
	}
	if err := client.Chmod(temp, mode); err != nil {
		return err
	}
	if err := j.ctx.Err(); err != nil {
		return err
	}
	current, err := root.Lstat(local)
	if err != nil {
		return err
	}
	opened, err := input.Stat()
	if err != nil {
		return err
	}
	if !sameSourceVersion(info, current) || !sameSourceVersion(info, opened) || !os.SameFile(info, current) || !os.SameFile(info, opened) {
		return errors.New("local source changed during upload; original destination was preserved")
	}
	if err := j.ctx.Err(); err != nil {
		return err
	}
	if err := replaceRemote(client, temp, remote, approvedOverwrite); err != nil {
		return err
	}
	j.copied++
	j.completed[remote] = true
	return nil
}

func (s *Service) downloadPath(j *job, client *sftp.Client, remote string, root *os.Root, local string) (resultErr error) {
	if err := j.ctx.Err(); err != nil {
		return err
	}
	if j.completed[remote] {
		return nil
	}
	info, err := client.Lstat(remote)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symbolic links are not transferred: %s", remote)
	}
	if j.parent != nil && info.IsDir() {
		return errors.New("remote source changed from a file to a directory during download")
	}
	if info.IsDir() {
		destInfo, err := root.Lstat(local)
		if os.IsNotExist(err) {
			if err := root.Mkdir(local, 0755); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if !destInfo.IsDir() || destInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination is not a regular directory: %s", local)
		}
		entries, err := client.ReadDir(remote)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !safeName(entry.Name()) {
				return fmt.Errorf("unsafe remote filename: %q", entry.Name())
			}
			if err := s.downloadPath(j, client, path.Join(remote, entry.Name()), root, filepath.Join(local, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("only regular files and directories can be transferred: %s", remote)
	}
	defer func() {
		if resultErr != nil && !retryable(resultErr) && !uncertainCommit(resultErr) {
			if temp := j.temps[remote]; temp != "" {
				_ = root.Remove(temp)
			}
		}
	}()
	version := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	if previous, ok := j.sourceVersions[remote]; ok && previous != version {
		return errors.New("remote source changed while resuming download")
	}
	j.sourceVersions[remote] = version
	destInfo, err := root.Lstat(local)
	approvedOverwrite := j.approved[remote]
	if err == nil {
		if !destInfo.Mode().IsRegular() {
			return fmt.Errorf("destination is not a regular file: %s", local)
		}
		allow := approvedOverwrite
		if !allow {
			allow, err = s.overwrite(j, remote, filepath.Join(root.Name(), local))
		}
		if err != nil {
			return err
		}
		if !allow {
			j.skipped++
			j.completed[remote] = true
			return nil
		}
		approvedOverwrite = true
		j.approved[remote] = true
	} else if !os.IsNotExist(err) {
		return err
	}
	input, err := client.Open(remote)
	if err != nil {
		return err
	}
	defer input.Close()
	temp, resuming := j.temps[remote]
	if !resuming {
		temp = filepath.Join(filepath.Dir(local), ".wails-ssh-"+uniqueID()+".part")
		j.temps[remote] = temp
	}
	offset := int64(0)
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if resuming {
		partial, err := root.Lstat(temp)
		if err == nil {
			if !partial.Mode().IsRegular() {
				return errors.New("download checkpoint is not a regular file")
			}
			offset, flags = partial.Size(), os.O_WRONLY
			if offset > info.Size() {
				return errors.New("download checkpoint exceeds source size")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	output, err := root.OpenFile(temp, flags, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		if !retryable(resultErr) && !uncertainCommit(resultErr) {
			_ = root.Remove(temp)
		}
	}()
	if _, err := input.Seek(offset, io.SeekStart); err != nil {
		output.Close()
		return err
	}
	if _, err := output.Seek(offset, io.SeekStart); err != nil {
		output.Close()
		return err
	}
	j.progress.Path, j.progress.Total, j.progress.Transferred = remote, info.Size(), offset
	s.report(j, true)
	err = s.copy(j, output, input)
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := j.ctx.Err(); err != nil {
		return err
	}
	current, err := client.Lstat(remote)
	if err != nil {
		return err
	}
	opened, err := input.Stat()
	if err != nil {
		return err
	}
	if !sameSourceVersion(info, current) || !sameSourceVersion(info, opened) {
		return errors.New("remote source changed during download; original destination was preserved")
	}
	// SFTP v3 exposes modification times only to the second. A same-size remote
	// edit can otherwise pass the stat checks and commit a mixture of versions.
	// Verify the completed checkpoint against a fresh source read before rename.
	if err := verifyDownloadedContent(j.ctx, client, remote, root, temp); err != nil {
		return err
	}
	if err := replaceLocal(root, temp, local, approvedOverwrite); err != nil {
		return err
	}
	j.copied++
	j.completed[remote] = true
	return nil
}

func sameSourceVersion(before, after os.FileInfo) bool {
	return before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && after.Mode().IsRegular()
}

func verifyDownloadedContent(ctx context.Context, client *sftp.Client, remote string, root *os.Root, temp string) error {
	source, err := client.Open(remote)
	if err != nil {
		return err
	}
	defer source.Close()
	partial, err := root.Open(temp)
	if err != nil {
		return err
	}
	defer partial.Close()
	sourceHash, err := contentHash(ctx, source)
	if err != nil {
		return err
	}
	partialHash, err := contentHash(ctx, partial)
	if err != nil {
		return err
	}
	if sourceHash != partialHash {
		return errors.New("remote source content changed during download; original destination was preserved")
	}
	return ctx.Err()
}

func contentHash(ctx context.Context, source io.Reader) ([sha256.Size]byte, error) {
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return [sha256.Size]byte{}, err
		}
		n, err := source.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if err == io.EOF {
			return [sha256.Size]byte(hash.Sum(nil)), nil
		}
		if err != nil {
			return [sha256.Size]byte{}, err
		}
	}
}

func (s *Service) copy(j *job, dst io.Writer, src io.Reader) error {
	buffer := make([]byte, 128*1024)
	for {
		if err := j.ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if err := j.ctx.Err(); err != nil {
				return err
			}
			written, err := dst.Write(buffer[:n])
			j.progress.Transferred += int64(written)
			s.report(j, false)
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if j.progress.Transferred != j.progress.Total {
		return fmt.Errorf("%w: expected %d bytes, received %d", io.ErrUnexpectedEOF, j.progress.Total, j.progress.Transferred)
	}
	s.report(j, true)
	return j.ctx.Err()
}

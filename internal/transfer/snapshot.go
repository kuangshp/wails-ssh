package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var errUploadSourceChanged = errors.New("本地上传源已变化")

const snapshotBufferSize = 128 * 1024

type snapshotCopier func(context.Context, io.Writer, io.Reader, []byte) (int64, error)

// snapshotUpload owns its returned files until cleanup is called. No remote
// operations should begin until the entire batch and final source check pass.
// Owned files stay readable and private (0600). Original destination permissions
// travel separately in uploadFile.mode, never on the temporary local files.
func snapshotUpload(ctx context.Context, files []uploadFile) ([]uploadFile, func(), error) {
	return snapshotUploadWithCopy(ctx, files, copySnapshot)
}

func snapshotUploadWithCopy(ctx context.Context, files []uploadFile, copyFile snapshotCopier) (_ []uploadFile, _ func(), resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return []uploadFile{}, func() {}, nil
	}
	for _, file := range files {
		if file.size < 0 {
			return nil, nil, changedSnapshotSource(file)
		}
	}
	directory, err := os.MkdirTemp("", "wails-ssh-upload-")
	if err != nil {
		return nil, nil, fmt.Errorf("无法创建本地上传快照目录：%w", err)
	}
	var root *os.Root
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if root != nil {
				_ = root.Close()
			}
			_ = os.RemoveAll(directory)
		})
	}
	defer func() {
		if resultErr != nil {
			cleanup()
		}
	}()
	if err := os.Chmod(directory, 0700); err != nil {
		return nil, nil, fmt.Errorf("无法保护本地上传快照目录：%w", err)
	}
	root, err = os.OpenRoot(directory)
	if err != nil {
		return nil, nil, fmt.Errorf("无法打开本地上传快照目录：%w", err)
	}
	buffer := make([]byte, snapshotBufferSize)
	snapshots := make([]uploadFile, 0, len(files))
	for index, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := fmt.Sprintf("%08d", index)
		snapshot, err := snapshotUploadFile(ctx, file, root, name, buffer, copyFile)
		if err != nil {
			return nil, nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	// A build may replace an early source while a later file is being copied.
	// Check every original once more before permitting the first remote write.
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := verifySnapshotSource(file); err != nil {
			return nil, nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return snapshots, cleanup, nil
}

func snapshotSourceName(file uploadFile) string {
	if file.source != "" {
		return file.source
	}
	if file.root != nil {
		return filepath.Join(file.root.Name(), file.local)
	}
	return file.local
}

func changedSnapshotSource(file uploadFile) error {
	return fmt.Errorf("%w：%s；文件在准备上传期间被修改或替换，请等待构建完成后重试", errUploadSourceChanged, snapshotSourceName(file))
}

func snapshotSourceError(file uploadFile, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w：%s；本地文件不存在或已被删除，请等待构建完成后重试：%w", errUploadSourceChanged, snapshotSourceName(file), err)
	}
	return fmt.Errorf("无法读取本地上传源文件 %s：%w", snapshotSourceName(file), err)
}

func sameSnapshotSource(file uploadFile, current os.FileInfo) bool {
	return file.info != nil && current != nil && file.info.Mode().IsRegular() &&
		file.info.Size() == file.size && sameSourceVersion(file.info, current) &&
		file.info.Mode().Perm() == current.Mode().Perm() && os.SameFile(file.info, current)
}

func verifySnapshotSource(file uploadFile) error {
	if file.root == nil || file.info == nil {
		return changedSnapshotSource(file)
	}
	current, err := file.root.Lstat(file.local)
	if err != nil {
		return snapshotSourceError(file, err)
	}
	if !sameSnapshotSource(file, current) {
		return changedSnapshotSource(file)
	}
	return nil
}

func snapshotUploadFile(ctx context.Context, file uploadFile, root *os.Root, name string, buffer []byte, copyFile snapshotCopier) (uploadFile, error) {
	if err := verifySnapshotSource(file); err != nil {
		return uploadFile{}, err
	}
	input, err := file.root.Open(file.local)
	if err != nil {
		return uploadFile{}, snapshotSourceError(file, err)
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil {
		return uploadFile{}, snapshotSourceError(file, err)
	}
	if !sameSnapshotSource(file, opened) {
		return uploadFile{}, changedSnapshotSource(file)
	}
	output, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return uploadFile{}, fmt.Errorf("无法为 %s 创建本地上传快照：%w", snapshotSourceName(file), err)
	}
	defer output.Close()
	if err := output.Chmod(0600); err != nil {
		return uploadFile{}, fmt.Errorf("无法保护本地上传快照 %s：%w", snapshotSourceName(file), err)
	}
	// A live log can keep growing faster than we read it. Bound staging to the
	// scanned size plus one byte so growth is rejected without filling the disk.
	limit := file.size
	if limit < int64(1<<63-1) {
		limit++
	}
	copied, err := copyFile(ctx, output, io.LimitReader(input, limit), buffer)
	if err != nil {
		return uploadFile{}, fmt.Errorf("复制本地上传快照失败 %s：%w", snapshotSourceName(file), err)
	}
	if err := ctx.Err(); err != nil {
		return uploadFile{}, err
	}
	if copied != file.size {
		return uploadFile{}, changedSnapshotSource(file)
	}
	opened, err = input.Stat()
	if err != nil {
		return uploadFile{}, snapshotSourceError(file, err)
	}
	if !sameSnapshotSource(file, opened) {
		return uploadFile{}, changedSnapshotSource(file)
	}
	if err := verifySnapshotSource(file); err != nil {
		return uploadFile{}, err
	}
	snapshotInfo, err := output.Stat()
	if err != nil {
		return uploadFile{}, fmt.Errorf("无法验证本地上传快照 %s：%w", snapshotSourceName(file), err)
	}
	if snapshotInfo.Size() != file.size {
		return uploadFile{}, fmt.Errorf("本地上传快照大小不一致：%s", snapshotSourceName(file))
	}
	if err := output.Close(); err != nil {
		return uploadFile{}, fmt.Errorf("无法完成本地上传快照 %s：%w", snapshotSourceName(file), err)
	}
	return snapshotFileMetadata(file, root, name, snapshotInfo), nil
}

func snapshotFileMetadata(source uploadFile, root *os.Root, name string, info os.FileInfo) uploadFile {
	snapshot := source
	snapshot.root, snapshot.local, snapshot.source, snapshot.info = root, name, snapshotSourceName(source), info
	snapshot.mode = source.info.Mode().Perm()
	return snapshot
}

// Do not use io.CopyBuffer directly with *os.File: its WriterTo/ReaderFrom fast
// paths can bypass the supplied buffer and our cancellation checks.
func copySnapshot(ctx context.Context, dst io.Writer, src io.Reader, buffer []byte) (int64, error) {
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return copied, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if err := ctx.Err(); err != nil {
				return copied, err
			}
			written, writeErr := dst.Write(buffer[:n])
			copied += int64(written)
			if writeErr != nil {
				return copied, writeErr
			}
			if written != n {
				return copied, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return copied, ctx.Err()
		}
		if readErr != nil {
			return copied, readErr
		}
	}
}

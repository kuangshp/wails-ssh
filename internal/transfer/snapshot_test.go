package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func snapshotFixture(t *testing.T, content map[string][]byte) ([]uploadFile, string) {
	t.Helper()
	directory := t.TempDir()
	for name, data := range content {
		putFile(t, filepath.Join(directory, name), data)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	files := make([]uploadFile, 0, len(content))
	for name := range content {
		info, err := root.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, uploadFile{root: root, local: name, remote: "/site/" + filepath.ToSlash(name), size: info.Size(), source: filepath.Join(directory, name), info: info})
	}
	return files, directory
}

func isolatedSnapshotTemp(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	t.Setenv("TMP", directory)
	t.Setenv("TEMP", directory)
	return directory
}

func assertSnapshotTempEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("owned snapshot files leaked after failure/cancellation: %v", entries)
	}
}

func TestSnapshotUploadSurvivesOriginalDeletionAndRewrite(t *testing.T) {
	content := map[string][]byte{"assets/index.hash.js": bytes.Repeat([]byte("old asset\n"), 17000), "index.html": []byte("old page")}
	files, sourceDir := snapshotFixture(t, content)
	for index := range files {
		mode := os.FileMode(0751)
		if files[index].local == "index.html" {
			mode = 0440
		}
		if err := os.Chmod(files[index].source, mode); err != nil {
			t.Fatal(err)
		}
		files[index].info, _ = files[index].root.Lstat(files[index].local)
	}
	snapshots, cleanup, err := snapshotUpload(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	snapshotDir := snapshots[0].root.Name()
	if runtime.GOOS != "windows" {
		info, err := os.Stat(snapshotDir)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("snapshot directory is not private: %v, %v", info, err)
		}
	}
	if err := os.Remove(filepath.Join(sourceDir, "assets/index.hash.js")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(sourceDir, "index.html"), 0600); err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Join(sourceDir, "index.html"), []byte("new page"))
	if err := files[0].root.Close(); err != nil {
		t.Fatal(err)
	}
	for index, snapshot := range snapshots {
		data, err := snapshot.root.ReadFile(snapshot.local)
		if err != nil || !bytes.Equal(data, content[files[index].local]) {
			t.Fatalf("snapshot changed with its original: %s, %v", snapshot.source, err)
		}
		if snapshot.source != files[index].source || snapshot.remote != files[index].remote || snapshot.size != files[index].size {
			t.Fatalf("snapshot lost original identity/target metadata: %+v", snapshot)
		}
		info, err := snapshot.root.Lstat(snapshot.local)
		if err != nil || !os.SameFile(info, snapshot.info) || snapshot.mode != files[index].info.Mode().Perm() {
			t.Fatalf("snapshot metadata/permissions do not describe the staged file: %v, %v", info, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("source permissions were applied to an owned local snapshot: %v", info.Mode())
		}
	}
	cleanup()
	cleanup() // The caller can safely arrange cleanup on multiple exit paths.
	if _, err := os.Stat(snapshotDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot directory remains after cleanup: %v", err)
	}
	assertContent(t, filepath.Join(sourceDir, "index.html"), []byte("new page"))
}

func TestSnapshotUploadRejectsSourcesChangedSinceCollection(t *testing.T) {
	for _, change := range []string{"deleted", "rewritten", "replaced"} {
		t.Run(change, func(t *testing.T) {
			files, _ := snapshotFixture(t, map[string][]byte{"asset.js": []byte("original")})
			temporary := isolatedSnapshotTemp(t)
			file := files[0]
			var err error
			switch change {
			case "deleted":
				err = os.Remove(file.source)
			case "rewritten":
				err = os.WriteFile(file.source, []byte("a different build"), 0600)
			case "replaced":
				// Preserve size and mtime: inode identity must still reject it.
				replacement := file.source + ".replacement"
				if err = os.WriteFile(replacement, []byte("replaced"), file.info.Mode().Perm()); err == nil {
					err = os.Chtimes(replacement, file.info.ModTime(), file.info.ModTime())
				}
				if err == nil {
					err = os.Remove(file.source)
				}
				if err == nil {
					err = os.Rename(replacement, file.source)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			snapshots, cleanup, err := snapshotUpload(context.Background(), files)
			if !errors.Is(err, errUploadSourceChanged) || !strings.Contains(err.Error(), file.source) || snapshots != nil || cleanup != nil {
				t.Fatalf("changed source was not rejected with its original path: snapshots=%v cleanup=%v err=%v", snapshots, cleanup != nil, err)
			}
			if change == "deleted" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing-source error lost its original cause: %v", err)
			}
			assertSnapshotTempEmpty(t, temporary)
		})
	}
}

type snapshotMutationReader struct {
	reader io.Reader
	after  func() error
}

func (r *snapshotMutationReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && r.after != nil {
		mutate := r.after
		r.after = nil
		if mutationErr := mutate(); mutationErr != nil {
			return n, mutationErr
		}
	}
	return n, err
}

func TestSnapshotUploadRejectsDeletionAndMutationDuringStreamingCopy(t *testing.T) {
	for _, change := range []string{"deleted", "rewritten"} {
		t.Run(change, func(t *testing.T) {
			files, _ := snapshotFixture(t, map[string][]byte{"asset.js": bytes.Repeat([]byte("a"), 3*snapshotBufferSize)})
			temporary := isolatedSnapshotTemp(t)
			file := files[0]
			copyFile := func(ctx context.Context, dst io.Writer, src io.Reader, buffer []byte) (int64, error) {
				if len(buffer) != snapshotBufferSize {
					return 0, fmt.Errorf("unexpected copy buffer size: %d", len(buffer))
				}
				info, err := dst.(*os.File).Stat()
				if err != nil {
					return 0, err
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
					return 0, fmt.Errorf("in-progress snapshot is not private: %v", info.Mode())
				}
				reader := &snapshotMutationReader{reader: src, after: func() error {
					if change == "deleted" {
						return os.Remove(file.source)
					}
					if err := os.WriteFile(file.source, bytes.Repeat([]byte("b"), int(file.size)), 0644); err != nil {
						return err
					}
					changed := file.info.ModTime().Add(time.Second)
					return os.Chtimes(file.source, changed, changed)
				}}
				return copySnapshot(ctx, dst, reader, buffer)
			}
			_, _, err := snapshotUploadWithCopy(context.Background(), files, copyFile)
			if !errors.Is(err, errUploadSourceChanged) || !strings.Contains(err.Error(), file.source) {
				t.Fatalf("mid-copy source change was not rejected: %v", err)
			}
			assertSnapshotTempEmpty(t, temporary)
		})
	}
}

func TestSnapshotUploadRechecksEarlierSourcesAfterTheWholeBatch(t *testing.T) {
	files, _ := snapshotFixture(t, map[string][]byte{"first.js": []byte("first"), "second.js": []byte("second")})
	temporary := isolatedSnapshotTemp(t)
	count := 0
	copyFile := func(ctx context.Context, dst io.Writer, src io.Reader, buffer []byte) (int64, error) {
		count++
		if count == 2 {
			if err := os.Remove(files[0].source); err != nil {
				return 0, err
			}
		}
		return copySnapshot(ctx, dst, src, buffer)
	}
	_, _, err := snapshotUploadWithCopy(context.Background(), files, copyFile)
	if !errors.Is(err, errUploadSourceChanged) || !strings.Contains(err.Error(), files[0].source) {
		t.Fatalf("an earlier source disappeared before the batch was ready: %v", err)
	}
	assertSnapshotTempEmpty(t, temporary)
}

func TestSnapshotUploadCancellationRemovesPartialFiles(t *testing.T) {
	files, _ := snapshotFixture(t, map[string][]byte{"asset.js": bytes.Repeat([]byte("a"), 3*snapshotBufferSize)})
	temporary := isolatedSnapshotTemp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	copyFile := func(ctx context.Context, dst io.Writer, src io.Reader, buffer []byte) (int64, error) {
		reader := &snapshotMutationReader{reader: src, after: func() error { cancel(); return nil }}
		return copySnapshot(ctx, dst, reader, buffer)
	}
	_, cleanup, err := snapshotUploadWithCopy(ctx, files, copyFile)
	if !errors.Is(err, context.Canceled) || cleanup != nil {
		t.Fatalf("cancelled snapshot returned success or lost cancellation: %v", err)
	}
	assertSnapshotTempEmpty(t, temporary)
	_, _, err = snapshotUpload(ctx, files)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled snapshot did work: %v", err)
	}
	assertSnapshotTempEmpty(t, temporary)
}

func TestSnapshotUploadCopyFailurePreservesOriginalReadOnlyPermissions(t *testing.T) {
	files, _ := snapshotFixture(t, map[string][]byte{"one": []byte("one"), "two": []byte("two")})
	for index := range files {
		if err := os.Chmod(files[index].source, 0444); err != nil {
			t.Fatal(err)
		}
		files[index].info, _ = files[index].root.Lstat(files[index].local)
	}
	temporary := isolatedSnapshotTemp(t)
	failed := errors.New("simulated disk write failure")
	count := 0
	copyFile := func(ctx context.Context, dst io.Writer, src io.Reader, buffer []byte) (int64, error) {
		count++
		if count == 2 {
			return 0, failed
		}
		return copySnapshot(ctx, dst, src, buffer)
	}
	_, _, err := snapshotUploadWithCopy(context.Background(), files, copyFile)
	if !errors.Is(err, failed) {
		t.Fatalf("copy failure was not preserved: %v", err)
	}
	assertSnapshotTempEmpty(t, temporary)
	for _, file := range files {
		info, err := os.Stat(file.source)
		if err != nil || info.Mode().Perm() != file.info.Mode().Perm() {
			t.Fatalf("cleanup modified original source permissions: %v, %v", info, err)
		}
	}
}

type snapshotPermissionInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (info snapshotPermissionInfo) Mode() os.FileMode { return info.mode }

func TestSnapshotUploadSeparatesNonOwnerSourcePermissionsFromOwnedSnapshot(t *testing.T) {
	// Model a source readable through another group/ACL. The process owns its
	// staged copy, so applying these source modes to that copy would remove its
	// owner-read bit. Fake source metadata avoids changing OS users or ACLs.
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("staged", []byte("snapshot bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := root.Lstat("staged")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0040, 0004, 0000} {
		source := uploadFile{local: "source", remote: "/remote/source", source: "/original/source", size: info.Size(), info: snapshotPermissionInfo{FileInfo: info, mode: mode}}
		snapshot := snapshotFileMetadata(source, root, "staged", info)
		if snapshot.mode != mode || snapshot.mode&0400 != 0 {
			t.Fatalf("source permissions were not retained independently: got %v, want %v", snapshot.mode, mode)
		}
		if runtime.GOOS != "windows" && snapshot.info.Mode().Perm() != 0600 {
			t.Fatalf("owned snapshot lost owner-read/private permissions: %v", snapshot.info.Mode())
		}
		content, err := snapshot.root.ReadFile(snapshot.local)
		if err != nil || string(content) != "snapshot bytes" {
			t.Fatalf("snapshot cannot be reopened with source mode %v: %q, %v", mode, content, err)
		}
	}
}

type growingSnapshotReader struct {
	reader io.Reader
	path   string
	reads  int
}

func (r *growingSnapshotReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads > 4 {
		return 0, errors.New("snapshot copy did not stop at the scanned size")
	}
	file, err := os.OpenFile(r.path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return 0, err
	}
	_, writeErr := file.Write(bytes.Repeat([]byte("g"), 1024))
	closeErr := file.Close()
	if writeErr != nil {
		return 0, writeErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return r.reader.Read(p)
}

func TestSnapshotUploadBoundsContinuouslyGrowingSources(t *testing.T) {
	files, _ := snapshotFixture(t, map[string][]byte{"live.log": []byte("start")})
	temporary := isolatedSnapshotTemp(t)
	var staged int64
	copyFile := func(ctx context.Context, dst io.Writer, src io.Reader, buffer []byte) (int64, error) {
		var err error
		staged, err = copySnapshot(ctx, dst, &growingSnapshotReader{reader: src, path: files[0].source}, buffer)
		return staged, err
	}
	_, _, err := snapshotUploadWithCopy(context.Background(), files, copyFile)
	if !errors.Is(err, errUploadSourceChanged) || staged != files[0].size+1 {
		t.Fatalf("growing source was not bounded and rejected: staged=%d, err=%v", staged, err)
	}
	assertSnapshotTempEmpty(t, temporary)
}

func TestSnapshotUploadRejectsNegativeScannedSize(t *testing.T) {
	files, _ := snapshotFixture(t, map[string][]byte{"asset.js": []byte("source")})
	temporary := isolatedSnapshotTemp(t)
	files[0].size = -1
	_, _, err := snapshotUpload(context.Background(), files)
	if !errors.Is(err, errUploadSourceChanged) {
		t.Fatalf("invalid scanned size was accepted: %v", err)
	}
	assertSnapshotTempEmpty(t, temporary)
}

package transfer

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/pkg/sftp"
)

// A lost acknowledgement after rename cannot safely be treated like an
// interrupted byte copy: the original may already have moved to its backup.
type commitError struct{ cause error }

func (e *commitError) Error() string { return e.cause.Error() }
func (e *commitError) Unwrap() error { return e.cause }
func uncertainCommit(err error) bool { var uncertain *commitError; return errors.As(err, &uncertain) }
func commitFailure(err error, target, temp, backup string) error {
	if err == nil {
		return nil
	}
	if !retryable(err) {
		return err
	}
	return &commitError{cause: fmt.Errorf("file replacement acknowledgement was lost; inspect destination %s and checkpoint %s before retrying; original backup may be at %s: %w", target, temp, backup, err)}
}

// replaceOps permits testing every failure point without damaging a real file.
type replaceOps struct {
	lstat  func(string) (os.FileInfo, error)
	rename func(string, string) error
	remove func(string) error
	atomic func(string, string) error
}

func replaceRemote(client *sftp.Client, temp, target string, overwrite bool) error {
	if !overwrite {
		return commitFailure(client.Rename(temp, target), target, temp, "(no backup)")
	}
	ops := replaceOps{lstat: client.Lstat, rename: client.Rename, remove: client.Remove}
	if _, ok := client.HasExtension("posix-rename@openssh.com"); ok {
		ops.atomic = client.PosixRename
	}
	return replaceSafely(ops, temp, target)
}

func replaceLocal(root *os.Root, temp, target string, overwrite bool) error {
	if !overwrite {
		// Link is an atomic create-if-absent operation: a file appearing after the
		// initial conflict check must never be silently overwritten.
		if err := root.Link(temp, target); err != nil {
			return err
		}
		return root.Remove(temp)
	}
	ops := replaceOps{lstat: root.Lstat, rename: root.Rename, remove: root.Remove}
	// os.Rename atomically replaces regular files on Unix; Windows may need backup.
	if runtime.GOOS != "windows" {
		ops.atomic = root.Rename
	}
	return replaceSafely(ops, temp, target)
}

func replaceSafely(ops replaceOps, temp, target string) error {
	info, err := ops.lstat(target)
	if os.IsNotExist(err) {
		return commitFailure(ops.rename(temp, target), target, temp, "(no backup)")
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace a directory or symbolic link: %s", target)
	}
	if ops.atomic != nil {
		// If a supported atomic operation fails, preserve the original and report it.
		// The compatibility fallback is only for servers without the extension.
		return commitFailure(ops.atomic(temp, target), target, temp, "(atomic replacement)")
	}
	backup := target + ".wails-ssh-backup-" + uniqueID()
	if _, err := ops.lstat(backup); !os.IsNotExist(err) {
		return fmt.Errorf("unable to reserve backup path: %s", backup)
	}
	if err := ops.rename(target, backup); err != nil {
		return commitFailure(fmt.Errorf("backup original at %s: %w", backup, err), target, temp, backup)
	}
	if err := ops.rename(temp, target); err != nil {
		if retryable(err) {
			return commitFailure(err, target, temp, backup)
		}
		if restoreErr := ops.rename(backup, target); restoreErr != nil {
			return commitFailure(fmt.Errorf("replacement failed: %v; original remains at %s (restore failed: %w)", err, backup, restoreErr), target, temp, backup)
		}
		return fmt.Errorf("replacement failed; original restored: %w", err)
	}
	if err := ops.remove(backup); err != nil {
		return commitFailure(fmt.Errorf("replacement completed, but old backup could not be removed at %s: %w", backup, err), target, temp, backup)
	}
	return nil
}

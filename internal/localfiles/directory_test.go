package localfiles

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestListDirectoryMixesFilesAndFoldersWithoutTraversing(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zzz 资料", "aaa 项目"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"B.txt": "BB", "a.txt": "A", "中文 文件.txt": "test"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "aaa 项目", "nested.txt"), []byte("nested"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := listDirectory(filepath.Join(root, "aaa 项目", ".."), fixedHome(root), os.ReadDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != root || result.HomePath != root || result.ParentPath != filepath.Dir(root) {
		t.Fatalf("unexpected paths: %#v", result)
	}
	var names []string
	for i, entry := range result.Entries {
		names = append(names, entry.Name)
		if entry.Path != filepath.Join(root, entry.Name) || entry.IsDir != (i < 2) || entry.IsSymlink {
			t.Fatalf("unexpected entry metadata: %#v", entry)
		}
		if entry.Name == "中文 文件.txt" && entry.Size != 4 {
			t.Fatalf("expected regular file byte size 4, got %d", entry.Size)
		}
	}
	if want := []string{"aaa 项目", "zzz 资料", "a.txt", "B.txt", "中文 文件.txt"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("got %q, want %q", names, want)
	}
}

func TestListDirectoryDefaultsToInjectedHomeAndReturnsEmptyArray(t *testing.T) {
	home := filepath.Join(t.TempDir(), "测试 home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := listDirectory("", fixedHome(home), os.ReadDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != home || result.HomePath != home || result.Entries == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"entries":[]`) || !strings.Contains(string(encoded), `"parentPath":`) {
		t.Fatalf("unexpected JSON shape: %s", encoded)
	}
}

func TestListDirectoryMarksSymlinksIncludingBrokenLinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "folder"), filepath.Join(root, "folder-link")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken-link")); err != nil {
		t.Fatal(err)
	}
	result, err := listDirectory(root, fixedHome(root), os.ReadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 3 {
		t.Fatalf("expected folder and both symlinks, got %#v", result.Entries)
	}
	for _, entry := range result.Entries {
		if entry.Name == "folder" {
			if !entry.IsDir || entry.IsSymlink {
				t.Fatalf("unexpected ordinary directory: %#v", entry)
			}
			continue
		}
		if !entry.IsSymlink || entry.IsDir {
			t.Fatalf("must mark and not follow symlinks: %#v", entry)
		}
	}
}

func TestListDirectoryErrorsForUnreadableMissingAndNonDirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(root, "missing")} {
		if _, err := listDirectory(path, fixedHome(root), os.ReadDir); err == nil {
			t.Fatalf("expected listing %q to fail", path)
		}
	}
	// Permission bits are unreliable when tests run as root or on Windows.
	// Inject only the read failure, keeping path handling and error wrapping real.
	_, err := listDirectory(root, fixedHome(root), func(path string) ([]os.DirEntry, error) {
		return nil, &os.PathError{Op: "readdir", Path: path, Err: os.ErrPermission}
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected readable permission error, got %v", err)
	}
}

func TestListDirectoryRootHasNoParent(t *testing.T) {
	// filepath supplies this platform's volume syntax (e.g. C:\ on Windows).
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	result, err := listDirectory(root, fixedHome(root), func(path string) ([]os.DirEntry, error) {
		if path != root {
			t.Fatalf("expected root %q, got %q", root, path)
		}
		return []os.DirEntry{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ParentPath != "" || result.Path != root {
		t.Fatalf("root must not navigate to itself: %#v", result)
	}
}

func TestListDirectoryUnavailableHome(t *testing.T) {
	root := t.TempDir()
	noHome := func() (string, error) { return "", os.ErrNotExist }
	if _, err := listDirectory("", noHome, os.ReadDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty path needs home directory, got %v", err)
	}
	result, err := listDirectory(root, noHome, os.ReadDir)
	if err != nil || result.Path != root || result.HomePath != "" {
		t.Fatalf("an explicit directory remains browsable without home: %#v, %v", result, err)
	}
}

func fixedHome(home string) func() (string, error) {
	return func() (string, error) { return home, nil }
}

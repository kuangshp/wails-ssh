// Package localfiles exposes read-only metadata for the local upload picker.
package localfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Entry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDir     bool   `json:"isDir"`
	IsSymlink bool   `json:"isSymlink"`
	Size      int64  `json:"size"`
}

type Directory struct {
	Path       string  `json:"path"`
	ParentPath string  `json:"parentPath"`
	HomePath   string  `json:"homePath"`
	Entries    []Entry `json:"entries"`
}

// ListDirectory returns one directory's upload candidates. Symlinks are listed
// for context but marked so the picker can prevent selecting them. No file
// content is opened, and subdirectories are not traversed.
func ListDirectory(directory string) (Directory, error) {
	return listDirectory(directory, os.UserHomeDir, os.ReadDir)
}

func listDirectory(directory string, userHome func() (string, error), readDir func(string) ([]os.DirEntry, error)) (Directory, error) {
	home, homeErr := userHome()
	if homeErr == nil && home != "" {
		var err error
		home, err = filepath.Abs(home)
		if err != nil {
			return Directory{}, fmt.Errorf("无法确定主目录: %w", err)
		}
	}
	if directory == "" {
		if homeErr != nil {
			return Directory{}, fmt.Errorf("无法确定主目录: %w", homeErr)
		}
		if home == "" {
			return Directory{}, fmt.Errorf("无法确定主目录")
		}
		directory = home
	}
	path, err := filepath.Abs(directory)
	if err != nil {
		return Directory{}, fmt.Errorf("无效的本地目录: %w", err)
	}
	entries, err := readDir(path)
	if err != nil {
		return Directory{}, fmt.Errorf("无法读取本地目录: %w", err)
	}
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	result := Directory{Path: path, ParentPath: parent, HomePath: home, Entries: make([]Entry, 0, len(entries))}
	for _, entry := range entries {
		info, err := entry.Info()
		if os.IsNotExist(err) {
			// A file can disappear while the directory is being listed.
			continue
		}
		if err != nil {
			return Directory{}, fmt.Errorf("无法读取本地项目 %q: %w", entry.Name(), err)
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		if !info.IsDir() && !info.Mode().IsRegular() && !isSymlink {
			continue
		}
		result.Entries = append(result.Entries, Entry{
			Name: entry.Name(), Path: filepath.Join(path, entry.Name()),
			IsDir: info.IsDir(), IsSymlink: isSymlink, Size: info.Size(),
		})
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		lowerA, lowerB := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if lowerA == lowerB {
			return a.Name < b.Name
		}
		return lowerA < lowerB
	})
	return result, nil
}

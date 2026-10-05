package main

import (
	"fmt"
	"os"
	"path/filepath"

	"wails-ssh/internal/localfiles"
	"wails-ssh/internal/nativepicker"
)

type UploadSelection struct {
	Native  bool               `json:"native"`
	Entries []localfiles.Entry `json:"entries"`
}

// PickUploadSources uses the system mixed picker where available. A cancelled
// native dialog returns an empty selection without opening the fallback.
func (a *App) PickUploadSources() (UploadSelection, error) {
	result := UploadSelection{Native: nativepicker.Available(), Entries: []localfiles.Entry{}}
	if err := a.ready(); err != nil {
		return result, err
	}
	if !result.Native {
		return result, nil
	}
	paths, err := nativepicker.Pick()
	if err != nil {
		return result, err
	}
	result.Entries, err = describeUploadSelection(paths)
	return result, err
}

func describeUploadSelection(paths []string) ([]localfiles.Entry, error) {
	entries := make([]localfiles.Entry, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, selected := range paths {
		path, err := filepath.Abs(selected)
		if err != nil {
			return nil, err
		}
		if seen[path] {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("无法读取所选项目 %q: %w", selected, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("暂不支持上传符号链接，请选择实际文件或文件夹：%s", path)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("仅支持上传普通文件和文件夹：%s", path)
		}
		seen[path] = true
		entries = append(entries, localfiles.Entry{Name: info.Name(), Path: path, IsDir: info.IsDir(), Size: info.Size()})
	}
	return entries, nil
}

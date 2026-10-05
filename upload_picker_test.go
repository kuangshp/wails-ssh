package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDescribeMixedUploadSelection(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "目录 assets")
	file := filepath.Join(root, "首页 index.html")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := describeUploadSelection([]string{folder, file, file})
	if err != nil || len(entries) != 2 {
		t.Fatalf("mixed selection: entries=%v err=%v", entries, err)
	}
	if !entries[0].IsDir || entries[0].Path != folder || entries[1].IsDir || entries[1].Path != file || entries[1].Size != 7 {
		t.Fatalf("incorrect mixed selection metadata: %+v", entries)
	}
	empty, err := describeUploadSelection(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("cancel should produce an empty list: %v %v", empty, err)
	}
	if _, err := describeUploadSelection([]string{file, filepath.Join(root, "missing")}); err == nil {
		t.Fatal("missing selection silently accepted")
	}
}

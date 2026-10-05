package remotearchive

import (
	"archive/tar"
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func python(t *testing.T, mode, source string) (string, error) {
	t.Helper()
	exe, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	result, err := exec.Command(exe, "-c", script, mode, source).CombinedOutput()
	return strings.TrimSpace(string(result)), err
}
func TestArchiveRoundTripNoClobber(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "quote's name.txt")
	if err := os.WriteFile(source, []byte("hello world"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := python(t, "compress", source)
	if err != nil {
		t.Fatal(result, err)
	}
	original, _ := os.ReadFile(result)
	if _, err = python(t, "compress", source); err == nil {
		t.Fatal("replaced existing archive")
	}
	after, _ := os.ReadFile(result)
	if string(original) != string(after) {
		t.Fatal("existing archive changed")
	}
	target, err := python(t, "extract", result)
	if err != nil {
		t.Fatal(target, err)
	}
	contents, err := os.ReadFile(filepath.Join(target, filepath.Base(source)))
	if err != nil || string(contents) != "hello world" {
		t.Fatal(string(contents), err)
	}
}
func TestRejectUnsafeArchives(t *testing.T) {
	for _, item := range []struct {
		name string
		kind byte
		link string
	}{{"../escaped", tar.TypeReg, ""}, {"/tmp/escaped", tar.TypeReg, ""}, {"link", tar.TypeSymlink, ".."}, {"hard", tar.TypeLink, "../secret"}, {"device", tar.TypeChar, ""}} {
		t.Run(item.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "bad.tar")
			file, err := os.Create(source)
			if err != nil {
				t.Fatal(err)
			}
			archive := tar.NewWriter(file)
			if err = archive.WriteHeader(&tar.Header{Name: item.name, Typeflag: item.kind, Linkname: item.link, Mode: 0644}); err != nil {
				t.Fatal(err)
			}
			archive.Close()
			file.Close()
			if output, err := python(t, "extract", source); err == nil {
				t.Fatal("accepted unsafe archive", output)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 1 {
				t.Fatal("failed extraction left files", files)
			}
		})
	}
}
func TestRejectZipTraversal(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "bad.zip")
	file, _ := os.Create(source)
	archive := zip.NewWriter(file)
	entry, _ := archive.Create("../escape")
	entry.Write([]byte("bad"))
	archive.Close()
	file.Close()
	if output, err := python(t, "extract", source); err == nil {
		t.Fatal(output)
	}
}

func TestRefuseRootCompression(t *testing.T) {
	if output, err := python(t, "compress", "/"); err == nil || !strings.Contains(output, "filesystem root cannot be archived") {
		t.Fatal("root compression was not rejected", output, err)
	}
}

func TestShellQuote(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell unavailable")
	}
	input := "a'b $(touch never)\ntext"
	cmd := exec.Command("sh", "-c", "printf %s "+Quote(input))
	output, err := cmd.Output()
	if err != nil || string(output) != input {
		t.Fatal(string(output), err)
	}
}

package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestUntar(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range []tar.Header{
		{Name: "release-1.0/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "release-1.0/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "release-1.0/bin/tool", Typeflag: tar.TypeReg, Mode: 0o755, Size: 3},
		{Name: "release-1.0/bin/alias", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "tool"},
		{Name: "release-1.0/../escape", Typeflag: tar.TypeReg, Mode: 0o644, Size: 3},
	} {
		_ = tw.WriteHeader(&h)
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte("hi\n"))
		}
	}
	_ = tw.Close()
	_ = gz.Close()

	dir := t.TempDir()
	if err := untar(&buf, filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out", "bin", "alias"))
	if err != nil || string(data) != "hi\n" {
		t.Fatalf("symlinked file: %q, %v", data, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "out", "bin", "tool")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("tool mode: %v, %v", info, err)
	}
	if exists(filepath.Join(dir, "escape")) || exists(filepath.Join(dir, "out", "release-1.0")) {
		t.Fatal("top-level dir not stripped or path escaped")
	}
}

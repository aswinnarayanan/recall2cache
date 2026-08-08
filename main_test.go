package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// writeFile creates a file of n bytes under dir.
func writeFile(t *testing.T, dir, name string, n int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// recall runs a recall over dir with a small buffer and returns the stats.
func recall(t *testing.T, dir string, bufferSize int) *stats {
	t.Helper()
	total := &stats{}
	// The error is deliberately not asserted here; individual tests check the
	// counters, since a walk error must not stop the rest of the tree.
	_ = recallFiles(dir, 4, bufferSize, total)
	return total
}

func TestReadsEveryRegularFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "empty.bin", 0)
	writeFile(t, dir, "small.bin", 100)
	writeFile(t, dir, "sub/deeper/exact.bin", 4096)
	// Larger than the buffer, so it takes several reads.
	writeFile(t, dir, "sub/large.bin", 4096*3+7)

	got := recall(t, dir, 4096)

	if n := got.filesRead.Load(); n != 4 {
		t.Errorf("filesRead = %d, want 4", n)
	}
	if n := got.bytesRead.Load(); n != int64(0+100+4096+4096*3+7) {
		t.Errorf("bytesRead = %d, want %d", n, 0+100+4096+4096*3+7)
	}
	if n := got.filesFailed.Load(); n != 0 {
		t.Errorf("filesFailed = %d, want 0", n)
	}
}

// A FIFO with no writer blocks forever on open. Before the regular-file guard this
// deadlocked the whole run, so this test hangs rather than fails on a regression.
func TestSkipsFIFOWithoutHanging(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "real.bin", 10)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0644); err != nil {
		t.Skipf("cannot create FIFO: %v", err)
	}

	got := recall(t, dir, 4096)

	if n := got.filesRead.Load(); n != 1 {
		t.Errorf("filesRead = %d, want 1", n)
	}
	if got.skipped != 1 {
		t.Errorf("skipped = %d, want 1", got.skipped)
	}
}

// Symlinks are skipped rather than followed, so a link cannot pull in a file from
// outside the input tree or cause the same file to be recalled twice.
func TestSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "real.bin", 10)
	if err := os.Symlink(target, filepath.Join(dir, "link.bin")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	got := recall(t, dir, 4096)

	if n := got.filesRead.Load(); n != 1 {
		t.Errorf("filesRead = %d, want 1", n)
	}
	if n := got.bytesRead.Load(); n != 10 {
		t.Errorf("bytesRead = %d, want 10", n)
	}
	if got.skipped != 1 {
		t.Errorf("skipped = %d, want 1", got.skipped)
	}
}

// An unreadable directory must be reported but must not abandon the rest of the walk.
func TestUnreadableDirDoesNotAbortWalk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, permissions are not enforced")
	}
	dir := t.TempDir()
	// Named so the walk reaches it before the readable files, which is where the old
	// behaviour lost them.
	writeFile(t, dir, "aaa_locked/hidden.bin", 10)
	writeFile(t, dir, "zzz_visible.bin", 20)
	locked := filepath.Join(dir, "aaa_locked")
	if err := os.Chmod(locked, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0755) })

	got := recall(t, dir, 4096)

	if n := got.filesRead.Load(); n != 1 {
		t.Errorf("filesRead = %d, want 1 (the file after the unreadable dir)", n)
	}
	if got.walkErrors != 1 {
		t.Errorf("walkErrors = %d, want 1", got.walkErrors)
	}
}

func TestUnreadableFileCountsAsFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, permissions are not enforced")
	}
	dir := t.TempDir()
	path := writeFile(t, dir, "locked.bin", 10)
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}

	got := recall(t, dir, 4096)

	if n := got.filesFailed.Load(); n != 1 {
		t.Errorf("filesFailed = %d, want 1", n)
	}
	if n := got.filesRead.Load(); n != 0 {
		t.Errorf("filesRead = %d, want 0", n)
	}
}

// The buffer size must not change how many bytes are read.
func TestBufferSizeDoesNotChangeBytesRead(t *testing.T) {
	dir := t.TempDir()
	const size = 1 << 20
	writeFile(t, dir, "file.bin", size)

	for _, bufferSize := range []int{512, 4096, 1 << 16, 1 << 22} {
		got := recall(t, dir, bufferSize)
		if n := got.bytesRead.Load(); n != size {
			t.Errorf("buffer %d: bytesRead = %d, want %d", bufferSize, n, size)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{1 << 30, "1.0 GiB"},
		{1 << 40, "1.0 TiB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.in); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

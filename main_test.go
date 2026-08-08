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

// recall runs a recall over dir with the filter off, so the existing tests exercise the
// read path on every file regardless of allocation.
func recall(t *testing.T, dir string, bufferSize int) *stats {
	t.Helper()
	return recallWith(t, dir, options{bufferSize: bufferSize})
}

// recallWith runs a recall over dir with explicit options.
func recallWith(t *testing.T, dir string, opts options) *stats {
	t.Helper()
	total := &stats{}
	// The error is deliberately not asserted here; individual tests check the
	// counters, since a walk error must not stop the rest of the tree.
	_ = recallFiles(dir, 4, opts, total)
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

// sparseFile creates a file with a large logical size but no allocated blocks, which is
// the same signature a tape stub presents: allocated far below logical.
func sparseFile(t *testing.T, dir, name string, size int64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Blocks*512 >= size {
		t.Skipf("filesystem does not report %s as sparse, cannot simulate a stub", path)
	}
	return path
}

func TestFilterSkipsResidentFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "resident.bin", 8192)

	got := recallWith(t, dir, options{bufferSize: 4096, filter: true, stubRatio: defaultStubRatio})

	if n := got.filesResident.Load(); n != 1 {
		t.Errorf("filesResident = %d, want 1", n)
	}
	if n := got.filesRead.Load(); n != 0 {
		t.Errorf("filesRead = %d, want 0, a resident file must not be read", n)
	}
	if n := got.bytesRead.Load(); n != 0 {
		t.Errorf("bytesRead = %d, want 0", n)
	}
}

func TestFilterRecallsStubLikeFiles(t *testing.T) {
	dir := t.TempDir()
	const size = 1 << 20
	sparseFile(t, dir, "stub.bin", size)

	got := recallWith(t, dir, options{bufferSize: 4096, filter: true, stubRatio: defaultStubRatio})

	if n := got.filesRead.Load(); n != 1 {
		t.Errorf("filesRead = %d, want 1", n)
	}
	if n := got.bytesRead.Load(); n != size {
		t.Errorf("bytesRead = %d, want %d", n, size)
	}
	if n := got.filesResident.Load(); n != 0 {
		t.Errorf("filesResident = %d, want 0", n)
	}
}

// With the filter off the tool reads everything, which is the pre-filter behaviour and
// the escape hatch if the heuristic ever misjudges a storage tier.
func TestFilterDisabledReadsEverything(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "resident.bin", 8192)
	sparseFile(t, dir, "stub.bin", 4096)

	got := recallWith(t, dir, options{bufferSize: 4096, filter: false})

	if n := got.filesRead.Load(); n != 2 {
		t.Errorf("filesRead = %d, want 2", n)
	}
	if n := got.filesResident.Load(); n != 0 {
		t.Errorf("filesResident = %d, want 0 when the filter is off", n)
	}
}

func TestDryRunReadsNothing(t *testing.T) {
	dir := t.TempDir()
	sparseFile(t, dir, "stub.bin", 1<<20)

	got := recallWith(t, dir, options{bufferSize: 4096, filter: true, stubRatio: defaultStubRatio, dryRun: true})

	if n := got.filesRead.Load(); n != 1 {
		t.Errorf("filesRead = %d, want 1 file listed", n)
	}
	if n := got.bytesRead.Load(); n != 0 {
		t.Errorf("bytesRead = %d, want 0, dry run must not read", n)
	}
}

func TestIsMigrated(t *testing.T) {
	dir := t.TempDir()
	resident := writeFile(t, dir, "resident.bin", 8192)
	empty := writeFile(t, dir, "empty.bin", 0)
	stub := sparseFile(t, dir, "stub.bin", 1<<20)

	for _, tt := range []struct {
		path string
		want bool
		why  string
	}{
		{resident, false, "fully allocated"},
		{empty, false, "nothing to recall"},
		{stub, true, "allocated far below logical"},
	} {
		got, err := isMigrated(tt.path, defaultStubRatio)
		if err != nil {
			t.Fatalf("isMigrated(%s): %v", tt.path, err)
		}
		if got != tt.want {
			t.Errorf("isMigrated(%s) = %v, want %v (%s)", tt.path, got, tt.want, tt.why)
		}
	}
}

// A ratio below 1 tolerates some under-allocation before calling a file a stub.
func TestStubRatioTolerance(t *testing.T) {
	dir := t.TempDir()
	path := sparseFile(t, dir, "stub.bin", 1<<20)

	// Ratio 0 means nothing is ever under-allocated enough to count as a stub.
	got, err := isMigrated(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("isMigrated with ratio 0 = true, want false")
	}
}

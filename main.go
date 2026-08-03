package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// Number of files read concurrently, unless overridden with -j. Against tape this
	// is queue depth: the more requests outstanding, the more scope the filesystem has
	// to order recalls efficiently.
	defaultWorkers = 100
	// Paths buffered between the walk and the workers, so a slow recall does not stall
	// the traversal.
	queueDepth = 4096
	// Read buffer per worker, unless overridden with -buffer. Larger buffers mean
	// proportionally fewer read syscalls over the same bytes.
	defaultBufferMiB = 4
	// How often progress is reported, unless overridden with -progress.
	defaultProgress = time.Minute
)

// version is set at build time with -ldflags "-X main.version=...". It stays "dev" for
// local builds, so a binary on shared storage can always be traced back to a release.
var version = "dev"

// stats accumulates what a run actually did. The atomic fields are written by the
// workers; skipped and walkErrors are only ever touched by the walk goroutine.
type stats struct {
	filesRead   atomic.Int64
	filesFailed atomic.Int64
	bytesRead   atomic.Int64
	skipped     int64
	walkErrors  int64
}

// Main entrypoint. The real work is in run so that deferred cleanup still happens on a
// non-zero exit.
func main() {
	os.Exit(run())
}

func run() int {
	logFile := flag.String("log", "", "Path to log file")
	workers := flag.Int("j", defaultWorkers, "Number of files to read concurrently")
	bufferMiB := flag.Int("buffer", defaultBufferMiB, "Read buffer size per worker, in MiB")
	progress := flag.Duration("progress", defaultProgress, "Interval between progress lines (0 to disable)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("recall2cache", version)
		return 0
	}

	if *workers < 1 {
		log.Println("-j must be at least 1")
		return 1
	}
	if *bufferMiB < 1 {
		log.Println("-buffer must be at least 1")
		return 1
	}

	if *logFile != "" {
		file, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Printf("Failed to open log file: %v", err)
			return 1
		}
		defer file.Close()
		log.SetOutput(file)
	}

	if len(flag.Args()) == 0 {
		log.Println("Please provide the input directory")
		flag.Usage()
		return 1
	}

	timeStart := time.Now()
	total := &stats{}
	failedDirs := 0

	// A recall over tape can run for hours. Without this the tool emits nothing
	// between the first line and the last.
	if *progress > 0 {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			ticker := time.NewTicker(*progress)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					elapsed := time.Since(timeStart)
					bytes := total.bytesRead.Load()
					log.Printf("... %d files, %s, %s/s, elapsed %s",
						total.filesRead.Load(), humanBytes(bytes),
						humanBytes(int64(float64(bytes)/elapsed.Seconds())),
						elapsed.Truncate(time.Second))
				}
			}
		}()
	}

	// Process each input directory provided
	for _, inputDir := range flag.Args() {
		log.Println("> Recalling from", inputDir)

		// Check if the input directory is accessible
		if _, err := os.Stat(inputDir); err != nil {
			log.Println("Cannot access input directory:", inputDir, err)
			failedDirs++
			continue
		}

		// Recall files from the input directory. Work done before an error still
		// counts, so the totals do not under-report.
		if err := recallFiles(inputDir, *workers, *bufferMiB*1024*1024, total); err != nil {
			log.Println("Error recalling files:", err)
			failedDirs++
		}
	}

	// Print results
	elapsed := time.Since(timeStart)
	resultText := fmt.Sprintf("Recalled %d files (%s) in %s [%s/s]",
		total.filesRead.Load(), humanBytes(total.bytesRead.Load()), elapsed,
		humanBytes(int64(float64(total.bytesRead.Load())/elapsed.Seconds())))
	separator := strings.Repeat("=", len(resultText))

	log.Println(separator)
	log.Println(resultText)
	if total.filesFailed.Load() > 0 || total.walkErrors > 0 || total.skipped > 0 {
		log.Printf("%d failed, %d skipped as non-regular, %d walk errors",
			total.filesFailed.Load(), total.skipped, total.walkErrors)
	}
	log.Println(separator)

	// Exit non-zero if anything went wrong, so a scheduler can tell a clean run from a
	// run where most of the tree failed.
	if failedDirs > 0 || total.filesFailed.Load() > 0 || total.walkErrors > 0 {
		return 1
	}
	return 0
}

// humanBytes formats a byte count for the summary line.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for c := n / unit; c >= unit; c /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// recallFiles processes all files in the given directory, accumulating into total.
func recallFiles(inputDir string, workers int, bufferSize int, total *stats) error {
	wg := sync.WaitGroup{}

	// A fixed pool of workers reads from this queue while the walk fills it. The walk
	// must not spawn a goroutine per file: on a multi-million-file tree that parks
	// millions of goroutines, each holding a stack, long before any of them do work.
	paths := make(chan string, queueDepth)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// One buffer per worker, reused for every file it handles.
			dataBuffer := make([]byte, bufferSize)
			for filePath := range paths {
				n, err := uncacheFile(filePath, dataBuffer)
				if err != nil {
					log.Println("Error uncaching file:", err)
					total.filesFailed.Add(1)
					continue
				}
				total.filesRead.Add(1)
				total.bytesRead.Add(n)
			}
		}()
	}

	queued := 0
	skipped := int64(0)
	walkErrors := int64(0)

	startTime := time.Now()
	// The counters below are only touched from this callback, which WalkDir runs on a
	// single goroutine, so they need no synchronisation.
	err := filepath.WalkDir(inputDir, func(filePath string, file fs.DirEntry, err error) error {
		if err != nil {
			// Keep walking. One unreadable file or directory must not abandon the
			// rest of the tree.
			log.Println("Error accessing", filePath+":", err)
			walkErrors++
			return nil
		}
		if file.IsDir() {
			return nil
		}
		// Only regular files. Opening a FIFO with no writer blocks forever and would
		// hold a concurrency slot permanently, and symlinks would be followed back out
		// of the input tree.
		if !file.Type().IsRegular() {
			skipped++
			return nil
		}
		queued++
		paths <- filePath
		return nil
	})
	close(paths)
	wg.Wait()

	total.skipped += skipped
	total.walkErrors += walkErrors

	if skipped > 0 {
		log.Println("Skipped", skipped, "non-regular files in", inputDir)
	}
	if walkErrors > 0 {
		log.Println("Encountered", walkErrors, "errors walking", inputDir)
	}
	log.Println("<", queued, "files in", time.Since(startTime))

	if err != nil {
		return fmt.Errorf("error walking directory %s: %w", inputDir, err)
	}
	return nil
}

// uncacheFile reads the file to uncache it. The caller supplies the buffer so it can be
// reused across files. Reads stay sequential and cover the whole file: that is what
// triggers the recall, and it must not change.
func uncacheFile(filePath string, dataBuffer []byte) (int64, error) {
	fileHandle, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer fileHandle.Close()

	var read int64
	for {
		n, err := fileHandle.Read(dataBuffer)
		read += int64(n)
		if err != nil {
			if err == io.EOF {
				break
			}
			return read, fmt.Errorf("failed to read file %s: %w", filePath, err)
		}
	}
	return read, nil
}

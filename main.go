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
)

// Main entrypoint
func main() {
	logFile := flag.String("log", "", "Path to log file")
	workers := flag.Int("j", defaultWorkers, "Number of files to read concurrently")
	bufferMiB := flag.Int("buffer", defaultBufferMiB, "Read buffer size per worker, in MiB")
	flag.Parse()

	if *workers < 1 {
		log.Fatalln("-j must be at least 1")
	}
	if *bufferMiB < 1 {
		log.Fatalln("-buffer must be at least 1")
	}

	if *logFile != "" {
		file, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("Failed to open log file: %v", err)
		}
		log.SetOutput(file)
	}

	if len(flag.Args()) == 0 {
		log.Println("Please provide the input directory")
		return
	}

	timeStart := time.Now()
	totalCount := 0

	// Process each input directory provided
	for _, inputDir := range flag.Args() {
		log.Println("> Recalling from", inputDir)

		// Check if the input directory is accessible
		if _, err := os.Stat(inputDir); err != nil {
			log.Println("Cannot access input directory:", inputDir, err)
			continue
		}

		// Recall files from the input directory. Files recalled before an error are
		// still counted, so the total does not under-report.
		count, err := recallFiles(inputDir, *workers, *bufferMiB*1024*1024)
		totalCount += count
		if err != nil {
			log.Println("Error recalling files:", err)
			continue
		}
	}

	// Print results
	resultText := fmt.Sprintf("Recalled %d files in %s", totalCount, time.Since(timeStart))
	separator := strings.Repeat("=", len(resultText))

	log.Println(separator)
	log.Println(resultText)
	log.Println(separator)
}

// recallFiles processes all files in the given directory and returns the count of processed files.
func recallFiles(inputDir string, workers int, bufferSize int) (int, error) {
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
				if err := uncacheFile(filePath, dataBuffer); err != nil {
					log.Println("Error uncaching file:", err)
				}
			}
		}()
	}

	count := 0
	skipped := 0
	walkErrors := 0

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
		count++
		paths <- filePath
		return nil
	})
	close(paths)
	wg.Wait()

	if skipped > 0 {
		log.Println("Skipped", skipped, "non-regular files in", inputDir)
	}
	if walkErrors > 0 {
		log.Println("Encountered", walkErrors, "errors walking", inputDir)
	}
	log.Println("<", count, "files in", time.Since(startTime))

	if err != nil {
		return count, fmt.Errorf("error walking directory %s: %w", inputDir, err)
	}
	return count, nil
}

// uncacheFile reads the file to uncache it. The caller supplies the buffer so it can be
// reused across files. Reads stay sequential and cover the whole file: that is what
// triggers the recall, and it must not change.
func uncacheFile(filePath string, dataBuffer []byte) error {
	fileHandle, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer fileHandle.Close()

	for {
		_, err := fileHandle.Read(dataBuffer)
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to read file %s: %w", filePath, err)
		}
	}
	return nil
}

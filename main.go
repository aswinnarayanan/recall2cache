package main

import (
	"bufio"
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

func main() {
	if len(os.Args) <= 1 {
		log.Println("Please provide the input directory")
		return
	}

	timeStart := time.Now()
	totalCount := 0

	for _, inputDir := range os.Args[1:] {
		log.Println("> Recalling from", inputDir)

		if _, err := os.Stat(inputDir); os.IsNotExist(err) {
			log.Println("Input directory does not exist:", inputDir)
			continue
		}

		count, err := recallFiles(inputDir)
		if err != nil {
			log.Println("Error recalling files:", err)
			continue
		}

		totalCount += count
	}

	resultText := fmt.Sprintf("Recalled %d files in %s", totalCount, time.Since(timeStart))
	separator := strings.Repeat("=", len(resultText))

	log.Println(separator)
	log.Println(resultText)
	log.Println(separator)
}

// recallFiles processes all files in the given directory
func recallFiles(inputDir string) (int, error) {
	wg := sync.WaitGroup{}
	wc := make(chan struct{}, 100)
	count := 0

	startTime := time.Now()
	err := filepath.WalkDir(inputDir, func(filePath string, file fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("error accessing file %s: %w", filePath, err)
		}
		if !file.IsDir() {
			count++
			wg.Add(1)
			go func(filePath string) {
				defer wg.Done()
				wc <- struct{}{}
				if err := uncacheFile(filePath); err != nil {
					log.Println("Error uncaching file:", err)
				}
				<-wc
			}(filePath)
		}
		return nil
	})
	wg.Wait()
	if err != nil {
		return 0, fmt.Errorf("error walking directory %s: %w", inputDir, err)
	}

	log.Println("<", count, "files in", time.Since(startTime))
	return count, nil
}

// uncacheFile reads the file to uncache it
func uncacheFile(filePath string) error {
	dataBuffer := make([]byte, 4096)

	fileHandle, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer fileHandle.Close()

	fileReader := bufio.NewReader(fileHandle)
	for {
		_, err := fileReader.Read(dataBuffer)
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to read file %s: %w", filePath, err)
		}
	}
	return nil
}

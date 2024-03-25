package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func main() {
	if os.Args != nil && len(os.Args) > 1 {
		timeStart := time.Now()
		timePoint := timeStart
		totalCount := 0
		for _, inputDir := range os.Args[1:] {
			fmt.Println("Recalling", inputDir)

			if _, err := os.Stat(inputDir); os.IsNotExist(err) {
				fmt.Println("Input directory does not exist")
			} else {
				wg := sync.WaitGroup{}
				wc := make(chan struct{}, 100)
				count := 0

				err := filepath.WalkDir(inputDir,
					func(filePath string, file fs.DirEntry, err error) error {
						if !file.IsDir() {
							count++

							wg.Add(1)
							go func(count int) {
								wc <- struct{}{}
								uncacheFile(filePath)
								<-wc
								wg.Done()
							}(count)
						}
						return nil
					})
				wg.Wait()
				if err != nil {
					fmt.Println(err)
				}
				fmt.Printf("\nRecalled %d files in %s\n\n", count, time.Since(timePoint))
				timePoint = time.Now()
				totalCount += count
			}
		}
		fmt.Printf("===================\nRecalled %d files in %s\n", totalCount, time.Since(timeStart))
	} else {
		fmt.Println("Please provide the input directory")
	}
}

func uncacheFile(filePath string) error {
	dataBuffer := make([]byte, 4096)

	fileHandle, _ := os.Open(filePath)
	fileReader := bufio.NewReader(fileHandle)
	fmt.Printf(".")
	for {
		_, err := fileReader.Read(dataBuffer)
		if err != nil {
			if err == io.EOF {
				break
			} else {
				fmt.Printf("\n%s:%s\n", filePath, err)
				return err
			}
		}
	}
	fileHandle.Close()
	return nil
}

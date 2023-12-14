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
							// fmt.Printf(".")

							wg.Add(1)
							go func(count int) {
								wc <- struct{}{}
								uncacheFile(filePath, count)
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
				fmt.Printf("")
				fmt.Println("Recalled", count, "files in", time.Since(timePoint))
				timePoint = time.Now()
				totalCount += count
			}
			fmt.Println()
		}
		fmt.Println("===================")
		fmt.Println("Recalled", totalCount, "files in", time.Since(timeStart))
	} else {
		fmt.Println("Please provide the input directory")
	}
}

func uncacheFile(filePath string, count int) error {
	dataBuffer := make([]byte, 4096)

	fileHandle, _ := os.Open(filePath)
	fileReader := bufio.NewReader(fileHandle)
	fmt.Println(filePath)
	for {
		_, err := fileReader.Read(dataBuffer)
		if err != nil {
			if err == io.EOF {
				break
			} else {
				fmt.Println()
				fmt.Println(filePath, ": ", err)
				return err
			}
		}
	}
	fileHandle.Close()
	return nil
}

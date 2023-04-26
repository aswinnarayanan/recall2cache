package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func main() {
	if os.Args != nil && len(os.Args) == 2 {
		inputDir := os.Args[1]

		if _, err := os.Stat(inputDir); os.IsNotExist(err) {
			log.Println("Input directory does not exist")
		} else {
			wg := sync.WaitGroup{}
			wc := make(chan struct{}, 100)
			count := 0

			timestart := time.Now()
			err := filepath.WalkDir(inputDir,
				func(filePath string, file fs.DirEntry, err error) error {
					if !file.IsDir() {
						count++
						fmt.Printf(".")

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
				log.Println(err)
			}
			fmt.Printf("\n\n")
			log.Println("File Count ", count)
			log.Println("Time taken ", time.Since(timestart))
		}
	} else {
		log.Println("Please provide the input directory")
	}
}

func uncacheFile(filePath string, count int) error {
	dataBuffer := make([]byte, 4096)

	fileHandle, _ := os.Open(filePath)
	fileReader := bufio.NewReader(fileHandle)
	for {
		_, err := fileReader.Read(dataBuffer)
		if err != nil {
			if err == io.EOF {
				break
			} else {
				log.Println(err)
				return err
			}
		}
	}
	fileHandle.Close()
	return nil
}

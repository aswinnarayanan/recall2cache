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
	"syscall"
	"time"
)

func main() {
	wg := sync.WaitGroup{}
	wc := make(chan struct{}, 100)

	inputDir := os.Args[1]
	count := 0

	timestart := time.Now()
	err := filepath.WalkDir(inputDir,
		func(filePath string, file fs.DirEntry, err error) error {
			if !file.IsDir() {
				count++

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
	log.Println(time.Since(timestart))
}

func uncacheFile(filePath string, count int) error {
	fileName := filepath.Base(filePath)
	stats, _ := os.Stat(filePath)
	st := stats.Sys().(*syscall.Stat_t)
	fmt.Printf("%s | %v | %v\n", fileName, count, st.Blocks)

	//67108864
	dataBuffer := make([]byte, 4096)

	fileHandle, _ := os.Open(filePath)
	fileReader := bufio.NewReader(fileHandle)
	for {
		_, err := fileReader.Read(dataBuffer)
		if err != nil {
			// stats, _ = os.Stat(filePath)
			// st = stats.Sys().(*syscall.Stat_t)
			// fmt.Printf("%v\n", st.Blocks)
			if err == io.EOF {
				break
			} else {
				log.Println(err)
				return err
			}
		}
	}
	fmt.Printf("\n")
	fileHandle.Close()
	return nil
}

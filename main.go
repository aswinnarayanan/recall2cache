package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func uncacheFile(path string, fileCount int, buffer []byte, err error) error {
	fileName := filepath.Base(path)
	stats, _ := os.Stat(path)
	st := stats.Sys().(*syscall.Stat_t)
	fmt.Printf("%s | %v | %v |", fileName, fileCount, st.Blocks)

	file, _ := os.Open(path)
	reader := bufio.NewReader(file)
	for {
		_, err := reader.Read(buffer)
		if err != nil {
			stats, _ = os.Stat(path)
			st = stats.Sys().(*syscall.Stat_t)
			fmt.Printf("%v\n", st.Blocks)
			if err == io.EOF {
				break
			} else {
				fmt.Println(err)
				return err
			}
		}
	}
	return nil
}

func main() {
	timestart := time.Now()
	inputDir := os.Args[1]

	buffer := make([]byte, 67108864)
	fileCount := 0
	err := filepath.WalkDir(inputDir,
		func(path string, file fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !file.IsDir() {
				fileCount++
				uncacheFile(path, fileCount, buffer, err)
			}
			return nil
		})
	if err != nil {
		log.Println(err)
	}
	elapsedtime := time.Since(timestart)
	log.Println(elapsedtime)
}

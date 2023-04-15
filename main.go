package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

func uncacheFile(path string, err error) error {
	if err != nil {
		return err
	}
	// Read byte 67108864
	f, err := os.Open(path)
	if err != nil {
		fmt.Println(err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	buf := make([]byte, 67108864)
	for {
		n, err := io.ReadFull(r, buf[:cap(buf)])
		buf = buf[:n]
		if err != nil {
			if err == io.EOF {
				fmt.Println(path, "| UNCACHED")
				break
			}
			if err != io.ErrUnexpectedEOF {
				fmt.Fprintln(os.Stderr, err)
				break
			}
		}
		fmt.Println(path, "|", n)
	}
	if err != nil {
		return err
	}
	return nil
}

func main() {
	inputDir := "./tmp"
	err := filepath.WalkDir(inputDir,
		func(path string, file fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !file.IsDir() {
				uncacheFile(path, err)
			}
			return nil
		})
	fmt.Println("== COMPLETED ==")
	if err != nil {
		log.Println(err)
	}
}

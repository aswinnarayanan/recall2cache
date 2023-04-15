package main

import (
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
)

func fileInfo(path string, file fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	if !file.IsDir() {
		fmt.Println(path)
	}
	return nil
}

func main() {
	err := filepath.WalkDir(".", fileInfo)
	if err != nil {
		log.Println(err)
	}
}

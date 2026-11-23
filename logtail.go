package main

import (
	"bytes"
	"io"
	"os"
	"strings"
)

func readLogTail(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.NewSectionReader(f, max(0, st.Size()-limit), limit))
}
func logTailLines(path string) []string {
	b, _ := readLogTail(path, 2<<20)
	if len(b) == 0 {
		return nil
	}
	return strings.Split(string(bytes.TrimSuffix(b, []byte("\n"))), "\n")
}

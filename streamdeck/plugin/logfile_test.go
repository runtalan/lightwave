package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogFileComesBackAfterItsFolderIsDeleted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Lightwave")
	l := &logFile{path: filepath.Join(dir, "streamdeck-plugin.log")}
	if _, err := l.Write([]byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Write([]byte("two\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(l.path)
	if err != nil || string(b) != "two\n" {
		t.Fatalf("log after deletion = %q, %v", b, err)
	}
}

package main

import (
	"os"
	"path/filepath"
)

// logFile is the plugin log, reopened when the file has gone. A disk cleaner
// that deletes ~/Library/Logs/Lightwave would otherwise leave every later line
// going to a file nobody can open, until Stream Deck restarts the plugin.
//
// The log package serialises calls to Write, so it needs no lock of its own.
type logFile struct {
	path string
	f    *os.File
}

func (l *logFile) Write(b []byte) (int, error) {
	if l.f != nil && !l.stillThere() {
		l.f.Close()
		l.f = nil
	}
	if l.f == nil {
		if err := l.open(); err != nil {
			// Nowhere to write. Dropping the line beats failing the caller.
			return len(b), nil
		}
	}
	return l.f.Write(b)
}

// stillThere reports whether the open file is still the one at path.
func (l *logFile) stillThere() bool {
	onDisk, err := os.Stat(l.path)
	if err != nil {
		return false
	}
	open, err := l.f.Stat()
	return err == nil && os.SameFile(onDisk, open)
}

func (l *logFile) open() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	l.f = f
	return nil
}

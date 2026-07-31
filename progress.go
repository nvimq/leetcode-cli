package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var progressMu sync.Mutex

// appendProgress appends a line: <slug>|<status>|<attempts>|<ts> to progress.md
// using a process-local mutex (CLI runs as short-lived process, so OS-level flock
// is not strictly necessary but added for safety when invoked concurrently).
func appendProgress(slug, status string, attempts int) error {
	progressMu.Lock()
	defer progressMu.Unlock()

	path := progressPath()
	if err := os.MkdirAll(parentDir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	line := fmt.Sprintf("%s|%s|%d|%s\n", slug, strings.ToUpper(status), attempts, time.Now().UTC().Format(time.RFC3339))
	if _, err := f.WriteString(line); err != nil {
		return err
	}
	return nil
}

func parentDir(p string) string {
	idx := strings.LastIndexByte(p, '/')
	if idx < 0 {
		return "."
	}
	if idx == 0 {
		return "/"
	}
	return p[:idx]
}

// ProgressCmd: `leetcode-cli progress <slug> <status> [attempts]`
func ProgressCmd(args []string) {
	EnsureEnv()
	if len(args) < 2 {
		emitErr("BAD_ARGS", "usage: leetcode-cli progress <slug> <status> [attempts]")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(args[0]))
	status := strings.ToUpper(strings.TrimSpace(args[1]))
	attempts := 0
	if len(args) >= 3 {
		if n := atoi(strings.TrimSpace(args[2])); n > 0 {
			attempts = n
		}
	}
	if err := appendProgress(slug, status, attempts); err != nil {
		emitErr("IO", err.Error())
		return
	}
	emitOK(map[string]interface{}{
		"appended": true,
		"slug":     slug,
		"status":   status,
		"attempts": attempts,
	})
}
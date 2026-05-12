// Package debug provides a process-global logger written to a file. Off by
// default; main.go enables it via Init when --debug is set. Every other
// package calls Logf unconditionally — when disabled it's a cheap no-op.
package debug

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	mu sync.Mutex
	f  *os.File
	on bool
)

func Init(path string) error {
	mu.Lock()
	defer mu.Unlock()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	f = out
	on = true
	fmt.Fprintf(f, "\n=== %s session start (pid %d) ===\n", time.Now().Format(time.RFC3339), os.Getpid())
	return nil
}

func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return on
}

func Logf(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if !on || f == nil {
		return
	}
	ts := time.Now().Format("15:04:05.000")
	fmt.Fprintf(f, "%s "+format+"\n", append([]any{ts}, args...)...)
}

func Close() {
	mu.Lock()
	defer mu.Unlock()
	if f != nil {
		_ = f.Close()
		f = nil
	}
	on = false
}

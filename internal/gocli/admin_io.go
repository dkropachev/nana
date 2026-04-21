package gocli

import (
	"io"
	"os"
	"sync"
)

var (
	adminIOMu     sync.RWMutex
	adminIOStdout io.Writer
	adminIOStderr io.Writer
)

func currentAdminStdout() io.Writer {
	adminIOMu.RLock()
	value := adminIOStdout
	adminIOMu.RUnlock()
	if value == nil {
		return os.Stdout
	}
	return value
}

func currentAdminStderr() io.Writer {
	adminIOMu.RLock()
	value := adminIOStderr
	adminIOMu.RUnlock()
	if value == nil {
		return os.Stderr
	}
	return value
}

func withAdminIO(stdout io.Writer, stderr io.Writer, fn func() error) error {
	adminIOMu.Lock()
	prevStdout := adminIOStdout
	prevStderr := adminIOStderr
	if stdout != nil {
		adminIOStdout = stdout
	}
	if stderr != nil {
		adminIOStderr = stderr
	}
	adminIOMu.Unlock()
	defer func() {
		adminIOMu.Lock()
		adminIOStdout = prevStdout
		adminIOStderr = prevStderr
		adminIOMu.Unlock()
	}()
	return fn()
}

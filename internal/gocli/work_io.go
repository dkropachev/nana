package gocli

import (
	"io"
	"os"
	"sync"
)

var (
	workIOMu     sync.RWMutex
	workIOStdout io.Writer
	workIOStderr io.Writer
)

func currentWorkStdout() io.Writer {
	workIOMu.RLock()
	value := workIOStdout
	defer workIOMu.RUnlock()
	if value == nil {
		return os.Stdout
	}
	return value
}

func currentWorkStderr() io.Writer {
	workIOMu.RLock()
	value := workIOStderr
	defer workIOMu.RUnlock()
	if value == nil {
		return os.Stderr
	}
	return value
}

func withWorkIO(stdout io.Writer, stderr io.Writer, fn func() error) error {
	workIOMu.Lock()
	prevStdout := workIOStdout
	prevStderr := workIOStderr
	if stdout != nil {
		workIOStdout = stdout
	}
	if stderr != nil {
		workIOStderr = stderr
	}
	workIOMu.Unlock()
	defer func() {
		workIOMu.Lock()
		workIOStdout = prevStdout
		workIOStderr = prevStderr
		workIOMu.Unlock()
	}()
	return fn()
}

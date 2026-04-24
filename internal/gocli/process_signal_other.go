//go:build !unix

package gocli

import "os"

func terminateProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func forceKillProcess(pid int) error {
	return terminateProcess(pid)
}

//go:build !unix

package gocli

func processAlive(pid int) bool {
	return false
}

package gocli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	processExitPoll = 100 * time.Millisecond
	sigtermGrace    = 5 * time.Second
	staleTmpMaxAge  = time.Hour
)

type ProcessEntry struct {
	PID     int
	PPID    int
	Command string
}

type CleanupCandidate struct {
	ProcessEntry
	Reason string
}

func Cleanup(args []string) error {
	if hasAnyArg(args, "--help", "-h") {
		fmt.Fprintln(os.Stdout, cleanupHelp())
		return nil
	}
	dryRun := hasAnyArg(args, "--dry-run")

	candidates, err := findCleanupCandidates(listProcesses(), os.Getpid())
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		if dryRun {
			fmt.Fprintln(os.Stdout, "Dry run: no orphaned NANA MCP server processes found.")
		} else {
			fmt.Fprintln(os.Stdout, "No orphaned NANA MCP server processes found.")
		}
	} else if dryRun {
		fmt.Fprintf(os.Stdout, "Dry run: would terminate %d orphaned NANA MCP server process(es):\n", len(candidates))
		for _, candidate := range candidates {
			fmt.Fprintf(os.Stdout, "  %s\n", formatCleanupCandidate(candidate))
		}
	} else {
		fmt.Fprintf(os.Stdout, "Found %d orphaned NANA MCP server process(es). Sending SIGTERM...\n", len(candidates))
		stillRunning := terminateCandidates(candidates, syscall.SIGTERM, sigtermGrace)
		forceKilled := 0
		if len(stillRunning) > 0 {
			fmt.Fprintf(os.Stdout, "Escalating to SIGKILL for %d process(es) still alive after %.0fs.\n", len(stillRunning), sigtermGrace.Seconds())
			forceKilled = len(stillRunning) - len(terminateCandidates(stillRunning, syscall.SIGKILL, processExitPoll))
		}
		terminated := len(candidates) - len(stillRunning) + forceKilled
		if forceKilled > 0 {
			fmt.Fprintf(os.Stdout, "Killed %d orphaned NANA MCP server process(es) (%d required SIGKILL).\n", terminated, forceKilled)
		} else {
			fmt.Fprintf(os.Stdout, "Killed %d orphaned NANA MCP server process(es).\n", terminated)
		}
	}

	removed, stalePaths, err := cleanupStaleTmpDirectories(dryRun)
	if err != nil {
		return err
	}
	if len(stalePaths) == 0 {
		if dryRun {
			fmt.Fprintln(os.Stdout, "Dry run: no stale NANA /tmp directories found.")
		} else {
			fmt.Fprintln(os.Stdout, "No stale NANA /tmp directories found.")
		}
		return nil
	}
	if dryRun {
		fmt.Fprintf(os.Stdout, "Dry run: would remove %d stale NANA /tmp directories:\n", len(stalePaths))
		for _, path := range stalePaths {
			fmt.Fprintf(os.Stdout, "  %s\n", path)
		}
		return nil
	}
	for _, path := range stalePaths {
		fmt.Fprintf(os.Stdout, "Removed stale /tmp directory: %s\n", path)
	}
	fmt.Fprintf(os.Stdout, "Removed %d stale NANA /tmp directories.\n", removed)
	return nil
}

func cleanupHelp() string {
	return strings.Join([]string{
		"Usage: nana cleanup [--dry-run]",
		"",
		"Kill orphaned NANA MCP server processes and remove stale NANA /tmp directories left behind by previous Codex App sessions.",
		"",
		"Options:",
		"  --dry-run  List matching orphaned processes and stale /tmp directories without removing them",
		"  --help     Show this help message",
	}, "\n")
}

func hasAnyArg(args []string, values ...string) bool {
	for _, arg := range args {
		for _, value := range values {
			if arg == value {
				return true
			}
		}
	}
	return false
}

func listProcesses() []ProcessEntry {
	cmd := exec.Command("ps", "axww", "-o", "pid=,ppid=,command=")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	processes := []ProcessEntry{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		processes = append(processes, ProcessEntry{
			PID:     pid,
			PPID:    ppid,
			Command: strings.Join(fields[2:], " "),
		})
	}
	return processes
}

func isNanaMcpProcess(command string) bool {
	normalized := strings.ReplaceAll(command, "\\", "/")
	return strings.Contains(normalized, "nana/dist/mcp") || strings.Contains(normalized, "/dist/mcp/")
}

func isCodexSessionProcess(command string) bool {
	normalized := strings.ReplaceAll(command, "\\", "/")
	return strings.Contains(normalized, "codex") || strings.Contains(normalized, "@openai/codex")
}

func resolveProtectedRootPID(processes []ProcessEntry, currentPID int) int {
	parentByPID := map[int]int{}
	commandByPID := map[int]string{}
	for _, process := range processes {
		parentByPID[process.PID] = process.PPID
		commandByPID[process.PID] = process.Command
	}
	pid := currentPID
	for pid > 1 {
		if command, ok := commandByPID[pid]; ok && isCodexSessionProcess(command) {
			return pid
		}
		parentPID, ok := parentByPID[pid]
		if !ok || parentPID <= 0 || parentPID == pid {
			break
		}
		pid = parentPID
	}
	return currentPID
}

func buildProtectedPIDSet(processes []ProcessEntry, currentPID int) map[int]bool {
	childrenByPID := map[int][]int{}
	for _, process := range processes {
		childrenByPID[process.PPID] = append(childrenByPID[process.PPID], process.PID)
	}
	root := resolveProtectedRootPID(processes, currentPID)
	protected := map[int]bool{}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if protected[pid] {
			continue
		}
		protected[pid] = true
		queue = append(queue, childrenByPID[pid]...)
	}
	return protected
}

func findCleanupCandidates(processes []ProcessEntry, currentPID int) ([]CleanupCandidate, error) {
	protected := buildProtectedPIDSet(processes, currentPID)
	candidates := []CleanupCandidate{}
	for _, process := range processes {
		if process.PID == currentPID || protected[process.PID] || !isNanaMcpProcess(process.Command) {
			continue
		}
		reason := "outside-current-session"
		if process.PPID <= 1 {
			reason = "ppid=1"
		}
		candidates = append(candidates, CleanupCandidate{ProcessEntry: process, Reason: reason})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].PID < candidates[j].PID })
	return candidates, nil
}

func formatCleanupCandidate(candidate CleanupCandidate) string {
	return fmt.Sprintf("PID %d (PPID %d, %s) %s", candidate.PID, candidate.PPID, candidate.Reason, candidate.Command)
}

func terminateCandidates(candidates []CleanupCandidate, sig syscall.Signal, timeout time.Duration) []CleanupCandidate {
	for _, candidate := range candidates {
		_ = syscall.Kill(candidate.PID, sig)
	}
	deadline := time.Now().Add(timeout)
	stillRunning := candidates
	for time.Now().Before(deadline) {
		next := stillRunning[:0]
		for _, candidate := range stillRunning {
			if err := syscall.Kill(candidate.PID, 0); err == nil {
				next = append(next, candidate)
			}
		}
		stillRunning = next
		if len(stillRunning) == 0 {
			break
		}
		time.Sleep(processExitPoll)
	}
	return stillRunning
}

func cleanupStaleTmpDirectories(dryRun bool) (int, []string, error) {
	root := os.TempDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, nil, err
	}
	now := time.Now()
	stale := []string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "nana-") && !strings.HasPrefix(name, "omc-") {
			continue
		}
		path := filepath.Join(root, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) <= staleTmpMaxAge {
			continue
		}
		stale = append(stale, path)
	}
	sort.Strings(stale)
	if dryRun {
		return 0, stale, nil
	}
	removed := 0
	for _, path := range stale {
		if err := os.RemoveAll(path); err != nil {
			return removed, stale, err
		}
		removed++
	}
	return removed, stale, nil
}

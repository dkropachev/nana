package gocli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	localWorkStoppedByUserReason = "stopped by user"
	localWorkStopPollAttempts    = 5
	localWorkStopPollInterval    = 100 * time.Millisecond
)

func stopLocalWork(cwd string, options localWorkStopOptions) error {
	manifest, _, err := resolveLocalWorkRun(cwd, options.RunSelection)
	if err != nil {
		return err
	}
	return stopLocalWorkManifest(manifest)
}

func localWorkStopAllowed(manifest localWorkManifest) bool {
	switch strings.ToLower(strings.TrimSpace(manifest.Status)) {
	case "running", "paused":
		return true
	default:
		return false
	}
}

func stopLocalWorkManifest(manifest localWorkManifest) error {
	switch strings.ToLower(strings.TrimSpace(manifest.Status)) {
	case "completed":
		return fmt.Errorf("work run %s is already completed", manifest.RunID)
	case "blocked":
		return fmt.Errorf("work run %s is blocked: %s", manifest.RunID, defaultString(manifest.LastError, manifest.FinalApplyError))
	case "stopped":
		return nil
	}
	if !localWorkStopAllowed(manifest) {
		return fmt.Errorf("work run %s is not actively stoppable from state %s", manifest.RunID, defaultString(strings.TrimSpace(manifest.Status), "(unknown)"))
	}

	snapshot, err := localWorkProcessSnapshot()
	if err != nil {
		return err
	}
	pids := localWorkMatchingProcessIDs(manifest, snapshot)
	for _, pid := range pids {
		_ = localWorkTerminateProcess(pid)
	}
	remaining, err := localWorkWaitForRunProcesses(manifest)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		for _, pid := range remaining {
			_ = localWorkForceKillProcess(pid)
		}
		remaining, err = localWorkWaitForRunProcesses(manifest)
		if err != nil {
			return err
		}
		if len(remaining) > 0 {
			return fmt.Errorf("failed to stop work run %s; active processes remain: %s", manifest.RunID, localWorkJoinPIDs(remaining))
		}
	}

	manifest.Status = "stopped"
	manifest.LastError = localWorkStoppedByUserReason
	manifest.PauseReason = localWorkStoppedByUserReason
	manifest.PauseUntil = ""
	manifest.PausedAt = ISOTimeNow()
	setLocalWorkProgress(&manifest, nil, "stopped", "stopped", manifest.CurrentRound)
	return writeLocalWorkManifest(manifest)
}

func localWorkMatchingProcessIDs(manifest localWorkManifest, snapshot string) []int {
	seen := map[int]struct{}{}
	ids := []int{}
	for _, line := range strings.Split(snapshot, "\n") {
		if line == "" {
			continue
		}
		if !localWorkProcessLineIsCodexWorker(line) && !localWorkProcessLineIsDetachedRunner(line) {
			continue
		}
		if !localWorkProcessLineMatchesRun(manifest, line) {
			continue
		}
		pid, ok := localWorkProcessLinePID(line)
		if !ok {
			continue
		}
		if _, exists := seen[pid]; exists {
			continue
		}
		seen[pid] = struct{}{}
		ids = append(ids, pid)
	}
	sort.Ints(ids)
	return ids
}

func localWorkProcessLinePID(line string) (int, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func localWorkWaitForRunProcesses(manifest localWorkManifest) ([]int, error) {
	remaining := []int{}
	for attempt := 0; attempt < localWorkStopPollAttempts; attempt++ {
		snapshot, err := localWorkProcessSnapshot()
		if err != nil {
			return nil, err
		}
		remaining = localWorkMatchingProcessIDs(manifest, snapshot)
		if len(remaining) == 0 {
			return nil, nil
		}
		if attempt+1 < localWorkStopPollAttempts {
			localWorkStopSleep(localWorkStopPollInterval)
		}
	}
	return remaining, nil
}

func localWorkJoinPIDs(pids []int) string {
	parts := make([]string, 0, len(pids))
	for _, pid := range pids {
		parts = append(parts, strconv.Itoa(pid))
	}
	return strings.Join(parts, ", ")
}

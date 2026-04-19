package gocli

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type codexRateLimitPolicy string

const (
	codexRateLimitPolicyWaitInProcess codexRateLimitPolicy = "wait_in_process"
	codexRateLimitPolicyReturnPause   codexRateLimitPolicy = "return_pause"
)

type codexRateLimitPauseInfo struct {
	Reason     string
	RetryAfter string
	SwitchedTo string
}

type codexRateLimitPauseError struct {
	Info codexRateLimitPauseInfo
}

func (e *codexRateLimitPauseError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{defaultString(strings.TrimSpace(e.Info.Reason), "rate limited")}
	if strings.TrimSpace(e.Info.RetryAfter) != "" {
		parts = append(parts, "retry after "+strings.TrimSpace(e.Info.RetryAfter))
	}
	return strings.Join(parts, " ")
}

func isCodexRateLimitPauseError(err error) (*codexRateLimitPauseError, bool) {
	if err == nil {
		return nil, false
	}
	var target *codexRateLimitPauseError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

func codexRateLimitPolicyDefault(policy codexRateLimitPolicy) codexRateLimitPolicy {
	if policy == "" {
		return codexRateLimitPolicyWaitInProcess
	}
	return policy
}

func codexOutputLooksRateLimited(output string) bool {
	normalized := strings.ToLower(strings.TrimSpace(output))
	if normalized == "" {
		return false
	}
	for _, needle := range []string{
		"rate limited",
		"rate limit",
		"too many requests",
		"429",
		"quota",
		"usage limit",
		"usage limit reached",
		"hit your usage limit",
		"limit reached",
		"capacity",
		"purchase more credits",
	} {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

func codexRateLimitReason(stdout string, stderr string, runErr error) string {
	combined := strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(stderr),
		strings.TrimSpace(stdout),
		errorString(runErr),
	}, "\n"))
	lines := strings.Split(combined, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if codexOutputLooksRateLimited(trimmed) {
			return trimmed
		}
	}
	if combined != "" && codexOutputLooksRateLimited(combined) {
		return combined
	}
	return "rate limited"
}

func codexResumeNeedsFreshLaunch(output string) bool {
	normalized := strings.ToLower(strings.TrimSpace(output))
	if normalized == "" {
		return false
	}
	for _, needle := range []string{
		"session not found",
		"conversation not found",
		"no matching session",
		"could not find session",
		"unknown session",
		"session does not exist",
		"unauthorized",
		"forbidden",
		"not authenticated",
		"authentication required",
	} {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

func codexPauseRetryAt(info codexRateLimitPauseInfo) (time.Time, bool) {
	return parseManagedAuthTime(info.RetryAfter)
}

func codexPauseInfoMessage(info codexRateLimitPauseInfo) string {
	parts := []string{defaultString(strings.TrimSpace(info.Reason), "rate limited")}
	if strings.TrimSpace(info.SwitchedTo) != "" {
		parts = append(parts, fmt.Sprintf("switched_to=%s", strings.TrimSpace(info.SwitchedTo)))
	}
	if strings.TrimSpace(info.RetryAfter) != "" {
		parts = append(parts, fmt.Sprintf("retry_after=%s", strings.TrimSpace(info.RetryAfter)))
	}
	return strings.Join(parts, " ")
}

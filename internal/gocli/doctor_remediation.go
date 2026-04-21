package gocli

import (
	"fmt"
	"strings"
)

func setupFixCommand(scope string) string {
	return fmt.Sprintf("nana setup --scope %s", normalizeSetupFixScope(scope))
}

func setupForceFixCommand(scope string) string {
	return fmt.Sprintf("nana setup --force --scope %s", normalizeSetupFixScope(scope))
}

func normalizeSetupFixScope(scope string) string {
	if strings.TrimSpace(scope) == "project" {
		return "project"
	}
	return "user"
}

func setupDoctorRemediation(scope string, path string, manualFallback string) *doctorRemediation {
	return &doctorRemediation{
		Path:             strings.TrimSpace(path),
		SafeAutomaticFix: fmt.Sprintf("yes — run `%s`", setupFixCommand(scope)),
		ManualFallback:   strings.TrimSpace(manualFallback),
	}
}

func manualDoctorRemediation(path string, manualFallback string) *doctorRemediation {
	return &doctorRemediation{
		Path:             strings.TrimSpace(path),
		SafeAutomaticFix: "no — manual review required",
		ManualFallback:   strings.TrimSpace(manualFallback),
	}
}

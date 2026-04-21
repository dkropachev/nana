package gocli

import (
	"fmt"
	"os"
	"strings"
	"time"
)

var managedAuthDisplayLocation = func() *time.Location {
	if raw := strings.TrimSpace(os.Getenv("TZ")); raw != "" {
		if location, err := time.LoadLocation(raw); err == nil {
			return location
		}
	}
	return time.Local
}

type managedAccountLimitProduct struct {
	Name      string
	RateLimit *codexUsageRateLimit
}

func limitsManagedAccounts(codexHome string) error {
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return err
	}
	if len(registry.Accounts) == 0 {
		fmt.Fprintf(os.Stdout, "No managed accounts. Use `nana account pull` or `nana account add`.\n")
		return nil
	}

	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		return err
	}
	settings := resolveManagedAuthSettings(codexHome)
	now := managedAuthNow().UTC()
	location := managedAuthDisplayLocation()
	if location == nil {
		location = time.Local
	}

	var output strings.Builder
	var commandErr error
	for _, account := range registry.orderedAccounts() {
		check := managedAuthFetchUsage(account, settings)
		accountState := state.Accounts[account.Name]
		mergeAccountUsageState(&accountState, check, now, settings)
		state.Accounts[account.Name] = accountState

		if check.payload == nil {
			if commandErr == nil {
				detail := displayOrFallback(errString(check.err), displayOrFallback(check.result, "usage check failed"))
				commandErr = fmt.Errorf("managed account %q usage check failed: %s", account.Name, detail)
			}
			continue
		}

		profile, readErr := readManagedAccountProfile(account.AuthPath)
		if readErr != nil {
			if commandErr == nil {
				commandErr = fmt.Errorf("managed account %q profile read failed: %w", account.Name, readErr)
			}
			continue
		}

		if output.Len() > 0 {
			output.WriteByte('\n')
		}
		renderManagedAccountLimits(&output, account.Name, profile, check.payload, now, location)
	}

	if err := saveManagedAuthRuntimeState(codexHome, state); err != nil {
		return err
	}
	if commandErr != nil {
		return commandErr
	}
	_, err = fmt.Fprint(os.Stdout, output.String())
	return err
}

func renderManagedAccountLimits(output *strings.Builder, name string, profile managedAccountProfile, payload *codexUsageResponse, now time.Time, location *time.Location) {
	if output == nil || payload == nil {
		return
	}

	accountID := ""
	if profile.Tokens != nil {
		accountID = strings.TrimSpace(profile.Tokens.AccountID)
	}

	fmt.Fprintf(output, "%s\n", name)
	fmt.Fprintf(output, "  email: %s\n", displayOrFallback(managedAccountProfileEmail(profile), "(unknown)"))
	fmt.Fprintf(output, "  account_id: %s\n", displayOrFallback(accountID, "(unknown)"))
	fmt.Fprintf(output, "  plan: %s\n", displayOrFallback(strings.TrimSpace(payload.PlanType), "(unknown)"))

	for _, product := range managedAccountLimitProducts(payload) {
		fmt.Fprintf(output, "  %s: %s\n", product.Name, managedAccountAvailabilityWord(product.RateLimit))
		if product.RateLimit == nil {
			continue
		}
		appendManagedAccountLimitWindow(output, managedAccountWindowLabel(product.RateLimit.PrimaryWindow, "5h"), product.RateLimit.PrimaryWindow, now, location)
		appendManagedAccountLimitWindow(output, managedAccountWindowLabel(product.RateLimit.SecondaryWindow, "weekly"), product.RateLimit.SecondaryWindow, now, location)
	}
}

func managedAccountLimitProducts(payload *codexUsageResponse) []managedAccountLimitProduct {
	if payload == nil {
		return nil
	}

	products := []managedAccountLimitProduct{{
		Name:      "Codex",
		RateLimit: payload.RateLimit,
	}}
	for _, limit := range payload.AdditionalRateLimits {
		name := strings.TrimSpace(limit.LimitName)
		if name == "" {
			name = strings.TrimSpace(limit.MeteredFeature)
		}
		if name == "" {
			continue
		}
		products = append(products, managedAccountLimitProduct{
			Name:      name,
			RateLimit: limit.RateLimit,
		})
	}
	return products
}

func managedAccountProfileEmail(profile managedAccountProfile) string {
	if profile.Tokens == nil {
		return ""
	}
	claims, ok := parseManagedAccountIDTokenClaims(profile.Tokens.IDToken)
	if !ok {
		return ""
	}
	return strings.TrimSpace(claims.Email)
}

func managedAccountAvailabilityWord(rateLimit *codexUsageRateLimit) string {
	if rateLimit != nil && rateLimit.Allowed && !rateLimit.LimitReached {
		return "available"
	}
	return "unavailable"
}

func appendManagedAccountLimitWindow(output *strings.Builder, label string, window *codexUsageWindow, now time.Time, location *time.Location) {
	if output == nil || window == nil {
		return
	}

	usedPercent := clampPercent(window.UsedPercent)
	retryAt := usageWindowRetryAt(window)
	if retryAt.IsZero() {
		fmt.Fprintf(output, "    %s: %d%% used; refreshes unknown\n", label, usedPercent)
		return
	}

	fmt.Fprintf(output, "    %s: %d%% used; refreshes %s (in %s)\n",
		label,
		usedPercent,
		retryAt.In(location).Format("2006-01-02 15:04:05 MST"),
		formatManagedAccountRelativeDuration(retryAt.Sub(now)),
	)
}

func managedAccountWindowLabel(window *codexUsageWindow, fallback string) string {
	if window == nil {
		return fallback
	}

	switch window.LimitWindowSeconds {
	case int64((5 * time.Hour) / time.Second):
		return "5h"
	case int64((7 * 24 * time.Hour) / time.Second):
		return "weekly"
	default:
		return fallback
	}
}

func formatManagedAccountRelativeDuration(value time.Duration) string {
	if value <= 0 {
		return "0s"
	}

	remaining := int64(value.Round(time.Second) / time.Second)
	if remaining <= 0 {
		return "0s"
	}

	type durationUnit struct {
		seconds int64
		suffix  string
	}

	includeSeconds := remaining < int64(time.Hour/time.Second)
	units := []durationUnit{
		{seconds: int64((7 * 24 * time.Hour) / time.Second), suffix: "w"},
		{seconds: int64((24 * time.Hour) / time.Second), suffix: "d"},
		{seconds: int64(time.Hour / time.Second), suffix: "h"},
		{seconds: int64(time.Minute / time.Second), suffix: "m"},
		{seconds: 1, suffix: "s"},
	}

	parts := make([]string, 0, 3)
	for _, unit := range units {
		if unit.suffix == "s" && !includeSeconds {
			continue
		}
		if remaining < unit.seconds {
			continue
		}
		count := remaining / unit.seconds
		remaining %= unit.seconds
		parts = append(parts, fmt.Sprintf("%d%s", count, unit.suffix))
		if len(parts) == 3 {
			break
		}
	}

	if len(parts) == 0 {
		return "0s"
	}
	return strings.Join(parts, " ")
}

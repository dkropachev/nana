package gocli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAuthUsageSwitchThresholdPct = 95
	defaultAuthUsagePollInterval       = time.Minute
	defaultAuthHTTPTimeout             = 10 * time.Second
	defaultAuthUsageRetryMaxAttempts   = 3
	defaultAuthUsageRetryBaseDelay     = 500 * time.Millisecond
	defaultAuthUsageStaleAfter         = 5 * time.Minute
	defaultAuthUsageMinActiveDwell     = 5 * time.Minute
	defaultAuthUsageReturnDwell        = 10 * time.Minute

	authConfigUsageThresholdKey     = "nana_account_usage_threshold_pct"
	authConfigUsagePollSecondsKey   = "nana_account_usage_poll_interval_seconds"
	authConfigUsageEndpointKey      = "nana_account_usage_endpoint_url"
	authConfigRefreshURLKey         = "nana_account_refresh_url"
	authConfigRefreshClientIDKey    = "nana_account_refresh_client_id"
	authConfigUsageRetryAttemptsKey = "nana_account_usage_retry_max_attempts"
	authConfigUsageRetryDelayMsKey  = "nana_account_usage_retry_base_delay_ms"
	authConfigUsageStaleAfterKey    = "nana_account_usage_stale_after_seconds"
	authConfigUsageMinDwellKey      = "nana_account_min_active_dwell_seconds"
	authConfigUsageReturnDwellKey   = "nana_account_preferred_return_dwell_seconds"

	authEnvUsageThresholdPct         = "NANA_ACCOUNT_USAGE_THRESHOLD_PCT"
	authEnvUsagePollIntervalSeconds  = "NANA_ACCOUNT_USAGE_POLL_INTERVAL_SECONDS"
	authEnvUsageEndpointURL          = "NANA_ACCOUNT_USAGE_ENDPOINT_URL"
	authEnvRefreshURL                = "NANA_ACCOUNT_REFRESH_URL"
	authEnvRefreshClientID           = "NANA_ACCOUNT_REFRESH_CLIENT_ID"
	authEnvUsageRetryMaxAttempts     = "NANA_ACCOUNT_USAGE_RETRY_MAX_ATTEMPTS"
	authEnvUsageRetryBaseDelayMs     = "NANA_ACCOUNT_USAGE_RETRY_BASE_DELAY_MS"
	authEnvUsageStaleAfterSeconds    = "NANA_ACCOUNT_USAGE_STALE_AFTER_SECONDS"
	authEnvUsageMinActiveDwell       = "NANA_ACCOUNT_MIN_ACTIVE_DWELL_SECONDS"
	authEnvUsagePreferredReturnDwell = "NANA_ACCOUNT_PREFERRED_RETURN_DWELL_SECONDS"

	chatGPTUsageSource          = "chatgpt-wham-usage"
	accountUsageResultOK        = "ok"
	accountUsageResultStale     = "stale"
	accountUsageResultTransient = "transient_error"
	accountUsageResultPermanent = "permanent_auth_error"
)

var managedAuthRetrySleep = time.Sleep

type managedAuthSettings struct {
	usageThresholdPct    int
	pollInterval         time.Duration
	httpTimeout          time.Duration
	usageEndpointURL     string
	refreshURL           string
	refreshClientID      string
	retryMaxAttempts     int
	retryBaseDelay       time.Duration
	staleAfter           time.Duration
	minActiveDwell       time.Duration
	preferredReturnDwell time.Duration
}

type managedAccountProfile struct {
	AuthMode string                       `json:"auth_mode,omitempty"`
	Tokens   *managedAccountTokenEnvelope `json:"tokens,omitempty"`
}

type managedAccountTokenEnvelope struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
}

type managedAccountIdentityClaims struct {
	Email             string `json:"email,omitempty"`
	PreferredUsername string `json:"preferred_username,omitempty"`
	Login             string `json:"login,omitempty"`
	Username          string `json:"username,omitempty"`
	Nickname          string `json:"nickname,omitempty"`
	Name              string `json:"name,omitempty"`
}

type codexUsageResponse struct {
	PlanType             string                    `json:"plan_type,omitempty"`
	RateLimit            *codexUsageRateLimit      `json:"rate_limit,omitempty"`
	AdditionalRateLimits []codexAdditionalRateInfo `json:"additional_rate_limits,omitempty"`
	Credits              *codexUsageCredits        `json:"credits,omitempty"`
	SpendControl         *codexSpendControl        `json:"spend_control,omitempty"`
}

type codexAdditionalRateInfo struct {
	LimitName      string               `json:"limit_name,omitempty"`
	MeteredFeature string               `json:"metered_feature,omitempty"`
	RateLimit      *codexUsageRateLimit `json:"rate_limit,omitempty"`
}

type codexUsageRateLimit struct {
	Allowed         bool              `json:"allowed"`
	LimitReached    bool              `json:"limit_reached"`
	PrimaryWindow   *codexUsageWindow `json:"primary_window,omitempty"`
	SecondaryWindow *codexUsageWindow `json:"secondary_window,omitempty"`
}

type codexUsageWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type codexUsageCredits struct {
	HasCredits          bool   `json:"has_credits"`
	Unlimited           bool   `json:"unlimited"`
	OverageLimitReached bool   `json:"overage_limit_reached"`
	Balance             string `json:"balance,omitempty"`
}

type codexSpendControl struct {
	Reached bool `json:"reached"`
}

type managedAccountUsageSnapshot struct {
	PlanType         string
	FiveHourUsedPct  *int
	WeeklyUsedPct    *int
	CreditsAvailable *bool
	SpendControlHit  bool
	NearLimit        bool
	HardLimit        bool
	PrimaryRetryAt   string
	SecondaryRetryAt string
	RetryAfter       string
}

type managedAccountUsageCheck struct {
	authMode string
	snapshot *managedAccountUsageSnapshot
	result   string
	err      error
}

type managedAccountRequestResult struct {
	body       []byte
	statusCode int
	err        error
}

func resolveManagedAuthSettings(codexHome string) managedAuthSettings {
	settings := managedAuthSettings{
		usageThresholdPct:    defaultAuthUsageSwitchThresholdPct,
		pollInterval:         defaultAuthUsagePollInterval,
		httpTimeout:          defaultAuthHTTPTimeout,
		usageEndpointURL:     "https://chatgpt.com/backend-api/wham/usage",
		refreshURL:           "https://auth.openai.com/oauth/token",
		refreshClientID:      "app_EMoamEEZ73f0CkXaXp7hrann",
		retryMaxAttempts:     defaultAuthUsageRetryMaxAttempts,
		retryBaseDelay:       defaultAuthUsageRetryBaseDelay,
		staleAfter:           defaultAuthUsageStaleAfter,
		minActiveDwell:       defaultAuthUsageMinActiveDwell,
		preferredReturnDwell: defaultAuthUsageReturnDwell,
	}

	configPath := filepath.Join(codexHome, "config.toml")
	if content, err := os.ReadFile(configPath); err == nil {
		text := string(content)
		if value, ok := ReadTopLevelTomlInt(text, authConfigUsageThresholdKey); ok {
			settings.usageThresholdPct = clampSettingInt(value, 1, 100, settings.usageThresholdPct)
		}
		if value, ok := ReadTopLevelTomlInt(text, authConfigUsagePollSecondsKey); ok {
			settings.pollInterval = clampDurationSeconds(value, time.Second, 24*time.Hour, settings.pollInterval)
		}
		if value := ReadTopLevelTomlString(text, authConfigUsageEndpointKey); strings.TrimSpace(value) != "" {
			settings.usageEndpointURL = strings.TrimSpace(value)
		}
		if value := ReadTopLevelTomlString(text, authConfigRefreshURLKey); strings.TrimSpace(value) != "" {
			settings.refreshURL = strings.TrimSpace(value)
		}
		if value := ReadTopLevelTomlString(text, authConfigRefreshClientIDKey); strings.TrimSpace(value) != "" {
			settings.refreshClientID = strings.TrimSpace(value)
		}
		if value, ok := ReadTopLevelTomlInt(text, authConfigUsageRetryAttemptsKey); ok {
			settings.retryMaxAttempts = clampSettingInt(value, 1, 10, settings.retryMaxAttempts)
		}
		if value, ok := ReadTopLevelTomlInt(text, authConfigUsageRetryDelayMsKey); ok {
			settings.retryBaseDelay = clampDurationMilliseconds(value, 50*time.Millisecond, 30*time.Second, settings.retryBaseDelay)
		}
		if value, ok := ReadTopLevelTomlInt(text, authConfigUsageStaleAfterKey); ok {
			settings.staleAfter = clampDurationSeconds(value, time.Second, 7*24*time.Hour, settings.staleAfter)
		}
		if value, ok := ReadTopLevelTomlInt(text, authConfigUsageMinDwellKey); ok {
			settings.minActiveDwell = clampDurationSeconds(value, 0, 7*24*time.Hour, settings.minActiveDwell)
		}
		if value, ok := ReadTopLevelTomlInt(text, authConfigUsageReturnDwellKey); ok {
			settings.preferredReturnDwell = clampDurationSeconds(value, 0, 7*24*time.Hour, settings.preferredReturnDwell)
		}
	}

	if value := strings.TrimSpace(os.Getenv(authEnvUsageEndpointURL)); value != "" {
		settings.usageEndpointURL = value
	}
	if value := strings.TrimSpace(os.Getenv(authEnvRefreshURL)); value != "" {
		settings.refreshURL = value
	}
	if value := strings.TrimSpace(os.Getenv(authEnvRefreshClientID)); value != "" {
		settings.refreshClientID = value
	}
	if value, ok := readEnvInt(authEnvUsageThresholdPct); ok {
		settings.usageThresholdPct = clampSettingInt(value, 1, 100, settings.usageThresholdPct)
	}
	if value, ok := readEnvInt(authEnvUsagePollIntervalSeconds); ok {
		settings.pollInterval = clampDurationSeconds(value, time.Second, 24*time.Hour, settings.pollInterval)
	}
	if value, ok := readEnvInt(authEnvUsageRetryMaxAttempts); ok {
		settings.retryMaxAttempts = clampSettingInt(value, 1, 10, settings.retryMaxAttempts)
	}
	if value, ok := readEnvInt(authEnvUsageRetryBaseDelayMs); ok {
		settings.retryBaseDelay = clampDurationMilliseconds(value, 50*time.Millisecond, 30*time.Second, settings.retryBaseDelay)
	}
	if value, ok := readEnvInt(authEnvUsageStaleAfterSeconds); ok {
		settings.staleAfter = clampDurationSeconds(value, time.Second, 7*24*time.Hour, settings.staleAfter)
	}
	if value, ok := readEnvInt(authEnvUsageMinActiveDwell); ok {
		settings.minActiveDwell = clampDurationSeconds(value, 0, 7*24*time.Hour, settings.minActiveDwell)
	}
	if value, ok := readEnvInt(authEnvUsagePreferredReturnDwell); ok {
		settings.preferredReturnDwell = clampDurationSeconds(value, 0, 7*24*time.Hour, settings.preferredReturnDwell)
	}

	return settings
}

func readManagedAccountProfile(path string) (managedAccountProfile, error) {
	var profile managedAccountProfile
	content, err := os.ReadFile(path)
	if err != nil {
		return profile, err
	}
	if err := json.Unmarshal(content, &profile); err != nil {
		return profile, err
	}
	return profile, nil
}

func managedAccountProfileDisplayIdentity(profile managedAccountProfile) string {
	if profile.Tokens == nil {
		return ""
	}
	claims, ok := parseManagedAccountIDTokenClaims(profile.Tokens.IDToken)
	if !ok {
		return ""
	}
	for _, candidate := range []string{
		strings.TrimSpace(claims.Email),
		strings.TrimSpace(claims.PreferredUsername),
		strings.TrimSpace(claims.Login),
		strings.TrimSpace(claims.Username),
		strings.TrimSpace(claims.Nickname),
		strings.TrimSpace(claims.Name),
	} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func parseManagedAccountIDTokenClaims(idToken string) (managedAccountIdentityClaims, bool) {
	idToken = strings.TrimSpace(idToken)
	if idToken == "" {
		return managedAccountIdentityClaims{}, false
	}
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return managedAccountIdentityClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return managedAccountIdentityClaims{}, false
	}
	var claims managedAccountIdentityClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return managedAccountIdentityClaims{}, false
	}
	return claims, true
}

func writeManagedAccountProfile(path string, profile managedAccountProfile) error {
	content, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0o644)
}

func isChatGPTBackedAuthMode(mode string) bool {
	mode = strings.TrimSpace(strings.ToLower(mode))
	return mode == "chatgpt" || mode == "chatgpt_auth_tokens"
}

func fetchManagedAccountUsage(account ManagedAuthAccount, settings managedAuthSettings) managedAccountUsageCheck {
	profile, err := readManagedAccountProfile(account.AuthPath)
	if err != nil {
		return managedAccountUsageCheck{result: accountUsageResultPermanent, err: err}
	}
	if !isChatGPTBackedAuthMode(profile.AuthMode) {
		return managedAccountUsageCheck{authMode: profile.AuthMode, result: accountUsageResultPermanent, err: fmt.Errorf("unsupported auth mode %q", profile.AuthMode)}
	}
	if profile.Tokens == nil {
		return managedAccountUsageCheck{authMode: profile.AuthMode, result: accountUsageResultPermanent, err: fmt.Errorf("missing token payload")}
	}
	if strings.TrimSpace(profile.Tokens.AccountID) == "" {
		return managedAccountUsageCheck{authMode: profile.AuthMode, result: accountUsageResultPermanent, err: fmt.Errorf("missing ChatGPT account id")}
	}
	if strings.TrimSpace(profile.Tokens.AccessToken) == "" {
		return managedAccountUsageCheck{authMode: profile.AuthMode, result: accountUsageResultPermanent, err: fmt.Errorf("missing ChatGPT access token")}
	}

	requestResult := fetchChatGPTUsage(profile.Tokens.AccessToken, profile.Tokens.AccountID, settings)
	if requestResult.statusCode == http.StatusUnauthorized {
		if strings.TrimSpace(profile.Tokens.RefreshToken) == "" {
			return managedAccountUsageCheck{authMode: profile.AuthMode, result: accountUsageResultPermanent, err: fmt.Errorf("missing refresh token")}
		}
		refreshed, refreshErr := refreshManagedAccountTokens(profile, settings)
		if refreshErr != nil {
			return managedAccountUsageCheck{authMode: profile.AuthMode, result: accountUsageResultPermanent, err: refreshErr}
		}
		profile = refreshed
		if err := writeManagedAccountProfile(account.AuthPath, profile); err != nil {
			return managedAccountUsageCheck{authMode: profile.AuthMode, result: accountUsageResultPermanent, err: err}
		}
		requestResult = fetchChatGPTUsage(profile.Tokens.AccessToken, profile.Tokens.AccountID, settings)
	}

	if requestResult.err != nil {
		return managedAccountUsageCheck{authMode: profile.AuthMode, result: classifyUsageError(requestResult.statusCode), err: requestResult.err}
	}
	if requestResult.statusCode < 200 || requestResult.statusCode >= 300 {
		err = fmt.Errorf("usage request failed: status %d body=%s", requestResult.statusCode, truncateAuthError(requestResult.body))
		return managedAccountUsageCheck{authMode: profile.AuthMode, result: classifyUsageError(requestResult.statusCode), err: err}
	}

	var payload codexUsageResponse
	if err := json.Unmarshal(requestResult.body, &payload); err != nil {
		return managedAccountUsageCheck{authMode: profile.AuthMode, result: accountUsageResultTransient, err: err}
	}
	return managedAccountUsageCheck{
		authMode: profile.AuthMode,
		result:   accountUsageResultOK,
		snapshot: buildUsageSnapshot(&payload, settings),
	}
}

func refreshManagedAccountTokens(profile managedAccountProfile, settings managedAuthSettings) (managedAccountProfile, error) {
	body := url.Values{}
	body.Set("grant_type", "refresh_token")
	body.Set("refresh_token", profile.Tokens.RefreshToken)
	body.Set("client_id", settings.refreshClientID)

	request, err := http.NewRequest(http.MethodPost, settings.refreshURL, strings.NewReader(body.Encode()))
	if err != nil {
		return profile, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	result := runManagedAccountRequest(request, settings)
	if result.err != nil {
		return profile, result.err
	}
	if result.statusCode < 200 || result.statusCode >= 300 {
		return profile, fmt.Errorf("refresh failed: status %d body=%s", result.statusCode, truncateAuthError(result.body))
	}

	var refreshed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(result.body, &refreshed); err != nil {
		return profile, err
	}
	if strings.TrimSpace(refreshed.AccessToken) == "" {
		return profile, fmt.Errorf("refresh response missing access token")
	}
	profile.Tokens.AccessToken = refreshed.AccessToken
	if strings.TrimSpace(refreshed.RefreshToken) != "" {
		profile.Tokens.RefreshToken = refreshed.RefreshToken
	}
	if strings.TrimSpace(refreshed.IDToken) != "" {
		profile.Tokens.IDToken = refreshed.IDToken
	}
	return profile, nil
}

func fetchChatGPTUsage(accessToken string, accountID string, settings managedAuthSettings) managedAccountRequestResult {
	request, err := http.NewRequest(http.MethodGet, settings.usageEndpointURL, nil)
	if err != nil {
		return managedAccountRequestResult{err: err}
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("ChatGPT-Account-Id", accountID)
	request.Header.Set("User-Agent", "codex-cli")
	return runManagedAccountRequest(request, settings)
}

func runManagedAccountRequest(request *http.Request, settings managedAuthSettings) managedAccountRequestResult {
	client := &http.Client{Timeout: settings.httpTimeout}
	var last managedAccountRequestResult
	for attempt := 0; attempt < settings.retryMaxAttempts; attempt++ {
		cloned := request.Clone(request.Context())
		response, err := client.Do(cloned)
		if err != nil {
			last = managedAccountRequestResult{err: err}
		} else {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			last = managedAccountRequestResult{body: body, statusCode: response.StatusCode, err: readErr}
			if readErr == nil && !shouldRetryHTTPStatus(response.StatusCode) {
				return last
			}
		}
		if attempt >= settings.retryMaxAttempts-1 || !shouldRetryRequest(last) {
			break
		}
		managedAuthRetrySleep(retryDelayForAttempt(settings.retryBaseDelay, attempt))
	}
	return last
}

func buildUsageSnapshot(payload *codexUsageResponse, settings managedAuthSettings) *managedAccountUsageSnapshot {
	if payload == nil {
		return nil
	}
	snapshot := &managedAccountUsageSnapshot{
		PlanType: strings.TrimSpace(payload.PlanType),
	}
	if payload.RateLimit != nil {
		if payload.RateLimit.PrimaryWindow != nil {
			value := clampPercent(payload.RateLimit.PrimaryWindow.UsedPercent)
			snapshot.FiveHourUsedPct = &value
			snapshot.PrimaryRetryAt = managedAuthTimeString(usageWindowRetryAt(payload.RateLimit.PrimaryWindow))
			if value >= settings.usageThresholdPct {
				snapshot.NearLimit = true
			}
		}
		if payload.RateLimit.SecondaryWindow != nil {
			value := clampPercent(payload.RateLimit.SecondaryWindow.UsedPercent)
			snapshot.WeeklyUsedPct = &value
			snapshot.SecondaryRetryAt = managedAuthTimeString(usageWindowRetryAt(payload.RateLimit.SecondaryWindow))
			if value >= settings.usageThresholdPct {
				snapshot.NearLimit = true
			}
		}
		if payload.RateLimit.LimitReached || !payload.RateLimit.Allowed {
			snapshot.HardLimit = true
		}
	}
	if payload.Credits != nil {
		available := payload.Credits.HasCredits || payload.Credits.Unlimited
		snapshot.CreditsAvailable = &available
		if payload.Credits.OverageLimitReached {
			snapshot.HardLimit = true
		}
	}
	if payload.SpendControl != nil && payload.SpendControl.Reached {
		snapshot.SpendControlHit = true
		snapshot.HardLimit = true
	}
	snapshot.RetryAfter = managedAuthTimeString(earliestManagedUsageRetryAt(snapshot, settings))
	return snapshot
}

func mergeAccountUsageState(accountState *ManagedAuthAccountState, check managedAccountUsageCheck, now time.Time, settings managedAuthSettings) {
	accountState.LastUsageCheckAt = now.Format(time.RFC3339Nano)
	accountState.LastUsageSource = chatGPTUsageSource
	accountState.AuthMode = strings.TrimSpace(check.authMode)
	if check.snapshot != nil {
		accountState.PlanType = check.snapshot.PlanType
		accountState.FiveHourUsedPct = check.snapshot.FiveHourUsedPct
		accountState.WeeklyUsedPct = check.snapshot.WeeklyUsedPct
		accountState.CreditsAvailable = check.snapshot.CreditsAvailable
		accountState.SpendControlHit = check.snapshot.SpendControlHit
		accountState.LimitReached = check.snapshot.HardLimit
		accountState.PrimaryRetryAfter = check.snapshot.PrimaryRetryAt
		accountState.SecondaryRetryAfter = check.snapshot.SecondaryRetryAt
		accountState.RetryAfter = check.snapshot.RetryAfter
		accountState.LastSuccessfulUsageCheckAt = now.Format(time.RFC3339Nano)
		accountState.LastUsageFreshUntil = now.Add(settings.staleAfter).Format(time.RFC3339Nano)
		accountState.LastUsageResult = accountUsageResultOK
		accountState.LastUsageError = ""
		return
	}

	if strings.TrimSpace(accountState.LastSuccessfulUsageCheckAt) != "" && !lastUsageFreshUntilExpired(*accountState, now) && check.result == accountUsageResultTransient {
		accountState.LastUsageResult = accountUsageResultStale
		accountState.LastUsageError = errString(check.err)
		return
	}

	accountState.LastUsageResult = check.result
	accountState.LastUsageError = errString(check.err)
	if check.result == accountUsageResultTransient || check.result == accountUsageResultPermanent {
		accountState.LimitReached = false
		accountState.SpendControlHit = false
	}
}

func usageWindowRetryAt(window *codexUsageWindow) time.Time {
	if window == nil {
		return time.Time{}
	}
	if window.ResetAt > 0 {
		return time.Unix(window.ResetAt, 0).UTC()
	}
	if window.ResetAfterSeconds > 0 {
		return time.Now().UTC().Add(time.Duration(window.ResetAfterSeconds) * time.Second)
	}
	return time.Time{}
}

func earliestManagedUsageRetryAt(snapshot *managedAccountUsageSnapshot, settings managedAuthSettings) time.Time {
	if snapshot == nil {
		return time.Time{}
	}
	candidates := []string{}
	if snapshot.HardLimit {
		candidates = append(candidates, snapshot.PrimaryRetryAt, snapshot.SecondaryRetryAt)
	} else if snapshot.NearLimit {
		if snapshot.FiveHourUsedPct != nil && *snapshot.FiveHourUsedPct >= settings.usageThresholdPct {
			candidates = append(candidates, snapshot.PrimaryRetryAt)
		}
		if snapshot.WeeklyUsedPct != nil && *snapshot.WeeklyUsedPct >= settings.usageThresholdPct {
			candidates = append(candidates, snapshot.SecondaryRetryAt)
		}
	}
	best := time.Time{}
	for _, raw := range candidates {
		retryAt, ok := parseManagedAuthTime(raw)
		if !ok {
			continue
		}
		if best.IsZero() || retryAt.Before(best) {
			best = retryAt
		}
	}
	return best
}

func managedAuthTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func shouldRetryRequest(result managedAccountRequestResult) bool {
	if result.err != nil {
		return isTransientManagedAccountError(result.err)
	}
	return shouldRetryHTTPStatus(result.statusCode)
}

func isTransientManagedAccountError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isTransientManagedAccountError(urlErr.Err)
	}
	return false
}

func shouldRetryHTTPStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusRequestTimeout ||
		statusCode == http.StatusTooEarly ||
		statusCode == http.StatusBadGateway ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusGatewayTimeout ||
		statusCode >= 500
}

func retryDelayForAttempt(base time.Duration, attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := base * time.Duration(1<<attempt)
	if delay > 10*time.Second {
		delay = 10 * time.Second
	}
	jitter := time.Duration((attempt+1)*37) * time.Millisecond
	return delay + jitter
}

func classifyUsageError(statusCode int) string {
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return accountUsageResultPermanent
	}
	return accountUsageResultTransient
}

func lastUsageFreshUntilExpired(state ManagedAuthAccountState, now time.Time) bool {
	if strings.TrimSpace(state.LastUsageFreshUntil) == "" {
		return true
	}
	freshUntil, ok := parseManagedAuthTime(state.LastUsageFreshUntil)
	if !ok {
		return true
	}
	return now.After(freshUntil)
}

func clampPercent(value float64) int {
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	return int(value + 0.5)
}

func truncateAuthError(body []byte) string {
	trimmed := strings.TrimSpace(string(bytes.TrimSpace(body)))
	if len(trimmed) > 200 {
		return trimmed[:200]
	}
	return trimmed
}

func syncManagedAccountToResolvedAuth(profilePath string, codexHome string) error {
	return copyFile(profilePath, filepath.Join(codexHome, "auth.json"))
}

func clampSettingInt(value int, min int, max int, fallback int) int {
	if value < min || value > max {
		return fallback
	}
	return value
}

func clampDurationSeconds(value int, min time.Duration, max time.Duration, fallback time.Duration) time.Duration {
	duration := time.Duration(value) * time.Second
	if duration < min || duration > max {
		return fallback
	}
	return duration
}

func clampDurationMilliseconds(value int, min time.Duration, max time.Duration, fallback time.Duration) time.Duration {
	duration := time.Duration(value) * time.Millisecond
	if duration < min || duration > max {
		return fallback
	}
	return duration
}

func readEnvInt(key string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

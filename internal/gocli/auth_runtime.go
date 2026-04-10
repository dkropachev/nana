package gocli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type managedAuthManager struct {
	cwd          string
	codexHome    string
	sessionStart time.Time
	settings     managedAuthSettings
	mu           sync.Mutex
	registry     ManagedAuthRegistry
	state        ManagedAuthRuntimeState
}

func prepareManagedAuthManager(cwd string, codexHome string) (*managedAuthManager, error) {
	if strings.TrimSpace(codexHome) == "" {
		codexHome = ResolveCodexHomeForLaunch(cwd)
	}
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return nil, err
	}
	if len(registry.Accounts) == 0 {
		imported, importErr := bootstrapResolvedCodexAuth(cwd)
		if importErr != nil {
			return nil, importErr
		}
		if imported {
			registry, err = loadManagedAuthRegistry(codexHome)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(registry.Accounts) == 0 {
		return nil, nil
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		return nil, err
	}
	manager := &managedAuthManager{
		cwd:       cwd,
		codexHome: codexHome,
		settings:  resolveManagedAuthSettings(codexHome),
		registry:  registry,
		state:     state,
	}
	if err := manager.ensureLaunchAccount(time.Now().UTC(), "launch"); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *managedAuthManager) start(stop <-chan struct{}, sessionStart time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.sessionStart = sessionStart.UTC()
	interval := m.settings.pollInterval
	m.mu.Unlock()
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = m.evaluateUsage()
			}
		}
	}()
}

func (m *managedAuthManager) wrapOutput(target io.Writer) io.Writer {
	return target
}

func (m *managedAuthManager) evaluateUsage() error {
	if m == nil {
		return nil
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.evaluateUsageLocked(now, true)
}

func (m *managedAuthManager) ensureLaunchAccount(now time.Time, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.evaluateUsageLocked(now, false); err != nil {
		return err
	}
	pending := normalizeManagedAuthName(m.state.PendingActive)
	if pending != "" && pending != normalizeManagedAuthName(m.state.Active) && m.freshEligibleLocked(pending, now) {
		return m.activateAccountLocked(pending, now, reason)
	}

	name := m.chooseLaunchAccountLocked(now)
	if name == "" {
		m.markDecisionLocked(now, "no-account-selected")
		return m.persistStateLocked()
	}
	if m.state.Active == name {
		clearPendingAccountSwitch(&m.state)
		m.clearDegradedLocked("")
		m.markDecisionLocked(now, "retain-active:"+name)
		if account := m.registry.account(name); account != nil {
			if err := syncManagedAccountToResolvedAuth(account.AuthPath, m.codexHome); err != nil {
				return err
			}
		}
		return m.persistStateLocked()
	}
	return m.activateAccountLocked(name, now, reason)
}

func (m *managedAuthManager) evaluateUsageLocked(now time.Time, queueOnly bool) error {
	active := normalizeManagedAuthName(m.state.Active)
	for _, account := range m.registry.orderedAccounts() {
		accountState := m.state.Accounts[account.Name]
		check := fetchManagedAccountUsage(account, m.settings)
		mergeAccountUsageState(&accountState, check, now, m.settings)
		if check.snapshot != nil {
			if check.snapshot.HardLimit {
				accountState.DepletedAt = now.Format(time.RFC3339Nano)
				accountState.LastFailureReason = "usage-api-hard-limit"
			} else if check.snapshot.NearLimit {
				accountState.DepletedAt = now.Format(time.RFC3339Nano)
				accountState.LastFailureReason = "usage-api-near-limit"
			} else if account.Name == active || account.Name == normalizeManagedAuthName(m.registry.Preferred) {
				accountState.DepletedAt = ""
				if strings.HasPrefix(accountState.LastFailureReason, "usage-api-") {
					accountState.LastFailureReason = ""
				}
			}
		}
		m.state.Accounts[account.Name] = accountState
	}

	if queueOnly {
		if err := m.refreshQueuedSwitchesLocked(now); err != nil {
			return err
		}
		return m.persistStateLocked()
	}

	if err := m.refreshQueuedSwitchesLocked(now); err != nil {
		return err
	}
	return m.persistStateLocked()
}

func (m *managedAuthManager) refreshQueuedSwitchesLocked(now time.Time) error {
	active := normalizeManagedAuthName(m.state.Active)
	if active == "" {
		clearPendingAccountSwitch(&m.state)
		m.markDegradedLocked(now, "no-active-account")
		return nil
	}

	if m.activeShouldQueueFallbackLocked(active, now) {
		target := m.chooseFreshFallbackTargetLocked(active, now)
		if target == "" {
			clearPendingAccountSwitch(&m.state)
			m.markDegradedLocked(now, "no-fresh-fallback-target")
			return nil
		}
		if err := m.queuePendingAccountSwitchLocked(target, now, "usage-api-near-limit"); err != nil {
			return err
		}
		m.clearDegradedLocked("queue-fallback:" + target)
		return nil
	}

	preferred := normalizeManagedAuthName(m.registry.Preferred)
	if preferred != "" && preferred != active && m.shouldQueuePreferredReturnLocked(preferred, active, now) {
		if err := m.queuePendingAccountSwitchLocked(preferred, now, "usage-api-preferred-restored"); err != nil {
			return err
		}
		m.clearDegradedLocked("queue-preferred-return:" + preferred)
		return nil
	}

	clearPendingAccountSwitch(&m.state)
	if m.activeUsableLocked(active, now) {
		m.clearDegradedLocked("active-usable:" + active)
		return nil
	}
	m.markDegradedLocked(now, "active-usage-unavailable")
	return nil
}

func (m *managedAuthManager) chooseLaunchAccountLocked(now time.Time) string {
	pending := normalizeManagedAuthName(m.state.PendingActive)
	if pending != "" && m.freshEligibleLocked(pending, now) {
		return pending
	}
	preferred := normalizeManagedAuthName(m.registry.Preferred)
	if preferred != "" && m.freshEligibleLocked(preferred, now) {
		return preferred
	}
	active := normalizeManagedAuthName(m.state.Active)
	if active != "" && m.freshEligibleLocked(active, now) {
		return active
	}
	for _, account := range m.registry.orderedAccounts() {
		if account.Name == preferred || account.Name == active {
			continue
		}
		if m.freshEligibleLocked(account.Name, now) {
			return account.Name
		}
	}
	if active != "" && m.activeUsableLocked(active, now) {
		return active
	}
	if preferred != "" && m.accountEnabledLocked(preferred) {
		return preferred
	}
	return active
}

func (m *managedAuthManager) chooseFreshFallbackTargetLocked(active string, now time.Time) string {
	preferred := normalizeManagedAuthName(m.registry.Preferred)
	if preferred != "" && preferred != active && m.freshEligibleLocked(preferred, now) {
		return preferred
	}
	for _, account := range m.registry.orderedAccounts() {
		if account.Name == active {
			continue
		}
		if m.freshEligibleLocked(account.Name, now) {
			return account.Name
		}
	}
	return ""
}

func (m *managedAuthManager) freshEligibleLocked(name string, now time.Time) bool {
	name = normalizeManagedAuthName(name)
	if name == "" || !m.accountEnabledLocked(name) {
		return false
	}
	state := m.state.Accounts[name]
	return state.LastUsageResult == accountUsageResultOK && !state.LimitReached && !usageThresholdExceeded(state, m.settings) && !lastUsageFreshUntilExpired(state, now)
}

func (m *managedAuthManager) activeUsableLocked(name string, now time.Time) bool {
	name = normalizeManagedAuthName(name)
	if name == "" || !m.accountEnabledLocked(name) {
		return false
	}
	state := m.state.Accounts[name]
	switch state.LastUsageResult {
	case accountUsageResultOK:
		return !state.LimitReached && !usageThresholdExceeded(state, m.settings)
	case accountUsageResultStale:
		return !state.LimitReached
	default:
		return false
	}
}

func (m *managedAuthManager) activeShouldQueueFallbackLocked(active string, now time.Time) bool {
	state := m.state.Accounts[active]
	if state.LastUsageResult != accountUsageResultOK {
		return false
	}
	if !state.LimitReached && !usageThresholdExceeded(state, m.settings) {
		return false
	}
	return m.activeDwellSatisfiedLocked(active, now)
}

func (m *managedAuthManager) shouldQueuePreferredReturnLocked(preferred string, active string, now time.Time) bool {
	if !m.freshEligibleLocked(preferred, now) {
		return false
	}
	return m.preferredReturnDwellSatisfiedLocked(active, now)
}

func (m *managedAuthManager) activeDwellSatisfiedLocked(active string, now time.Time) bool {
	return dwellSatisfied(m.state.Accounts[active].LastActivatedAt, now, m.settings.minActiveDwell)
}

func (m *managedAuthManager) preferredReturnDwellSatisfiedLocked(active string, now time.Time) bool {
	return dwellSatisfied(m.state.Accounts[active].LastActivatedAt, now, m.settings.preferredReturnDwell)
}

func (m *managedAuthManager) accountEnabledLocked(name string) bool {
	account := m.registry.account(normalizeManagedAuthName(name))
	return account != nil && account.Enabled
}

func (m *managedAuthManager) activateAccountLocked(name string, now time.Time, reason string) error {
	account := m.registry.account(name)
	if account == nil {
		return fmt.Errorf("managed account %q not found", name)
	}
	if err := syncManagedAccountToResolvedAuth(account.AuthPath, m.codexHome); err != nil {
		return err
	}
	accountState := m.state.Accounts[name]
	accountState.LastActivatedAt = now.Format(time.RFC3339Nano)
	accountState.DepletedAt = ""
	accountState.LastFailureReason = ""
	m.state.Accounts[name] = accountState
	m.state.Active = name
	clearPendingAccountSwitch(&m.state)
	m.clearDegradedLocked("activate:" + name + ":" + reason)
	return m.persistStateLocked()
}

func (m *managedAuthManager) queuePendingAccountSwitchLocked(name string, now time.Time, reason string) error {
	name = normalizeManagedAuthName(name)
	if name == "" || name == normalizeManagedAuthName(m.state.Active) {
		clearPendingAccountSwitch(&m.state)
		return m.persistStateLocked()
	}
	if !m.accountEnabledLocked(name) {
		return nil
	}
	m.state.PendingActive = name
	m.state.PendingReason = reason
	m.state.PendingSince = now.Format(time.RFC3339Nano)
	m.state.RestartRequired = true
	m.markDecisionLocked(now, reason)
	return m.persistStateLocked()
}

func (m *managedAuthManager) clearDegradedLocked(decision string) {
	m.state.Degraded = false
	m.state.DegradedReason = ""
	if strings.TrimSpace(decision) != "" {
		m.markDecisionLocked(time.Now().UTC(), decision)
	}
}

func (m *managedAuthManager) markDegradedLocked(now time.Time, reason string) {
	m.state.Degraded = true
	m.state.DegradedReason = reason
	m.markDecisionLocked(now, "degraded:"+reason)
}

func (m *managedAuthManager) markDecisionLocked(now time.Time, reason string) {
	m.state.LastDecisionAt = now.Format(time.RFC3339Nano)
	m.state.LastDecisionReason = reason
}

func (m *managedAuthManager) persistStateLocked() error {
	return saveManagedAuthRuntimeState(m.codexHome, m.state)
}

func usageThresholdExceeded(state ManagedAuthAccountState, settings managedAuthSettings) bool {
	if state.FiveHourUsedPct != nil && *state.FiveHourUsedPct >= settings.usageThresholdPct {
		return true
	}
	if state.WeeklyUsedPct != nil && *state.WeeklyUsedPct >= settings.usageThresholdPct {
		return true
	}
	return false
}

func dwellSatisfied(raw string, now time.Time, minimum time.Duration) bool {
	if minimum <= 0 {
		return true
	}
	activatedAt, ok := parseManagedAuthTime(raw)
	if !ok {
		return true
	}
	return !activatedAt.Add(minimum).After(now)
}

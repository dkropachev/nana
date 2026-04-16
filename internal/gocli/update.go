package gocli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dkropachev/nana/internal/version"
)

const (
	updateCheckInterval = 12 * time.Hour
	updateStateFileName = "update-check.json"
	updateManifestURL   = "https://github.com/dkropachev/nana/releases/latest/download/native-release-manifest.json"
)

type updateState struct {
	LastCheckedAt  string `json:"last_checked_at"`
	LastSeenLatest string `json:"last_seen_latest,omitempty"`
}

type latestPackageInfo struct {
	Version string `json:"version"`
}

type updateManifest struct {
	Version string            `json:"version"`
	Assets  []updateAssetInfo `json:"assets"`
}

type updateAssetInfo struct {
	Product     string `json:"product"`
	Platform    string `json:"platform"`
	Arch        string `json:"arch"`
	Libc        string `json:"libc,omitempty"`
	Archive     string `json:"archive"`
	Binary      string `json:"binary"`
	BinaryPath  string `json:"binary_path"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"download_url"`
}

type updateDeps struct {
	now             func() time.Time
	isTTY           func() bool
	isLocalCheckout func() bool
	readState       func(cwd string) (*updateState, error)
	writeState      func(cwd string, state updateState) error
	fetchLatest     func() (string, error)
	currentVersion  func() (string, error)
	askYesNo        func(string) bool
	runGlobalUpdate func() (bool, string)
	runSetupRefresh func() error
	logf            func(string, ...any)
}

func defaultUpdateDeps(repoRoot string, launchCwd string) updateDeps {
	return updateDeps{
		now: time.Now,
		isTTY: func() bool {
			fi, err := os.Stdin.Stat()
			return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
		},
		isLocalCheckout: func() bool {
			if strings.TrimSpace(repoRoot) == "" {
				return false
			}
			if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err == nil {
				return true
			}
			return false
		},
		readState:       readUpdateState,
		writeState:      writeUpdateState,
		fetchLatest:     fetchLatestVersion,
		currentVersion:  currentBinaryVersion,
		askYesNo:        askYesNo,
		runGlobalUpdate: runGlobalUpdate,
		runSetupRefresh: func() error {
			return Setup(repoRoot, launchCwd, []string{"--force"})
		},
		logf: func(format string, args ...any) {
			fmt.Printf(format, args...)
		},
	}
}

func IsNewerVersion(current string, latest string) bool {
	c := parseSemver(current)
	l := parseSemver(latest)
	if c == nil || l == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseSemver(version string) []int {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, part := range parts {
		var parsed int
		if _, err := fmt.Sscanf(part, "%d", &parsed); err != nil {
			return nil
		}
		out[i] = parsed
	}
	return out
}

func shouldCheckForUpdates(now time.Time, state *updateState, interval time.Duration) bool {
	if state == nil || strings.TrimSpace(state.LastCheckedAt) == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, state.LastCheckedAt)
	if err != nil {
		return true
	}
	return now.Sub(last) >= interval
}

func MaybeCheckAndPromptUpdate(repoRoot string, launchCwd string) error {
	return maybeCheckAndPromptUpdateWithDeps(repoRoot, launchCwd, defaultUpdateDeps(repoRoot, launchCwd))
}

func maybeCheckAndPromptUpdateWithDeps(repoRoot string, launchCwd string, deps updateDeps) error {
	if strings.TrimSpace(os.Getenv("NANA_AUTO_UPDATE")) == "0" {
		return nil
	}
	if !deps.isTTY() {
		return nil
	}
	if deps.isLocalCheckout() {
		return nil
	}

	now := deps.now()
	state, _ := deps.readState(launchCwd)
	if !shouldCheckForUpdates(now, state, updateCheckInterval) {
		return nil
	}

	current, currentErr := deps.currentVersion()
	latest, latestErr := deps.fetchLatest()
	if err := deps.writeState(launchCwd, updateState{
		LastCheckedAt: now.UTC().Format(time.RFC3339),
		LastSeenLatest: func() string {
			if latestErr == nil {
				return latest
			}
			if state != nil {
				return state.LastSeenLatest
			}
			return ""
		}(),
	}); err != nil {
		return err
	}
	if currentErr != nil || latestErr != nil || !IsNewerVersion(current, latest) {
		return nil
	}

	if !deps.askYesNo(fmt.Sprintf("[nana] Update available: v%s -> v%s. Update now? [Y/n] ", current, latest)) {
		return nil
	}
	deps.logf("[nana] Running native self-update.\n")
	ok, _ := deps.runGlobalUpdate()
	if ok {
		if err := deps.runSetupRefresh(); err == nil {
			deps.logf("[nana] Updated to v%s. Restart to use new code.\n", latest)
			return nil
		}
	}
	deps.logf("[nana] Update failed. Install the latest release binary manually.\n")
	return nil
}

func updateStatePath(cwd string) string {
	return filepath.Join(cwd, ".nana", "state", updateStateFileName)
}

func readUpdateState(cwd string) (*updateState, error) {
	path := updateStatePath(cwd)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state updateState
	if err := json.Unmarshal(content, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func writeUpdateState(cwd string, state updateState) error {
	stateDir := filepath.Join(cwd, ".nana", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(updateStatePath(cwd), data, 0o644)
}

func fetchLatestVersion() (string, error) {
	client := http.Client{Timeout: 3500 * time.Millisecond}
	resp, err := client.Get(updateManifestURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry returned %s", resp.Status)
	}
	var body latestPackageInfo
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.Version, nil
}

func currentBinaryVersion() (string, error) {
	if strings.TrimSpace(version.Version) != "" && version.Version != "dev" {
		return strings.TrimPrefix(version.Version, "v"), nil
	}
	return version.Read(resolvePackageRoot())
}

func runGlobalUpdate() (bool, string) {
	if err := downloadAndReplaceLatestBinary(); err != nil {
		return false, err.Error()
	}
	return true, ""
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func askYesNo(prompt string) bool {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	trimmed := strings.ToLower(strings.TrimSpace(answer))
	return trimmed == "" || trimmed == "y" || trimmed == "yes"
}

func resolvePackageRoot() string {
	candidates := []string{}
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates, filepath.Dir(exeDir), exeDir)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(candidate, "go.mod")); err == nil {
			return candidate
		}
		if _, err := os.Stat(filepath.Join(candidate, "prompts")); err == nil {
			if _, err := os.Stat(filepath.Join(candidate, "skills")); err == nil {
				return candidate
			}
		}
	}
	return "."
}

func downloadAndReplaceLatestBinary() error {
	client := &http.Client{Timeout: 20 * time.Second}
	manifest, err := fetchUpdateManifest(client, updateManifestURL)
	if err != nil {
		return err
	}
	asset, err := selectCurrentPlatformAsset(manifest)
	if err != nil {
		return err
	}
	executablePath, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err := downloadAndExtractAsset(client, asset)
	if err != nil {
		return err
	}
	return replaceCurrentBinary(executablePath, binary)
}

func fetchUpdateManifest(client *http.Client, url string) (*updateManifest, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("update manifest returned %s", resp.Status)
	}
	var manifest updateManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func selectCurrentPlatformAsset(manifest *updateManifest) (updateAssetInfo, error) {
	platform := runtime.GOOS
	arch := runtimeArchForManifest()
	best := updateAssetInfo{}
	found := false
	for _, asset := range manifest.Assets {
		if asset.Product != "nana" || asset.Platform != platform || asset.Arch != arch {
			continue
		}
		if !found || updateAssetScore(asset) < updateAssetScore(best) {
			best = asset
			found = true
		}
	}
	if !found {
		return updateAssetInfo{}, fmt.Errorf("no native update asset for %s/%s", platform, arch)
	}
	return best, nil
}

func runtimeArchForManifest() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

func updateAssetScore(asset updateAssetInfo) int {
	if runtime.GOOS != "linux" {
		return 0
	}
	switch asset.Libc {
	case "musl":
		return 0
	case "glibc":
		return 1
	default:
		return 2
	}
}

func downloadAndExtractAsset(client *http.Client, asset updateAssetInfo) ([]byte, error) {
	resp, err := client.Get(asset.DownloadURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("asset download returned %s", resp.Status)
	}
	archiveBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(archiveBytes)
	if fmt.Sprintf("%x", sum[:]) != strings.TrimSpace(asset.SHA256) {
		return nil, fmt.Errorf("checksum mismatch for %s", asset.Archive)
	}
	if strings.HasSuffix(asset.Archive, ".zip") {
		return extractZipBinary(archiveBytes, asset.BinaryPath)
	}
	if strings.HasSuffix(asset.Archive, ".tar.gz") {
		return extractTarGzBinary(archiveBytes, asset.BinaryPath)
	}
	return nil, fmt.Errorf("unsupported update archive: %s", asset.Archive)
}

func extractZipBinary(archive []byte, binaryPath string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	target := filepath.ToSlash(binaryPath)
	for _, file := range reader.File {
		if filepath.ToSlash(file.Name) != target && !strings.HasSuffix(filepath.ToSlash(file.Name), "/"+target) {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("binary %s not found in zip archive", binaryPath)
}

func extractTarGzBinary(archive []byte, binaryPath string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	target := filepath.ToSlash(binaryPath)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.ToSlash(header.Name) != target && !strings.HasSuffix(filepath.ToSlash(header.Name), "/"+target) {
			continue
		}
		return io.ReadAll(tr)
	}
	return nil, fmt.Errorf("binary %s not found in tar.gz archive", binaryPath)
}

func replaceCurrentBinary(path string, content []byte) error {
	dir := filepath.Dir(path)
	tempFile, err := os.CreateTemp(dir, "nana-update-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	if _, err := tempFile.Write(content); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0o755); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("native self-update is not supported on Windows yet; download the latest release manually")
	}
	return os.Rename(tempPath, path)
}

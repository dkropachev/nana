package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Yeachan-Heo/nana/internal/version"
)

type tripleMapping struct {
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Libc     string `json:"libc,omitempty"`
}

type planArtifactAsset struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Path string `json:"path"`
	ID   string `json:"id,omitempty"`
}

type planArtifact struct {
	Kind          string              `json:"kind"`
	Name          string              `json:"name"`
	Checksum      string              `json:"checksum"`
	TargetTriples []string            `json:"target_triples"`
	Assets        []planArtifactAsset `json:"assets"`
}

type planRelease struct {
	AppName    string `json:"app_name"`
	AppVersion string `json:"app_version"`
}

type distPlan struct {
	Artifacts       map[string]planArtifact `json:"artifacts"`
	Releases        []planRelease           `json:"releases"`
	AnnouncementTag string                  `json:"announcement_tag"`
}

type supplementalAsset struct {
	Product    string `json:"product"`
	Version    string `json:"version"`
	Target     string `json:"target"`
	Archive    string `json:"archive"`
	Binary     string `json:"binary"`
	BinaryPath string `json:"binary_path"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

type manifestAsset struct {
	Product     string `json:"product"`
	Version     string `json:"version"`
	Platform    string `json:"platform"`
	Arch        string `json:"arch"`
	Target      string `json:"target"`
	Libc        string `json:"libc,omitempty"`
	Archive     string `json:"archive"`
	Binary      string `json:"binary"`
	BinaryPath  string `json:"binary_path"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
}

type releaseManifest struct {
	ManifestVersion int             `json:"manifest_version"`
	Version         string          `json:"version"`
	Tag             string          `json:"tag,omitempty"`
	GeneratedAt     string          `json:"generated_at"`
	Assets          []manifestAsset `json:"assets"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: go run ./cmd/nana-release-helper <check-version-sync|generate-native-release-manifest|verify-native-release-assets> [flags]")
	}
	switch args[0] {
	case "check-version-sync":
		return runCheckVersionSync(args[1:])
	case "generate-native-release-manifest":
		return runGenerateNativeReleaseManifest(args[1:])
	case "verify-native-release-assets":
		return runVerifyNativeReleaseAssets(args[1:])
	default:
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

func runCheckVersionSync(args []string) error {
	tag := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--tag" && i+1 < len(args) {
			tag = strings.TrimSpace(args[i+1])
			i++
		}
	}

	root := "."
	currentVersion, err := version.Read(root)
	if err != nil {
		return err
	}
	problems := []string{}
	for _, stale := range []string{
		"Cargo.toml",
		filepath.Join("crates", "nana-runtime-core"),
		filepath.Join("crates", "nana-mux"),
	} {
		if _, err := os.Stat(filepath.Join(root, stale)); err == nil {
			problems = append(problems, fmt.Sprintf("%s should be removed after the Go migration", stale))
		}
	}
	if tag != "" && tag != "v"+currentVersion {
		problems = append(problems, fmt.Sprintf("release tag (%s) does not match VERSION (v%s)", tag, currentVersion))
	}
	if len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintf(os.Stderr, "[version-sync] %s\n", problem)
		}
		return errors.New("version sync failed")
	}
	fmt.Printf("[version-sync] OK version=%s", currentVersion)
	if tag != "" {
		fmt.Printf(" tag=%s", tag)
	}
	fmt.Println()
	return nil
}

func runGenerateNativeReleaseManifest(args []string) error {
	planPath := ""
	artifactsDir := ""
	outPath := ""
	releaseBaseURL := ""
	requiredProducts := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--plan":
			i++
			planPath = args[i]
		case "--artifacts-dir":
			i++
			artifactsDir = args[i]
		case "--out":
			i++
			outPath = args[i]
		case "--release-base-url":
			i++
			releaseBaseURL = args[i]
		case "--require-products":
			i++
			for _, p := range strings.Split(args[i], ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					requiredProducts = append(requiredProducts, p)
				}
			}
		}
	}
	if artifactsDir == "" || outPath == "" || releaseBaseURL == "" {
		return errors.New("usage: go run ./cmd/nana-release-helper generate-native-release-manifest [--plan <path>] --artifacts-dir <dir> --out <path> --release-base-url <url> [--require-products a,b]")
	}

	files, err := walkFiles(artifactsDir)
	if err != nil {
		return err
	}
	byName := map[string]string{}
	for _, file := range files {
		byName[filepath.Base(file)] = file
	}

	assets := []manifestAsset{}
	if planPath != "" {
		var plan distPlan
		raw, err := os.ReadFile(planPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &plan); err != nil {
			return err
		}
		for _, artifact := range plan.Artifacts {
			if artifact.Kind != "executable-zip" || len(artifact.TargetTriples) == 0 {
				continue
			}
			mapped, ok := mapTriple(artifact.TargetTriples[0])
			if !ok {
				continue
			}
			var execAsset *planArtifactAsset
			for i := range artifact.Assets {
				if artifact.Assets[i].Kind == "executable" {
					execAsset = &artifact.Assets[i]
					break
				}
			}
			if execAsset == nil {
				continue
			}
			archivePath := byName[artifact.Name]
			checksumPath := byName[artifact.Checksum]
			if archivePath == "" || checksumPath == "" {
				return fmt.Errorf("missing artifact files for %s", artifact.Name)
			}
			version := strings.TrimPrefix(plan.AnnouncementTag, "v")
			for _, release := range plan.Releases {
				if release.AppName == execAsset.Name || release.AppName == strings.Split(execAsset.ID, "-exe-")[0] {
					version = release.AppVersion
					break
				}
			}
			size, err := fileSize(archivePath)
			if err != nil {
				return err
			}
			assets = append(assets, manifestAsset{
				Product:     execAsset.Name,
				Version:     version,
				Platform:    mapped.Platform,
				Arch:        mapped.Arch,
				Target:      artifact.TargetTriples[0],
				Libc:        mapped.Libc,
				Archive:     artifact.Name,
				Binary:      execAsset.Name,
				BinaryPath:  execAsset.Path,
				SHA256:      parseChecksumFile(checksumPath),
				Size:        size,
				DownloadURL: strings.TrimRight(releaseBaseURL, "/") + "/" + artifact.Name,
			})
		}
	}

	for _, file := range files {
		if !strings.HasSuffix(file, ".metadata.json") {
			continue
		}
		var meta supplementalAsset
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return err
		}
		mapped, ok := mapTriple(meta.Target)
		if !ok {
			continue
		}
		assets = append(assets, manifestAsset{
			Product:     meta.Product,
			Version:     meta.Version,
			Platform:    mapped.Platform,
			Arch:        mapped.Arch,
			Target:      meta.Target,
			Libc:        mapped.Libc,
			Archive:     meta.Archive,
			Binary:      meta.Binary,
			BinaryPath:  meta.BinaryPath,
			SHA256:      meta.SHA256,
			Size:        meta.Size,
			DownloadURL: strings.TrimRight(releaseBaseURL, "/") + "/" + meta.Archive,
		})
	}

	sort.Slice(assets, func(i, j int) bool {
		left := fmt.Sprintf("%s-%s-%s", assets[i].Product, assets[i].Platform, assets[i].Arch)
		right := fmt.Sprintf("%s-%s-%s", assets[j].Product, assets[j].Platform, assets[j].Arch)
		if left != right {
			return left < right
		}
		libcOrder := map[string]int{"musl": 0, "glibc": 1}
		li := libcOrder[assets[i].Libc]
		ri := libcOrder[assets[j].Libc]
		if li != ri {
			return li < ri
		}
		return assets[i].Archive < assets[j].Archive
	})

	manifest := releaseManifest{
		ManifestVersion: 1,
		GeneratedAt:     nowISO(),
		Assets:          assets,
	}
	if len(assets) > 0 {
		manifest.Version = assets[0].Version
		manifest.Tag = "v" + assets[0].Version
	}
	for _, product := range requiredProducts {
		found := false
		for _, asset := range assets {
			if asset.Product == product {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("missing required product in release manifest: %s", product)
		}
	}

	if err := writePrettyJSON(outPath, manifest); err != nil {
		return err
	}
	fmt.Println(filepath.Clean(outPath))
	return nil
}

func runVerifyNativeReleaseAssets(args []string) error {
	manifestPath := ""
	artifactsDir := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--manifest":
			i++
			manifestPath = args[i]
		case "--artifacts-dir":
			i++
			artifactsDir = args[i]
		}
	}
	if manifestPath == "" || artifactsDir == "" {
		return errors.New("usage: go run ./cmd/nana-release-helper verify-native-release-assets --manifest <path> --artifacts-dir <dir>")
	}
	var manifest releaseManifest
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	files, err := walkFiles(artifactsDir)
	if err != nil {
		return err
	}
	byName := map[string]string{}
	for _, file := range files {
		byName[filepath.Base(file)] = file
	}
	for _, asset := range manifest.Assets {
		archivePath := byName[asset.Archive]
		if archivePath == "" {
			return fmt.Errorf("missing archive %s", asset.Archive)
		}
		size, err := fileSize(archivePath)
		if err != nil {
			return err
		}
		if asset.Size > 0 && size != asset.Size {
			return fmt.Errorf("size mismatch for %s", asset.Archive)
		}
		sum, err := sha256File(archivePath)
		if err != nil {
			return err
		}
		if sum != asset.SHA256 {
			return fmt.Errorf("checksum mismatch for %s", asset.Archive)
		}
		members, err := archiveMembers(archivePath)
		if err != nil {
			return err
		}
		if !archiveContainsBinary(members, asset.BinaryPath) {
			return fmt.Errorf("archive %s is missing %s", asset.Archive, asset.BinaryPath)
		}
	}
	fmt.Printf("[native-release-assets] verified %d assets\n", len(manifest.Assets))
	return nil
}

func walkFiles(root string) ([]string, error) {
	files := []string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func parseChecksumFile(path string) string {
	raw, _ := os.ReadFile(path)
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func mapTriple(triple string) (tripleMapping, bool) {
	switch triple {
	case "x86_64-unknown-linux-gnu":
		return tripleMapping{Platform: "linux", Arch: "x64", Libc: "glibc"}, true
	case "aarch64-unknown-linux-gnu":
		return tripleMapping{Platform: "linux", Arch: "arm64", Libc: "glibc"}, true
	case "x86_64-unknown-linux-musl":
		return tripleMapping{Platform: "linux", Arch: "x64", Libc: "musl"}, true
	case "aarch64-unknown-linux-musl":
		return tripleMapping{Platform: "linux", Arch: "arm64", Libc: "musl"}, true
	case "x86_64-apple-darwin":
		return tripleMapping{Platform: "darwin", Arch: "x64"}, true
	case "aarch64-apple-darwin":
		return tripleMapping{Platform: "darwin", Arch: "arm64"}, true
	case "x86_64-pc-windows-msvc":
		return tripleMapping{Platform: "win32", Arch: "x64"}, true
	case "aarch64-pc-windows-msvc":
		return tripleMapping{Platform: "win32", Arch: "arm64"}, true
	default:
		return tripleMapping{}, false
	}
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func archiveMembers(path string) ([]string, error) {
	if strings.HasSuffix(path, ".zip") {
		reader, err := zip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		out := make([]string, 0, len(reader.File))
		for _, file := range reader.File {
			out = append(out, file.Name)
		}
		return out, nil
	}
	if strings.HasSuffix(path, ".tar.gz") {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		gz, err := gzip.NewReader(file)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		out := []string{}
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			out = append(out, hdr.Name)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported archive format: %s", path)
}

func archiveContainsBinary(members []string, binaryPath string) bool {
	binaryPath = strings.ReplaceAll(binaryPath, "\\", "/")
	for _, member := range members {
		member = strings.ReplaceAll(member, "\\", "/")
		if member == binaryPath || strings.HasSuffix(member, "/"+binaryPath) {
			return true
		}
	}
	return false
}

func writePrettyJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

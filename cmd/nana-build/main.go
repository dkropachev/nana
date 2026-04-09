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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Yeachan-Heo/nana/internal/version"
)

type buildTarget struct {
	Pkg string
	Out string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: go run ./cmd/nana-build <build-go-cli|build-go-release-asset|stage-explore-harness|stage-sparkshell|test-sparkshell> [flags]")
	}
	switch args[0] {
	case "build-go-cli":
		return runBuildGoCLI()
	case "build-go-release-asset":
		return runBuildGoReleaseAsset(args[1:])
	case "stage-explore-harness":
		return runStageExploreHarness()
	case "stage-sparkshell":
		return runStageSparkShell(args[1:])
	case "test-sparkshell":
		return runTestSparkShell(args[1:])
	default:
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

func runBuildGoCLI() error {
	root := "."
	version, err := version.Read(root)
	if err != nil {
		return err
	}
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	targets := []buildTarget{
		{Pkg: "./cmd/nana", Out: filepath.Join(root, "bin", "nana"+exe)},
		{Pkg: "./cmd/nana-runtime", Out: filepath.Join(root, "bin", "go", "nana-runtime"+exe)},
		{Pkg: "./cmd/nana-explore", Out: filepath.Join(root, "bin", "go", "nana-explore-harness"+exe)},
		{Pkg: "./cmd/nana-sparkshell", Out: filepath.Join(root, "bin", "go", "nana-sparkshell"+exe)},
		{Pkg: "./cmd/nana-release-helper", Out: filepath.Join(root, "bin", "go", "nana-release-helper"+exe)},
	}
	for _, target := range targets {
		if err := os.MkdirAll(filepath.Dir(target.Out), 0o755); err != nil {
			return err
		}
		if err := goBuild(root, target.Pkg, target.Out, version, "", ""); err != nil {
			return fmt.Errorf("[build-go-cli] %w", err)
		}
	}
	return nil
}

func runBuildGoReleaseAsset(args []string) error {
	target := ""
	outDir := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--target":
			i++
			target = args[i]
		case "--out-dir":
			i++
			outDir = args[i]
		}
	}
	if target == "" || outDir == "" {
		return errors.New("usage: go run ./cmd/nana-build build-go-release-asset --target <triple> --out-dir <dir>")
	}
	goos, goarch, ext, err := mapTriple(target)
	if err != nil {
		return err
	}
	root := "."
	version, err := version.Read(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	stagingRoot, err := os.MkdirTemp("", "nana-go-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stagingRoot)
	stagingDir := filepath.Join(stagingRoot, "nana-"+target)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return err
	}
	binaries := []buildTarget{
		{Pkg: "./cmd/nana", Out: filepath.Join(stagingDir, "nana"+ext)},
		{Pkg: "./cmd/nana-runtime", Out: filepath.Join(stagingDir, "nana-runtime"+ext)},
		{Pkg: "./cmd/nana-explore", Out: filepath.Join(stagingDir, "nana-explore-harness"+ext)},
		{Pkg: "./cmd/nana-sparkshell", Out: filepath.Join(stagingDir, "nana-sparkshell"+ext)},
	}
	for _, binary := range binaries {
		if err := goBuild(root, binary.Pkg, binary.Out, version, goos, goarch); err != nil {
			return err
		}
	}
	archiveName := fmt.Sprintf("nana-%s", target)
	if goos == "windows" {
		archiveName += ".zip"
	} else {
		archiveName += ".tar.gz"
	}
	archivePath := filepath.Join(outDir, archiveName)
	if goos == "windows" {
		if err := writeZip(archivePath, stagingDir, binaries); err != nil {
			return err
		}
	} else {
		if err := writeTarGz(archivePath, stagingDir, binaries); err != nil {
			return err
		}
	}
	sum, err := sha256File(archivePath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, archiveName+".sha256"), []byte(sum+"\n"), 0o644); err != nil {
		return err
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return err
	}
	meta := map[string]any{
		"product":     "nana",
		"version":     version,
		"target":      target,
		"archive":     archiveName,
		"binary":      "nana" + ext,
		"binary_path": "nana" + ext,
		"sha256":      sum,
		"size":        info.Size(),
	}
	metaPath := filepath.Join(outDir, fmt.Sprintf("nana-%s.metadata.json", target))
	data, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(metaPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println(archivePath)
	return nil
}

func runStageExploreHarness() error {
	root := "."
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	binaryName := "nana-explore-harness" + exe
	stagingDir := filepath.Join(root, "target", "go-explore-release")
	sourcePath := filepath.Join(stagingDir, binaryName)
	outputPath := filepath.Join(root, "bin", binaryName)
	metadataPath := filepath.Join(root, "bin", "nana-explore-harness.meta.json")
	version, err := version.Read(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return err
	}
	if err := goBuild(root, "./cmd/nana-explore", sourcePath, version, "", ""); err != nil {
		return err
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return fmt.Errorf("[build-explore-harness] expected built binary at %s", sourcePath)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, content, 0o755); err != nil {
		return err
	}
	meta := map[string]any{
		"binaryName": binaryName,
		"platform":   runtime.GOOS,
		"arch":       runtime.GOARCH,
		"builtAt":    nowISO(),
		"strategy":   "prepack-go-native",
	}
	if err := writePrettyJSON(metadataPath, meta); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[build-explore-harness] wrote %s\n", outputPath)
	return nil
}

func runStageSparkShell(extraArgs []string) error {
	root := "."
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	binaryName := "nana-sparkshell" + exe
	buildDir := os.Getenv("NANA_SPARKSHELL_BUILD_DIR")
	if strings.TrimSpace(buildDir) == "" {
		buildDir = filepath.Join(root, "target", "go-sparkshell-release")
	}
	releaseBinaryPath := filepath.Join(buildDir, binaryName)
	stageRoot := os.Getenv("NANA_SPARKSHELL_STAGE_DIR")
	if strings.TrimSpace(stageRoot) == "" {
		stageRoot = filepath.Join(root, "bin", "native")
	}
	packagedBinaryDir := filepath.Join(stageRoot, runtime.GOOS+"-"+runtime.GOARCH)
	packagedBinaryPath := filepath.Join(packagedBinaryDir, binaryName)
	version, err := version.Read(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}
	if err := goBuild(root, "./cmd/nana-sparkshell", releaseBinaryPath, version, "", ""); err != nil {
		return err
	}
	if _, err := os.Stat(releaseBinaryPath); err != nil {
		return fmt.Errorf("nana sparkshell build: expected release binary at %s", releaseBinaryPath)
	}
	if err := os.MkdirAll(packagedBinaryDir, 0o755); err != nil {
		return err
	}
	content, err := os.ReadFile(releaseBinaryPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(packagedBinaryPath, content, 0o755); err != nil {
		return err
	}
	fmt.Printf("nana sparkshell build: staged native binary at %s\n", packagedBinaryPath)
	_ = extraArgs
	return nil
}

func runTestSparkShell(extraArgs []string) error {
	root := "."
	if _, err := os.Stat(filepath.Join(root, "cmd", "nana-sparkshell", "main.go")); err != nil {
		return fmt.Errorf("nana sparkshell test: missing Go command package at %s", filepath.Join(root, "cmd", "nana-sparkshell", "main.go"))
	}
	args := append([]string{"test", "./cmd/nana-sparkshell"}, extraArgs...)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nana sparkshell test: failed to launch go test: %w", err)
	}
	return nil
}

func goBuild(root, pkg, out, version, goos, goarch string) error {
	args := []string{"build", "-ldflags", fmt.Sprintf("-X github.com/Yeachan-Heo/nana/internal/version.Version=%s", version), "-o", out, pkg}
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if goos != "" || goarch != "" {
		cmd.Env = append([]string{}, os.Environ()...)
		if goos != "" {
			cmd.Env = append(cmd.Env, "GOOS="+goos)
		}
		if goarch != "" {
			cmd.Env = append(cmd.Env, "GOARCH="+goarch)
		}
		cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build failed for %s: %w", pkg, err)
	}
	return nil
}

func mapTriple(target string) (goos, goarch, ext string, err error) {
	switch target {
	case "x86_64-unknown-linux-gnu", "x86_64-unknown-linux-musl":
		return "linux", "amd64", "", nil
	case "aarch64-unknown-linux-gnu", "aarch64-unknown-linux-musl":
		return "linux", "arm64", "", nil
	case "x86_64-apple-darwin":
		return "darwin", "amd64", "", nil
	case "aarch64-apple-darwin":
		return "darwin", "arm64", "", nil
	case "x86_64-pc-windows-msvc":
		return "windows", "amd64", ".exe", nil
	default:
		return "", "", "", fmt.Errorf("unsupported go release target: %s", target)
	}
}

func writeZip(path, stagingDir string, binaries []buildTarget) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	zw := zip.NewWriter(file)
	defer zw.Close()
	for _, binary := range binaries {
		name := filepath.Base(binary.Out)
		writer, err := zw.Create(name)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(filepath.Join(stagingDir, name))
		if err != nil {
			return err
		}
		if _, err := writer.Write(content); err != nil {
			return err
		}
	}
	return nil
}

func writeTarGz(path, stagingDir string, binaries []buildTarget) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for _, binary := range binaries {
		name := filepath.Base(binary.Out)
		full := filepath.Join(stagingDir, name)
		info, err := os.Stat(full)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		if _, err := tw.Write(content); err != nil {
			return err
		}
	}
	return nil
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

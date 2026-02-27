package marketplace

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cloudbro-kube-ai/k13d/pkg/config"
)

// GetMCPServerBinDir returns the directory where downloaded MCP server binaries are stored.
// Creates the directory if it does not exist.
func GetMCPServerBinDir() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("getting config directory: %w", err)
	}
	binDir := filepath.Join(configDir, "mcp-binaries")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("creating mcp-binaries directory: %w", err)
	}
	return binDir, nil
}

// BuildDownloadURL replaces platform placeholders in a URL template with
// values derived from the current runtime. Supported placeholders:
//
//   - {os}        Go-style lowercase: darwin, linux, windows
//   - {arch}      Go-style: amd64, arm64
//   - {OS}        Title-case: Darwin, Linux, Windows
//   - {ARCH}      Release-style: x86_64, arm64
//   - {rust_os}   Rust target triple OS: apple-darwin, unknown-linux-gnu, pc-windows-msvc
//   - {rust_arch} Rust target triple arch: x86_64, aarch64
func BuildDownloadURL(urlPattern string) string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// {os} / {arch} — Go defaults
	url := strings.ReplaceAll(urlPattern, "{os}", goos)
	url = strings.ReplaceAll(url, "{arch}", goarch)

	// {OS} — title-case (Darwin, Linux, Windows)
	titleOS := strings.ToUpper(goos[:1]) + goos[1:]
	url = strings.ReplaceAll(url, "{OS}", titleOS)

	// {ARCH} — release-style (amd64 → x86_64, arm64 stays arm64)
	releaseArch := goarch
	if goarch == "amd64" {
		releaseArch = "x86_64"
	}
	url = strings.ReplaceAll(url, "{ARCH}", releaseArch)

	// {rust_os} — Rust target triple OS suffix
	rustOS := map[string]string{
		"darwin":  "apple-darwin",
		"linux":   "unknown-linux-gnu",
		"windows": "pc-windows-msvc",
	}[goos]
	if rustOS == "" {
		rustOS = goos
	}
	url = strings.ReplaceAll(url, "{rust_os}", rustOS)

	// {rust_arch} — Rust target triple arch
	rustArch := map[string]string{
		"amd64": "x86_64",
		"arm64": "aarch64",
		"386":   "i686",
	}[goarch]
	if rustArch == "" {
		rustArch = goarch
	}
	url = strings.ReplaceAll(url, "{rust_arch}", rustArch)

	if goos == "windows" && !strings.HasSuffix(url, ".exe") {
		url += ".exe"
	}

	return url
}

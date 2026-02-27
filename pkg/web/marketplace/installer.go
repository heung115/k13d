package marketplace

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cloudbro-kube-ai/k13d/pkg/config"
)

// MCPAgent is the interface the installer uses to register installed servers.
// Implemented by web.Server so the marketplace package stays decoupled.
type MCPAgent interface {
	AddMCPServer(ctx context.Context, server config.MCPServer) error
	IsMCPServerInstalled(name string) bool
}

// isConnectWarning checks whether err is a non-fatal connection warning
// (server was saved but the initial connection failed).
func isConnectWarning(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "server registered but connection failed:")
}

// Installer handles downloading, extracting, and registering MCP servers
type Installer struct {
	agent MCPAgent
}

// NewInstaller creates an installer bound to the given agent
func NewInstaller(agent MCPAgent) *Installer {
	return &Installer{agent: agent}
}

// RunInstallation orchestrates the full installation lifecycle
func (i *Installer) RunInstallation(ctx context.Context, job *InstallJob, item *MarketplaceItem, method string) error {
	defer func() {
		if r := recover(); r != nil {
			job.SetError(fmt.Sprintf("installation panic: %v", r))
		}
	}()

	job.AddLog(fmt.Sprintf("Starting installation of %s...", item.Name))
	job.SetProgress(5)

	if method == "" {
		if item.Install.Binary != nil {
			method = "binary"
		} else if item.Install.Archive != nil {
			method = "archive"
		} else {
			job.SetError("no install method available")
			return fmt.Errorf("no install method available for %s", item.ID)
		}
	}

	var binPath string
	var err error

	switch method {
	case "binary":
		binPath, err = i.installBinary(ctx, job, item)
	case "archive":
		binPath, err = i.installArchive(ctx, job, item)
	default:
		job.SetError(fmt.Sprintf("unsupported install method: %s", method))
		return fmt.Errorf("unsupported install method: %s", method)
	}

	if err != nil {
		job.SetError(err.Error())
		return err
	}

	job.SetProgress(85)

	// Verify the downloaded binary
	job.AddLog("Verifying installation...")
	fileInfo, err := os.Stat(binPath)
	if err != nil {
		job.SetError(fmt.Sprintf("binary verification failed: %v", err))
		return fmt.Errorf("binary not found after install: %w", err)
	}
	sizeMB := float64(fileInfo.Size()) / (1024 * 1024)
	job.AddLog(fmt.Sprintf("✓ Binary verified: %s (%.2f MB)", binPath, sizeMB))

	// Register with the MCP system
	job.AddLog("Adding MCP server to configuration...")
	job.SetProgress(90)

	serverCfg := config.MCPServer{
		Name:        item.Config.ServerName,
		Command:     binPath,
		Args:        item.Config.Args,
		Env:         item.Config.Env,
		Description: item.Description,
		Enabled:     true,
	}

	if err := i.agent.AddMCPServer(ctx, serverCfg); err != nil {
		if isConnectWarning(err) {
			job.AddLog(fmt.Sprintf("⚠ Server registered but initial connection failed: %v", err))
			job.AddLog("  Configure required environment variables (e.g. API tokens) and reconnect from the MCP Servers section.")
		} else {
			job.SetError(fmt.Sprintf("failed to register MCP server: %v", err))
			return err
		}
	} else {
		job.AddLog(fmt.Sprintf("✓ MCP server connected: %s", item.Config.ServerName))
	}

	job.AddLog(fmt.Sprintf("✓ MCP server registered: %s", item.Config.ServerName))
	job.SetProgress(100)
	job.AddLog("✓ Installation completed successfully!")
	job.AddLog(fmt.Sprintf("  → Binary: %s", binPath))
	job.AddLog(fmt.Sprintf("  → Server: %s", item.Config.ServerName))
	job.AddLog(fmt.Sprintf("  → Version: %s", item.Version))
	job.SetStatus("completed")

	return nil
}

// installBinary downloads a binary directly from a GitHub Releases URL
func (i *Installer) installBinary(ctx context.Context, job *InstallJob, item *MarketplaceItem) (string, error) {
	if item.Install.Binary == nil || item.Install.Binary.URL == "" {
		return "", fmt.Errorf("binary URL not specified")
	}

	url := BuildDownloadURL(item.Install.Binary.URL)
	job.AddLog(fmt.Sprintf("Downloading from %s...", url))
	job.SetProgress(10)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	client := &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	contentLength := resp.ContentLength
	job.AddLog(fmt.Sprintf("Download size: %d bytes", contentLength))
	job.SetProgress(15)

	binDir, err := GetMCPServerBinDir()
	if err != nil {
		return "", fmt.Errorf("getting binary directory: %w", err)
	}

	binName := item.Config.ServerName + "-" + item.Version
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)

	job.AddLog(fmt.Sprintf("Saving to %s...", binPath))
	job.SetProgress(20)

	out, err := os.Create(binPath)
	if err != nil {
		return "", fmt.Errorf("creating binary file: %w", err)
	}
	defer out.Close()

	var downloaded int64
	buf := make([]byte, 32*1024)

	for {
		if ctx.Err() != nil {
			os.Remove(binPath)
			return "", fmt.Errorf("download cancelled: %w", ctx.Err())
		}

		nr, readErr := resp.Body.Read(buf)
		if nr > 0 {
			nw, writeErr := out.Write(buf[:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if writeErr == nil {
					writeErr = errors.New("invalid write result")
				}
			}
			downloaded += int64(nw)
			if writeErr != nil {
				os.Remove(binPath)
				return "", fmt.Errorf("writing binary: %w", writeErr)
			}

			if contentLength > 0 {
				progress := 20 + int(float64(downloaded)/float64(contentLength)*50)
				job.SetProgress(progress)
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				os.Remove(binPath)
				return "", fmt.Errorf("reading response: %w", readErr)
			}
			break
		}
	}

	job.SetProgress(70)
	job.AddLog(fmt.Sprintf("Downloaded %d bytes", downloaded))

	if runtime.GOOS != "windows" {
		if err := os.Chmod(binPath, 0755); err != nil {
			os.Remove(binPath)
			return "", fmt.Errorf("making binary executable: %w", err)
		}
		job.AddLog("Made binary executable")
	}

	job.SetProgress(80)
	job.AddLog("Binary download completed")
	return binPath, nil
}

// installArchive downloads a tar.gz or zip archive and extracts the target binary
func (i *Installer) installArchive(ctx context.Context, job *InstallJob, item *MarketplaceItem) (string, error) {
	if item.Install.Archive == nil || item.Install.Archive.URL == "" {
		return "", fmt.Errorf("archive URL not specified")
	}

	url := BuildDownloadURL(item.Install.Archive.URL)
	job.AddLog(fmt.Sprintf("Downloading archive from %s...", url))
	job.SetProgress(10)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	job.SetProgress(40)

	tmpFile, err := os.CreateTemp("", "mcp-archive-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return "", fmt.Errorf("saving archive: %w", err)
	}
	if _, err := tmpFile.Seek(0, 0); err != nil {
		return "", fmt.Errorf("seeking temp file: %w", err)
	}

	job.SetProgress(55)
	job.AddLog(fmt.Sprintf("Extracting %s archive...", item.Install.Archive.Type))

	binDir, err := GetMCPServerBinDir()
	if err != nil {
		return "", fmt.Errorf("getting binary directory: %w", err)
	}

	var binPath string
	switch item.Install.Archive.Type {
	case "tar.gz":
		binPath, err = extractTarGz(ctx, job, tmpFile, binDir, item)
	case "zip":
		binPath, err = extractZip(ctx, job, tmpFile, binDir, item)
	default:
		return "", fmt.Errorf("unsupported archive type: %s", item.Install.Archive.Type)
	}

	if err != nil {
		return "", fmt.Errorf("extracting archive: %w", err)
	}

	job.SetProgress(80)
	job.AddLog("Archive extraction completed")
	return binPath, nil
}

func extractTarGz(ctx context.Context, job *InstallJob, archive *os.File, destDir string, item *MarketplaceItem) (string, error) {
	gzr, err := gzip.NewReader(archive)
	if err != nil {
		return "", fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	extractPath := item.Install.Archive.ExtractPath
	if extractPath == "" {
		extractPath = item.Config.ServerName
	}

	binName := item.Config.ServerName + "-" + item.Version
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(destDir, binName)

	for {
		if ctx.Err() != nil {
			return "", fmt.Errorf("extraction cancelled: %w", ctx.Err())
		}

		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading tar header: %w", err)
		}

		if header.Typeflag == tar.TypeReg {
			if strings.HasSuffix(header.Name, extractPath) || filepath.Base(header.Name) == filepath.Base(extractPath) {
				out, err := os.Create(binPath)
				if err != nil {
					return "", fmt.Errorf("creating binary file: %w", err)
				}
				if _, err := io.Copy(out, tr); err != nil {
					out.Close()
					os.Remove(binPath)
					return "", fmt.Errorf("extracting binary: %w", err)
				}
				out.Close()

				if runtime.GOOS != "windows" {
					if err := os.Chmod(binPath, 0755); err != nil {
						os.Remove(binPath)
						return "", fmt.Errorf("making binary executable: %w", err)
					}
				}
				job.AddLog(fmt.Sprintf("Extracted %s", header.Name))
				return binPath, nil
			}
		}
	}

	return "", fmt.Errorf("binary not found in archive: %s", extractPath)
}

func extractZip(ctx context.Context, job *InstallJob, archive *os.File, destDir string, item *MarketplaceItem) (string, error) {
	stat, err := archive.Stat()
	if err != nil {
		return "", fmt.Errorf("getting archive stat: %w", err)
	}

	rd, err := zip.NewReader(archive, stat.Size())
	if err != nil {
		return "", fmt.Errorf("creating zip reader: %w", err)
	}

	extractPath := item.Install.Archive.ExtractPath
	if extractPath == "" {
		extractPath = item.Config.ServerName
	}

	binName := item.Config.ServerName + "-" + item.Version
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(destDir, binName)

	for _, f := range rd.File {
		if ctx.Err() != nil {
			return "", fmt.Errorf("extraction cancelled: %w", ctx.Err())
		}

		if strings.HasSuffix(f.Name, extractPath) || filepath.Base(f.Name) == filepath.Base(extractPath) {
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("opening zip entry: %w", err)
			}

			out, err := os.Create(binPath)
			if err != nil {
				rc.Close()
				return "", fmt.Errorf("creating binary file: %w", err)
			}

			if _, err := io.Copy(out, rc); err != nil {
				rc.Close()
				out.Close()
				os.Remove(binPath)
				return "", fmt.Errorf("extracting binary: %w", err)
			}
			rc.Close()
			out.Close()

			if runtime.GOOS != "windows" {
				if err := os.Chmod(binPath, 0755); err != nil {
					os.Remove(binPath)
					return "", fmt.Errorf("making binary executable: %w", err)
				}
			}
			job.AddLog(fmt.Sprintf("Extracted %s", f.Name))
			return binPath, nil
		}
	}

	return "", fmt.Errorf("binary not found in archive: %s", extractPath)
}

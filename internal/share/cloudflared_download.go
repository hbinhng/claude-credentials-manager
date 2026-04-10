package share

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// pinnedCloudflaredVersion is the cloudflared release we download if the
// user doesn't already have one. Bumped manually when we want to pick up
// newer versions; TLS integrity from github.com is our only provenance
// check (Cloudflare does not publish SHA256 sums alongside their binaries).
const pinnedCloudflaredVersion = "2026.3.0"

// cloudflaredDownloadTimeout caps the HTTP download.
const cloudflaredDownloadTimeout = 5 * time.Minute

// EnsureCloudflared returns the path to a cloudflared binary that the
// caller can exec. Resolution order:
//
//  1. ~/.ccm/bin/cloudflared (our managed copy)
//  2. `cloudflared` on PATH
//  3. download the pinned version to ~/.ccm/bin/cloudflared
//
// On unsupported platforms (currently win32-arm64) the function returns
// a clear error explaining the situation.
func EnsureCloudflared() (string, error) {
	managed := managedCloudflaredPath()
	if _, err := os.Stat(managed); err == nil {
		return managed, nil
	}

	if path, err := exec.LookPath("cloudflared"); err == nil {
		return path, nil
	}

	asset, err := cloudflaredAssetName()
	if err != nil {
		return "", err
	}

	if err := downloadCloudflared(asset, managed); err != nil {
		return "", err
	}
	return managed, nil
}

// managedCloudflaredPath returns ~/.ccm/bin/cloudflared[.exe].
func managedCloudflaredPath() string {
	home, _ := os.UserHomeDir()
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name = "cloudflared.exe"
	}
	return filepath.Join(home, ".ccm", "bin", name)
}

// cloudflaredAssetName returns the release-asset filename for the current
// GOOS/GOARCH, or an error if Cloudflare does not publish one.
func cloudflaredAssetName() (string, error) {
	switch runtime.GOOS {
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return "cloudflared-linux-amd64", nil
		case "arm64":
			return "cloudflared-linux-arm64", nil
		}
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			return "cloudflared-darwin-amd64.tgz", nil
		case "arm64":
			return "cloudflared-darwin-arm64.tgz", nil
		}
	case "windows":
		switch runtime.GOARCH {
		case "amd64":
			return "cloudflared-windows-amd64.exe", nil
		case "arm64":
			// Cloudflare does not publish a windows-arm64 build.
			return "", errors.New("cloudflared does not publish a windows/arm64 build — install it manually or switch host")
		}
	}
	return "", fmt.Errorf("unsupported platform %s/%s for cloudflared auto-download", runtime.GOOS, runtime.GOARCH)
}

// downloadCloudflared fetches the named asset from the pinned release and
// installs it at dest.
func downloadCloudflared(asset, dest string) error {
	url := fmt.Sprintf("https://github.com/cloudflare/cloudflared/releases/download/%s/%s", pinnedCloudflaredVersion, asset)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create ~/.ccm/bin: %w", err)
	}

	fmt.Fprintf(os.Stderr, "ccm share: downloading cloudflared %s (first run only)…\n", pinnedCloudflaredVersion)

	client := &http.Client{Timeout: cloudflaredDownloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	tmp := dest + ".part"
	// Remove any leftover from a prior crash.
	_ = os.Remove(tmp)

	if filepath.Ext(asset) == ".tgz" {
		if err := extractTgzBinary(resp.Body, "cloudflared", tmp); err != nil {
			return err
		}
	} else {
		if err := writeExecutable(resp.Body, tmp); err != nil {
			return err
		}
	}

	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("install cloudflared: %w", err)
	}
	return nil
}

// writeExecutable streams r to path and marks it executable.
func writeExecutable(r io.Reader, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// extractTgzBinary scans a gzipped tarball for a file whose basename is
// `wantedName` and writes it to destPath as an executable.
func extractTgzBinary(r io.Reader, wantedName, destPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) != wantedName {
			continue
		}
		return writeExecutable(tr, destPath)
	}
	return fmt.Errorf("tarball did not contain %q", wantedName)
}

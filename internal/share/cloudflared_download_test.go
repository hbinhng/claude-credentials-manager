package share

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestManagedCloudflaredPath_RespectsCCMHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	custom := filepath.Join(t.TempDir(), "custom-ccm")
	t.Setenv("CCM_HOME", custom)

	got := managedCloudflaredPath()
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name = "cloudflared.exe"
	}
	want := filepath.Join(custom, "bin", name)
	if got != want {
		t.Fatalf("managedCloudflaredPath() = %q, want %q", got, want)
	}
}

func TestManagedCloudflaredPath_DefaultsToHomeCcm(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("CCM_HOME", "")

	got := managedCloudflaredPath()
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name = "cloudflared.exe"
	}
	want := filepath.Join(tmp, ".ccm", "bin", name)
	if got != want {
		t.Fatalf("managedCloudflaredPath() = %q, want %q", got, want)
	}
}

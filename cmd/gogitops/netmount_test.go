package main

import (
	"strings"
	"testing"
)

func TestSystemdEscapePath(t *testing.T) {
	cases := map[string]string{
		"/media/benn/Bifrost":     "media-benn-Bifrost",
		"/media/benn/MiddleEarth": "media-benn-MiddleEarth",
		"/mnt/backup":             "mnt-backup",
		"/media/benn/Drive m2":    `media-benn-Drive\x20m2`,
	}
	for in, want := range cases {
		if got := systemdEscapePath(in); got != want {
			t.Errorf("systemdEscapePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsNetworkDevice(t *testing.T) {
	cases := []struct {
		dev      string
		smb, nfs bool
	}{
		{"//10.2.0.103/Bifrost", true, false},
		{"//benn@10.2.0.103/Bifrost", true, false},
		{"nas:/export/media", false, true},
		{"10.2.0.103:/share/x", false, true},
		{"uuid=3f2a01c2-1234", false, false},
		{"label=BACKUP", false, false},
		{"/dev/sdb1", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		smb, nfs := isNetworkDevice(c.dev)
		if smb != c.smb || nfs != c.nfs {
			t.Errorf("isNetworkDevice(%q) = (%v,%v), want (%v,%v)", c.dev, smb, nfs, c.smb, c.nfs)
		}
	}
}

func TestTranslateNetworkMountDarwin(t *testing.T) {
	step := recipeStep{
		Name:        "bifrost",
		Mount:       "Bifrost",
		MountDevice: "//10.2.0.103/Bifrost",
		MountAt:     "/media/benn/Bifrost", // must be IGNORED on darwin
	}
	out := translateNetworkMountDarwin(step, step.Mount, step.MountDevice, false, "")
	for _, want := range []string{
		"M_AT=/Volumes/Bifrost",
		"mount_smbfs //10.2.0.103/Bifrost",
		"state=already-mounted",
		"note=macOS ignores at=", // explicitly notes the ignored path
	} {
		if !strings.Contains(out, want) {
			t.Errorf("darwin translation missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/media/benn/Bifrost'") {
		t.Error("darwin translation must not mount at the linux path")
	}
}

func TestTranslateNetworkMountDarwinNFS(t *testing.T) {
	step := recipeStep{Name: "n", Mount: "media", MountDevice: "nas:/export/media"}
	out := translateNetworkMountDarwin(step, step.Mount, step.MountDevice, true, "")
	if !strings.Contains(out, "mount_nfs") {
		t.Error("darwin NFS translation missing mount_nfs")
	}
}

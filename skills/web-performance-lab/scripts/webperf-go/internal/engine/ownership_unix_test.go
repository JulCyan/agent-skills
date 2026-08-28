//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package engine

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestEffectiveUserOwnsRejectsForeignAndUnknownOwners(t *testing.T) {
	euid := uint32(os.Geteuid())
	foreign := euid + 1
	tests := []struct {
		name string
		sys  any
		want bool
	}{
		{name: "current effective user", sys: &syscall.Stat_t{Uid: euid}, want: true},
		{name: "foreign user", sys: &syscall.Stat_t{Uid: foreign}, want: false},
		{name: "unknown metadata", sys: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := ownerFileInfo{sys: tt.sys}
			if got := effectiveUserOwns(info); got != tt.want {
				t.Fatalf("effectiveUserOwns()=%t want=%t", got, tt.want)
			}
		})
	}
	foreignDirectory := ownerFileInfo{sys: &syscall.Stat_t{Uid: foreign}, mode: fs.ModeDir | 0o700}
	if trustedDirectory(foreignDirectory) {
		t.Fatal("trustedDirectory accepted a foreign-owned directory")
	}
}

func TestSafeEntryPermissionsRejectsGroupOrOtherWrite(t *testing.T) {
	tests := []struct {
		name string
		mode fs.FileMode
		want bool
	}{
		{name: "private file", mode: 0o600, want: true},
		{name: "shared read only", mode: 0o755, want: true},
		{name: "group writable", mode: 0o660, want: false},
		{name: "other writable", mode: 0o602, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeEntryPermissions(tt.mode); got != tt.want {
				t.Fatalf("safeEntryPermissions(%#o)=%t want=%t", tt.mode, got, tt.want)
			}
		})
	}
}

type ownerFileInfo struct {
	sys  any
	mode fs.FileMode
}

func (ownerFileInfo) Name() string { return "entry" }
func (ownerFileInfo) Size() int64  { return 0 }
func (i ownerFileInfo) Mode() fs.FileMode {
	if i.mode == 0 {
		return 0o600
	}
	return i.mode
}
func (ownerFileInfo) ModTime() time.Time { return time.Time{} }
func (ownerFileInfo) IsDir() bool        { return false }
func (i ownerFileInfo) Sys() any         { return i.sys }

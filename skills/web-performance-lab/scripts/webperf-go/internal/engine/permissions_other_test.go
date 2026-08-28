//go:build !(aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris)

package engine

import (
	"testing"
)

func TestSafeEntryPermissionsDoesNotApplyUnixBits(t *testing.T) {
	if !safeEntryPermissions(0o666) {
		t.Fatal("non-Unix permission predicate interpreted Unix group/other bits")
	}
}

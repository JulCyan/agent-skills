//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package engine

import "os"

func safeEntryPermissions(mode os.FileMode) bool {
	return mode.Perm()&0o022 == 0
}

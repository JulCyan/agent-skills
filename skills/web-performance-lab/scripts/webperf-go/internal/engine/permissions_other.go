//go:build !(aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris)

package engine

import "os"

// FileMode permission bits do not describe Windows ACL writability. Enforcing
// Unix group/other semantics there would reject ordinary 0666-looking files.
func safeEntryPermissions(os.FileMode) bool {
	return true
}

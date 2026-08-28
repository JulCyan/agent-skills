//go:build !(aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris)

package engine

import "os"

// os.FileInfo does not expose Windows ACL ownership or a portable effective
// user identifier. Non-Unix platforms still enforce entry type, no symlinks,
// the best identity checks available to os.Root, and the full payload digest.
func effectiveUserOwns(os.FileInfo) bool {
	return true
}

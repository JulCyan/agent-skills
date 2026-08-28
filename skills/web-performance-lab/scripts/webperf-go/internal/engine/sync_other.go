//go:build !(aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris)

package engine

// Directory handles do not support Sync on every non-Unix platform. The
// marker is still atomically published without replacement by os.Link.
func syncDirectory(string) error {
	return nil
}

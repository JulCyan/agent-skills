//go:build !(aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris)

package report

import "os"

const privateReportOutputSupported = false

func validPrivateReportFile(info os.FileInfo) bool {
	return info.Mode().IsRegular()
}

func syncReportDirectory(*os.Root) error {
	return nil
}

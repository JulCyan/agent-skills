package mediasync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// executionLease protects the mutable evidence checkpoint for a single
// upload/alt/verify process. The lock file is deliberately non-blocking: a
// second operator must inspect the checkpoint instead of racing a write.
type executionLease struct {
	files []*os.File
}

func acquireExecutionLease(opts commandOptions) (*executionLease, error) {
	lockPaths, canonicalPlanPath, evidencePath, err := executionLockPaths(opts)
	if err != nil {
		return nil, err
	}
	lease := &executionLease{}
	for _, lockPath := range lockPaths {
		if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
			_ = lease.release()
			return nil, err
		}
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			_ = lease.release()
			return nil, fmt.Errorf("获取 execution lease 失败(%s): %w", lockPath, err)
		}
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			file.Close()
			_ = lease.release()
			if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
				return nil, fmt.Errorf("execution lease 已占用：同一 plan 或 evidence checkpoint 正被其他 upload/alt/verify 命令使用（包括 apply）: %s；请等待当前命令完成后再从 checkpoint/evidence 恢复", lockPath)
			}
			return nil, fmt.Errorf("获取 execution lease 失败(%s): %w", lockPath, err)
		}
		lease.files = append(lease.files, file)
		metadata := fmt.Sprintf("command=%s\npid=%d\nplan=%s\nevidence=%s\nacquired_at=%s\n", opts.command, os.Getpid(), canonicalPlanPath, evidencePath, time.Now().UTC().Format(time.RFC3339))
		if err := writeLeaseMetadata(file, metadata); err != nil {
			_ = lease.release()
			return nil, fmt.Errorf("写 execution lease 失败(%s): %w", lockPath, err)
		}
	}
	return lease, nil
}

func executionLeaseActive(opts commandOptions) (bool, error) {
	lockPaths, _, _, err := executionLockPaths(opts)
	if err != nil {
		return false, err
	}
	for _, lockPath := range lockPaths {
		file, err := os.Open(lockPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == syscall.EWOULDBLOCK || lockErr == syscall.EAGAIN {
			_ = file.Close()
			return true, nil
		}
		if lockErr != nil {
			_ = file.Close()
			return false, lockErr
		}
		unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		closeErr := file.Close()
		if unlockErr != nil {
			return false, unlockErr
		}
		if closeErr != nil {
			return false, closeErr
		}
	}
	return false, nil
}

func executionLockPaths(opts commandOptions) ([]string, string, string, error) {
	planPath, err := resolvePlanPath(opts)
	if err != nil {
		return nil, "", "", err
	}
	canonicalPlanPath, err := canonicalExecutionPath(planPath)
	if err != nil {
		return nil, "", "", err
	}
	evidencePath, err := canonicalExecutionPath(resolveEvidencePath(opts, planPath))
	if err != nil {
		return nil, "", "", err
	}
	lockPaths := []string{canonicalPlanPath + ".execution.lock", evidencePath + ".execution.lock"}
	if lockPaths[0] == lockPaths[1] {
		lockPaths = lockPaths[:1]
	}
	sort.Strings(lockPaths)
	return lockPaths, canonicalPlanPath, evidencePath, nil
}

func writeLeaseMetadata(file *os.File, metadata string) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.WriteString(metadata); err != nil {
		return err
	}
	return file.Sync()
}

func canonicalExecutionPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// A pre-existing evidence file can itself be a symlink. Resolve the whole
	// path in that case so direct and aliased invocations contend for one lock.
	if _, err := os.Lstat(absolute); err == nil {
		resolvedPath, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", err
		}
		return resolvedPath, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	// The evidence file is normally created by the first execution. Canonicalize
	// an existing parent directory for that first-write case.
	dir, base := filepath.Dir(absolute), filepath.Base(absolute)
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err == nil {
		dir = resolvedDir
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return filepath.Join(dir, base), nil
}

func (lease *executionLease) release() error {
	if lease == nil {
		return nil
	}
	var firstErr error
	for i := len(lease.files) - 1; i >= 0; i-- {
		file := lease.files[i]
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	lease.files = nil
	return firstErr
}

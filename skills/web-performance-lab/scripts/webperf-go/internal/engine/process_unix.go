//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package engine

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

var errInvalidProcess = errors.New("invalid owned process")

const (
	guardianParentEnv = "WEBPERF_INTERNAL_GUARDIAN_PARENT"
	guardianPipeEnv   = "WEBPERF_INTERNAL_GUARDIAN_PIPE_FD"
	guardianPipeFD    = 3
)

func init() {
	parentPID, err := strconv.Atoi(os.Getenv(guardianParentEnv))
	pipeFD, pipeErr := strconv.Atoi(os.Getenv(guardianPipeEnv))
	if err != nil || parentPID <= 0 || pipeErr != nil || pipeFD != guardianPipeFD {
		return
	}
	if parentPID != os.Getppid() {
		killGuardianGroup()
	}
	parentLifetime := os.NewFile(uintptr(pipeFD), "webperf-guardian-parent-lifetime")
	if parentLifetime == nil {
		killGuardianGroup()
	}
	defer parentLifetime.Close()
	signal.Ignore(syscall.SIGTERM)
	for {
		var buffer [1]byte
		if _, err := parentLifetime.Read(buffer[:]); err != nil {
			killGuardianGroup()
		}
	}
}

func killGuardianGroup() {
	pid := os.Getpid()
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	os.Exit(1)
}

type ownedCommand struct {
	command   *exec.Cmd
	guardian  *exec.Cmd
	pgid      int
	keepalive *os.File
}

func startOwnedCommand(command *exec.Cmd) (*ownedCommand, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	parentLifetime, keepalive, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	guardian := exec.Command(executable)
	guardian.Env = appendWithoutKey(os.Environ(), guardianParentEnv)
	guardian.Env = appendWithoutKey(guardian.Env, guardianPipeEnv)
	guardian.Env = append(guardian.Env, guardianParentEnv+"="+strconv.Itoa(os.Getpid()))
	guardian.Env = append(guardian.Env, guardianPipeEnv+"="+strconv.Itoa(guardianPipeFD))
	guardian.ExtraFiles = []*os.File{parentLifetime}
	guardian.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := guardian.Start(); err != nil {
		_ = parentLifetime.Close()
		_ = keepalive.Close()
		return nil, err
	}
	_ = parentLifetime.Close()
	if guardian.Process == nil || guardian.Process.Pid <= 0 {
		_ = keepalive.Close()
		_ = guardian.Wait()
		return nil, errInvalidProcess
	}
	owned := &ownedCommand{command: command, guardian: guardian, pgid: guardian.Process.Pid, keepalive: keepalive}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: owned.pgid}
	if err := command.Start(); err != nil {
		_ = owned.kill()
		owned.release()
		return nil, err
	}
	return owned, nil
}

func (c *ownedCommand) wait() error {
	return c.command.Wait()
}

func (c *ownedCommand) terminate() error {
	return c.signal(syscall.SIGTERM)
}

func (c *ownedCommand) kill() error {
	return c.signal(syscall.SIGKILL)
}

func (c *ownedCommand) signal(signal syscall.Signal) error {
	if c == nil || c.guardian == nil || c.guardian.Process == nil || c.pgid <= 0 || c.guardian.Process.Pid != c.pgid {
		return errInvalidProcess
	}
	return syscall.Kill(-c.pgid, signal)
}

func (c *ownedCommand) release() {
	if c != nil && c.guardian != nil {
		if c.guardian.Process != nil {
			_ = c.guardian.Process.Kill()
		}
		if c.keepalive != nil {
			_ = c.keepalive.Close()
		}
		_ = c.guardian.Wait()
	}
}

func appendWithoutKey(environment []string, key string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment))
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return result
}

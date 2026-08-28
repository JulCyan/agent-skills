//go:build !(aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris)

package engine

import (
	"errors"
	"os/exec"
)

var errInvalidProcess = errors.New("invalid owned process")

type ownedCommand struct {
	command *exec.Cmd
}

func startOwnedCommand(command *exec.Cmd) (*ownedCommand, error) {
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &ownedCommand{command: command}, nil
}

func (c *ownedCommand) wait() error {
	return c.command.Wait()
}

func (c *ownedCommand) terminate() error {
	if c == nil || c.command == nil || c.command.Process == nil || c.command.Process.Pid <= 0 {
		return errInvalidProcess
	}
	return c.command.Process.Kill()
}

func (c *ownedCommand) kill() error {
	if c == nil || c.command == nil || c.command.Process == nil || c.command.Process.Pid <= 0 {
		return errInvalidProcess
	}
	return c.command.Process.Kill()
}

func (c *ownedCommand) release() {}

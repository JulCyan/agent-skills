// Package contract defines the stable machine-readable webperf interface.
package contract

import "errors"

const SchemaVersion = 1

const (
	ExitOK           = 0
	ExitGeneralError = 1
	ExitInvalidInput = 2
	ExitNeedsSetup   = 3
	ExitInterrupted  = 130
)

type Status string

const (
	OK                   Status = "OK"
	Partial              Status = "PARTIAL"
	Inconclusive         Status = "INCONCLUSIVE"
	NeedsSetup           Status = "NEEDS_SETUP"
	InvalidInput         Status = "INVALID_INPUT"
	EngineFailed         Status = "ENGINE_FAILED"
	ParseFailed          Status = "PARSE_FAILED"
	ReportFailed         Status = "REPORT_FAILED"
	IncompatibleProtocol Status = "INCOMPATIBLE_PROTOCOL"
	Interrupted          Status = "INTERRUPTED"
)

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNeedsSetup   = errors.New("needs setup")
	ErrInterrupted  = errors.New("interrupted")
)

type Envelope struct {
	SchemaVersion int           `json:"schemaVersion"`
	Command       string        `json:"command"`
	Status        Status        `json:"status"`
	Data          any           `json:"data,omitempty"`
	Warnings      []string      `json:"warnings,omitempty"`
	Error         *CommandError `json:"error,omitempty"`
}

type CommandError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`

	cause error
}

func (e *CommandError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *CommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func NewCommandError(code, message, remediation string, cause error) *CommandError {
	return &CommandError{Code: code, Message: message, Remediation: remediation, cause: cause}
}

func ExitCode(status Status) int {
	switch status {
	case OK:
		return ExitOK
	case InvalidInput:
		return ExitInvalidInput
	case NeedsSetup:
		return ExitNeedsSetup
	case Interrupted:
		return ExitInterrupted
	default:
		return ExitGeneralError
	}
}

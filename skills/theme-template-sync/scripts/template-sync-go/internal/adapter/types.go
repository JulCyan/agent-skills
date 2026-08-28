package adapter

import (
	"context"
	"errors"
	"strings"

	"theme-template-sync/internal/templatejson"
)

var ErrTemplateNotFound = errors.New("template asset not found")

type postWriteCleanupError struct {
	cause error
}

func NewPostWriteCleanupError(cause error) error {
	if cause == nil {
		cause = errors.New("local cleanup failed")
	}
	return &postWriteCleanupError{cause: cause}
}

func (e *postWriteCleanupError) Error() string {
	return "remote write completed but local cleanup failed: " + e.cause.Error()
}

func (e *postWriteCleanupError) Unwrap() error {
	return e.cause
}

func IsPostWriteCleanupError(err error) bool {
	var cleanupError *postWriteCleanupError
	return errors.As(err, &cleanupError)
}

type StoreRef string

type ThemeRef string

type ResolvedTheme struct {
	Store string `json:"store"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

func (t ResolvedTheme) IsLive() bool {
	role := strings.ToLower(t.Role)
	return role == "live" || role == "main"
}

type ThemeAdapter interface {
	ResolveTheme(context.Context, StoreRef, ThemeRef) (ResolvedTheme, error)
	ReadTemplate(
		context.Context,
		ResolvedTheme,
		templatejson.TemplateKey,
	) ([]byte, error)
	WriteTemplate(
		context.Context,
		ResolvedTheme,
		templatejson.TemplateKey,
		[]byte,
	) error
}

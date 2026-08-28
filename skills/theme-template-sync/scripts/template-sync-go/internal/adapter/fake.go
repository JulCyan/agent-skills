package adapter

import (
	"context"
	"errors"
	"sync"

	"theme-template-sync/internal/templatejson"
)

type Operation string

const (
	OperationResolve Operation = "resolve"
	OperationRead    Operation = "read"
	OperationWrite   Operation = "write"
)

type Call struct {
	Sequence   int                      `json:"sequence"`
	Operation  Operation                `json:"operation"`
	Store      StoreRef                 `json:"store,omitempty"`
	ThemeRef   ThemeRef                 `json:"theme_ref,omitempty"`
	Theme      ResolvedTheme            `json:"theme,omitempty"`
	Template   templatejson.TemplateKey `json:"template,omitempty"`
	BodyLength int                      `json:"body_length,omitempty"`
}

type FakeAdapter struct {
	ResolveFunc func(context.Context, StoreRef, ThemeRef) (ResolvedTheme, error)
	ReadFunc    func(context.Context, ResolvedTheme, templatejson.TemplateKey) ([]byte, error)
	WriteFunc   func(context.Context, ResolvedTheme, templatejson.TemplateKey, []byte) error

	mu                  sync.Mutex
	calls               []Call
	writeCount          int
	activeWrites        int
	maxConcurrentWrites int
}

func NewFakeAdapter() *FakeAdapter {
	return &FakeAdapter{calls: []Call{}}
}

func (f *FakeAdapter) ResolveTheme(
	ctx context.Context,
	store StoreRef,
	ref ThemeRef,
) (ResolvedTheme, error) {
	f.record(Call{Operation: OperationResolve, Store: store, ThemeRef: ref})
	if f.ResolveFunc == nil {
		return ResolvedTheme{}, errors.New("fake resolve hook is not configured")
	}
	return f.ResolveFunc(ctx, store, ref)
}

func (f *FakeAdapter) ReadTemplate(
	ctx context.Context,
	theme ResolvedTheme,
	key templatejson.TemplateKey,
) ([]byte, error) {
	f.record(Call{Operation: OperationRead, Theme: theme, Template: key})
	if f.ReadFunc == nil {
		return nil, errors.New("fake read hook is not configured")
	}
	return f.ReadFunc(ctx, theme, key)
}

func (f *FakeAdapter) WriteTemplate(
	ctx context.Context,
	theme ResolvedTheme,
	key templatejson.TemplateKey,
	body []byte,
) error {
	f.mu.Lock()
	f.writeCount++
	f.activeWrites++
	if f.activeWrites > f.maxConcurrentWrites {
		f.maxConcurrentWrites = f.activeWrites
	}
	f.calls = append(f.calls, Call{
		Sequence:   len(f.calls) + 1,
		Operation:  OperationWrite,
		Theme:      theme,
		Template:   key,
		BodyLength: len(body),
	})
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.activeWrites--
		f.mu.Unlock()
	}()
	if f.WriteFunc == nil {
		return errors.New("fake write hook is not configured")
	}
	return f.WriteFunc(ctx, theme, key, body)
}

func (f *FakeAdapter) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]Call{}, f.calls...)
}

func (f *FakeAdapter) WriteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.writeCount
}

func (f *FakeAdapter) MaxConcurrentWrites() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.maxConcurrentWrites
}

func (f *FakeAdapter) record(call Call) {
	f.mu.Lock()
	defer f.mu.Unlock()

	call.Sequence = len(f.calls) + 1
	f.calls = append(f.calls, call)
}

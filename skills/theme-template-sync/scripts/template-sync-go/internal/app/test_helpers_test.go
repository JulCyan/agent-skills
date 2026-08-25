package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"theme-template-sync/internal/adapter"
	"theme-template-sync/internal/syncengine"
	"theme-template-sync/internal/templatejson"
)

type remoteFixture struct {
	t *testing.T

	Adapter *adapter.FakeAdapter

	mu              sync.Mutex
	themes          map[string]adapter.ResolvedTheme
	documents       map[string][]byte
	resolveCount    int
	readCount       int
	writeCount      int
	resolveOverride func(int, adapter.StoreRef, adapter.ThemeRef) (adapter.ResolvedTheme, error)
	readOverride    func(int, adapter.ResolvedTheme, templatejson.TemplateKey) ([]byte, error)
	writeOverride   func(int, adapter.ResolvedTheme, templatejson.TemplateKey, []byte) error
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()

	remote := &remoteFixture{
		t:         t,
		themes:    map[string]adapter.ResolvedTheme{},
		documents: map[string][]byte{},
	}
	remote.Adapter = adapter.NewFakeAdapter()
	remote.Adapter.ResolveFunc = func(
		_ context.Context,
		store adapter.StoreRef,
		ref adapter.ThemeRef,
	) (adapter.ResolvedTheme, error) {
		remote.mu.Lock()
		remote.resolveCount++
		count := remote.resolveCount
		override := remote.resolveOverride
		theme, ok := remote.themes[endpointKey(store, ref)]
		remote.mu.Unlock()
		if override != nil {
			return override(count, store, ref)
		}
		if !ok {
			return adapter.ResolvedTheme{}, fmt.Errorf("synthetic theme %s/%s not found", store, ref)
		}
		return theme, nil
	}
	remote.Adapter.ReadFunc = func(
		_ context.Context,
		theme adapter.ResolvedTheme,
		key templatejson.TemplateKey,
	) ([]byte, error) {
		remote.mu.Lock()
		remote.readCount++
		count := remote.readCount
		override := remote.readOverride
		body, ok := remote.documents[theme.ID]
		body = append([]byte{}, body...)
		remote.mu.Unlock()
		if override != nil {
			return override(count, theme, key)
		}
		if !ok {
			return nil, fmt.Errorf("%w: %s", adapter.ErrTemplateNotFound, key.AssetPath())
		}
		return body, nil
	}
	remote.Adapter.WriteFunc = func(
		_ context.Context,
		theme adapter.ResolvedTheme,
		key templatejson.TemplateKey,
		body []byte,
	) error {
		remote.mu.Lock()
		remote.writeCount++
		count := remote.writeCount
		override := remote.writeOverride
		remote.mu.Unlock()
		if override != nil {
			if err := override(count, theme, key, body); err != nil {
				return err
			}
		}
		remote.mu.Lock()
		remote.documents[theme.ID] = append([]byte{}, body...)
		remote.mu.Unlock()
		return nil
	}
	return remote
}

func (r *remoteFixture) addTheme(
	store string,
	ref string,
	id string,
	name string,
	role string,
	body string,
) adapter.ResolvedTheme {
	r.t.Helper()

	theme := adapter.ResolvedTheme{Store: store, ID: id, Name: name, Role: role}
	r.mu.Lock()
	r.themes[endpointKey(adapter.StoreRef(store), adapter.ThemeRef(ref))] = theme
	if body != "" {
		r.documents[id] = []byte(body)
	}
	r.mu.Unlock()
	return theme
}

func (r *remoteFixture) setDocument(themeID string, body string) {
	r.t.Helper()

	r.mu.Lock()
	r.documents[themeID] = []byte(body)
	r.mu.Unlock()
}

func (r *remoteFixture) document(themeID string) []byte {
	r.t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte{}, r.documents[themeID]...)
}

func (r *remoteFixture) setResolveOverride(
	function func(int, adapter.StoreRef, adapter.ThemeRef) (adapter.ResolvedTheme, error),
) {
	r.mu.Lock()
	r.resolveOverride = function
	r.mu.Unlock()
}

func (r *remoteFixture) setReadOverride(
	function func(int, adapter.ResolvedTheme, templatejson.TemplateKey) ([]byte, error),
) {
	r.mu.Lock()
	r.readOverride = function
	r.mu.Unlock()
}

func (r *remoteFixture) setWriteOverride(
	function func(int, adapter.ResolvedTheme, templatejson.TemplateKey, []byte) error,
) {
	r.mu.Lock()
	r.writeOverride = function
	r.mu.Unlock()
}

func testDependencies(
	t *testing.T,
	remote *remoteFixture,
	caller string,
) Dependencies {
	t.Helper()

	return Dependencies{
		Adapter:   remote.Adapter,
		CallerCWD: caller,
		Now: func() time.Time {
			return time.Date(2026, time.August, 25, 8, 0, 0, 0, time.UTC)
		},
		Random: bytes.NewReader([]byte{
			1, 2, 3, 4,
			5, 6, 7, 8,
			9, 10, 11, 12,
		}),
	}
}

func endpointKey(store adapter.StoreRef, ref adapter.ThemeRef) string {
	return string(store) + "\x00" + string(ref)
}

func standardRemote(t *testing.T) *remoteFixture {
	t.Helper()

	remote := newRemoteFixture(t)
	remote.addTheme(
		"store-source",
		"Source",
		"101",
		"Source",
		"unpublished",
		`/* source */
{"sections":{"hero":{"custom_css":[".hero { color: red; }"]}},"order":["hero"],"source_unknown":true}`,
	)
	remote.addTheme(
		"store-one",
		"Target One",
		"201",
		"Target One",
		"unpublished",
		`/* target one */
{"sections":{"hero":{"custom_css":[".hero { color: blue; }"],"settings":{"keep":1}}},"order":["hero"],"target_unknown":"one"}`,
	)
	remote.addTheme(
		"store-two",
		"Target Two",
		"202",
		"Target Two",
		"development",
		`/* target two */
{"sections":{"hero":{"custom_css":[".hero { color: green; }"],"blocks":[{"id":"keep"}]}},"order":["hero"],"target_unknown":"two"}`,
	)
	return remote
}

func standardPlanOptions(runtimeRoot string) PlanOptions {
	return PlanOptions{
		Template: "page.example",
		Source:   EndpointRef{Store: "store-source", Theme: "Source"},
		Targets: []EndpointRef{
			{Store: "store-one", Theme: "Target One"},
			{Store: "store-two", Theme: "Target Two"},
		},
		Scope:       templateFieldScope(),
		RuntimeRoot: runtimeRoot,
		Warnings:    []string{},
	}
}

func templateFieldScope() syncengine.Scope {
	return syncengine.Scope{
		Mode:       syncengine.ModeField,
		SectionKey: "hero",
		Field:      "custom_css",
	}
}

func assertNoWrites(t *testing.T, remote *remoteFixture) {
	t.Helper()

	if got := remote.Adapter.WriteCount(); got != 0 {
		t.Fatalf("write count = %d, want 0", got)
	}
}

func syntheticFailure(message string) error {
	return errors.New(message)
}

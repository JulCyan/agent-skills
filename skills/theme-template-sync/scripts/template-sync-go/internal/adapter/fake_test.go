package adapter

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"theme-template-sync/internal/templatejson"
)

func TestFakeAdapter_RecordsCallsAndDelegatesHooks(t *testing.T) {
	t.Parallel()

	theme := ResolvedTheme{
		Store: "store-one",
		ID:    "101",
		Name:  "Synthetic Target",
		Role:  "unpublished",
	}
	key := templatejson.TemplateKey("page.example")
	fake := NewFakeAdapter()
	fake.ResolveFunc = func(
		_ context.Context,
		store StoreRef,
		ref ThemeRef,
	) (ResolvedTheme, error) {
		if store != "store-one" || ref != "Synthetic Target" {
			t.Fatalf("resolve args = %q/%q", store, ref)
		}
		return theme, nil
	}
	fake.ReadFunc = func(
		_ context.Context,
		gotTheme ResolvedTheme,
		gotKey templatejson.TemplateKey,
	) ([]byte, error) {
		if gotTheme != theme || gotKey != key {
			t.Fatalf("read args = %#v/%q", gotTheme, gotKey)
		}
		return []byte(`{"sections":{},"order":[]}`), nil
	}
	fake.WriteFunc = func(
		_ context.Context,
		gotTheme ResolvedTheme,
		gotKey templatejson.TemplateKey,
		body []byte,
	) error {
		if gotTheme != theme || gotKey != key || string(body) != "planned" {
			t.Fatalf("write args = %#v/%q/%q", gotTheme, gotKey, body)
		}
		return nil
	}

	resolved, err := fake.ResolveTheme(context.Background(), "store-one", "Synthetic Target")
	if err != nil {
		t.Fatalf("ResolveTheme(): %v", err)
	}
	if _, err := fake.ReadTemplate(context.Background(), resolved, key); err != nil {
		t.Fatalf("ReadTemplate(): %v", err)
	}
	if err := fake.WriteTemplate(context.Background(), resolved, key, []byte("planned")); err != nil {
		t.Fatalf("WriteTemplate(): %v", err)
	}

	wantOperations := []Operation{OperationResolve, OperationRead, OperationWrite}
	calls := fake.Calls()
	gotOperations := make([]Operation, 0, len(calls))
	for _, call := range calls {
		gotOperations = append(gotOperations, call.Operation)
	}
	if !reflect.DeepEqual(gotOperations, wantOperations) {
		t.Fatalf("operations = %v, want %v", gotOperations, wantOperations)
	}
	if fake.WriteCount() != 1 {
		t.Fatalf("WriteCount() = %d, want 1", fake.WriteCount())
	}
	if fake.MaxConcurrentWrites() != 1 {
		t.Fatalf("MaxConcurrentWrites() = %d, want 1", fake.MaxConcurrentWrites())
	}
}

func TestFakeAdapter_PropagatesInjectedFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("synthetic read failure")
	fake := NewFakeAdapter()
	fake.ReadFunc = func(
		context.Context,
		ResolvedTheme,
		templatejson.TemplateKey,
	) ([]byte, error) {
		return nil, want
	}

	_, err := fake.ReadTemplate(
		context.Background(),
		ResolvedTheme{Store: "store-one", ID: "101", Name: "One", Role: "unpublished"},
		"page.example",
	)
	if !errors.Is(err, want) {
		t.Fatalf("ReadTemplate() error = %v, want injected failure", err)
	}
}

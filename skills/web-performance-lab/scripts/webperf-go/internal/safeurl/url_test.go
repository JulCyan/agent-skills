package safeurl

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAcceptsPublicHTTPURLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "https", input: "https://example.test/path"},
		{name: "http", input: "http://example.test:8080/path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Validate(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tt.input {
				t.Fatalf("url=%q", got)
			}
		})
	}
}

func TestValidateRejectsUnsafeOrIncompleteURLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "relative", input: "/path"},
		{name: "unsupported scheme", input: "ftp://example.test/file"},
		{name: "missing host", input: "https:///path"},
		{name: "userinfo", input: "https://user:secret@example.test/path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Validate(tt.input)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error exposed userinfo: %q", err)
			}
		})
	}
}

func TestDisplayRedactsQueryValues(t *testing.T) {
	parsed, err := Validate("https://example.test/p?a=secret&b=two#fragment")
	if err != nil {
		t.Fatal(err)
	}
	if got := Display(parsed); got != "https://example.test/p?a=REDACTED&b=REDACTED" {
		t.Fatalf("display=%q", got)
	}
}

func TestDisplayOmitsFragmentAndPreservesQueryNames(t *testing.T) {
	parsed, err := Validate("https://example.test/p?flag&flag=&encoded=value%20with%20spaces#fragment")
	if err != nil {
		t.Fatal(err)
	}
	if got := Display(parsed); got != "https://example.test/p?flag=REDACTED&flag=REDACTED&encoded=REDACTED" {
		t.Fatalf("display=%q", got)
	}
}

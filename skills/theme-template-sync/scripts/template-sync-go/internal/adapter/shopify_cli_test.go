package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"theme-template-sync/internal/templatejson"
)

type runnerCall struct {
	command string
	args    []string
	cwd     string
}

type fakeRunner struct {
	calls []runnerCall
	run   func(command string, args []string, cwd string) ([]byte, error)
}

func (f *fakeRunner) Run(
	_ context.Context,
	command string,
	args []string,
	cwd string,
) ([]byte, error) {
	f.calls = append(f.calls, runnerCall{
		command: command,
		args:    append([]string{}, args...),
		cwd:     cwd,
	})
	if f.run == nil {
		return []byte{}, nil
	}
	return f.run(command, args, cwd)
}

func TestShopifyCLIAdapter_ResolveTheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		store      StoreRef
		ref        ThemeRef
		output     string
		runnerErr  error
		want       ResolvedTheme
		wantErr    bool
		wantNoCall bool
	}{
		{
			name:   "numeric id",
			store:  "store-one",
			ref:    "101",
			output: `[{"id":101,"name":"Synthetic One","role":"unpublished"}]`,
			want:   ResolvedTheme{Store: "store-one", ID: "101", Name: "Synthetic One", Role: "unpublished"},
		},
		{
			name:   "string id",
			store:  "store-one.myshopify.com",
			ref:    "202",
			output: `[{"id":"202","name":"Synthetic Two","role":"development"}]`,
			want:   ResolvedTheme{Store: "store-one.myshopify.com", ID: "202", Name: "Synthetic Two", Role: "development"},
		},
		{
			name:  "unique exact name keeps metacharacter as data",
			store: "store-one",
			ref:   "Synthetic; no shell",
			output: `[
  {"id":301,"name":"Synthetic","role":"unpublished"},
  {"id":302,"name":"Synthetic; no shell","role":"unpublished"}
]`,
			want: ResolvedTheme{Store: "store-one", ID: "302", Name: "Synthetic; no shell", Role: "unpublished"},
		},
		{
			name:    "zero match",
			store:   "store-one",
			ref:     "Missing",
			output:  `[{"id":101,"name":"Other","role":"unpublished"}]`,
			wantErr: true,
		},
		{
			name:  "duplicate exact name",
			store: "store-one",
			ref:   "Duplicate",
			output: `[
  {"id":101,"name":"Duplicate","role":"unpublished"},
  {"id":102,"name":"Duplicate","role":"unpublished"}
]`,
			wantErr: true,
		},
		{
			name:    "unknown role",
			store:   "store-one",
			ref:     "101",
			output:  `[{"id":101,"name":"Unknown","role":"mystery"}]`,
			wantErr: true,
		},
		{
			name:      "runner failure",
			store:     "store-one",
			ref:       "101",
			runnerErr: errors.New("synthetic CLI failure"),
			wantErr:   true,
		},
		{name: "empty store", ref: "101", wantErr: true, wantNoCall: true},
		{name: "store URL rejected", store: "https://store-one.myshopify.com", ref: "101", wantErr: true, wantNoCall: true},
		{name: "store path rejected", store: "store/one", ref: "101", wantErr: true, wantNoCall: true},
		{name: "empty theme", store: "store-one", wantErr: true, wantNoCall: true},
		{name: "theme newline rejected", store: "store-one", ref: "one\ntwo", wantErr: true, wantNoCall: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runner := &fakeRunner{run: func(string, []string, string) ([]byte, error) {
				return []byte(test.output), test.runnerErr
			}}
			client := NewShopifyCLIAdapter(runner, "shopify")
			got, err := client.ResolveTheme(context.Background(), test.store, test.ref)
			if test.wantErr {
				if err == nil {
					t.Fatal("ResolveTheme() error = nil")
				}
				if test.wantNoCall && len(runner.calls) != 0 {
					t.Fatalf("runner calls = %d, want 0", len(runner.calls))
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveTheme(): %v", err)
			}
			if got != test.want {
				t.Fatalf("ResolveTheme() = %#v, want %#v", got, test.want)
			}
			wantArgs := []string{"theme", "list", "--store", string(test.store), "--json"}
			if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, wantArgs) {
				t.Fatalf("runner calls = %#v, want args %v", runner.calls, wantArgs)
			}
		})
	}
}

func TestResolvedTheme_IsLive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role string
		want bool
	}{
		{role: "live", want: true},
		{role: "LIVE", want: true},
		{role: "main", want: true},
		{role: "unpublished", want: false},
		{role: "development", want: false},
	}
	for _, test := range tests {
		t.Run(test.role, func(t *testing.T) {
			t.Parallel()
			if got := (ResolvedTheme{Role: test.role}).IsLive(); got != test.want {
				t.Fatalf("IsLive() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestShopifyCLIAdapter_ReadTemplateUsesOneAsset(t *testing.T) {
	t.Parallel()

	theme := ResolvedTheme{Store: "store-one", ID: "101", Name: "Target", Role: "unpublished"}
	key := templatejson.TemplateKey("product.lp4")
	wantBody := []byte("/* remote */\n{\"sections\":{},\"order\":[]}")
	runner := &fakeRunner{run: func(_ string, args []string, _ string) ([]byte, error) {
		root := flagValue(t, args, "--path")
		asset := filepath.Join(root, filepath.FromSlash(key.AssetPath()))
		if err := os.MkdirAll(filepath.Dir(asset), 0o700); err != nil {
			t.Fatalf("creating pulled template dir: %v", err)
		}
		if err := os.WriteFile(asset, wantBody, 0o600); err != nil {
			t.Fatalf("writing pulled template: %v", err)
		}
		return []byte{}, nil
	}}
	client := NewShopifyCLIAdapter(runner, "shopify")

	got, err := client.ReadTemplate(context.Background(), theme, key)
	if err != nil {
		t.Fatalf("ReadTemplate(): %v", err)
	}
	if !reflect.DeepEqual(got, wantBody) {
		t.Fatalf("ReadTemplate() = %q, want %q", got, wantBody)
	}
	wantArgs := []string{
		"theme", "pull",
		"--path", flagValue(t, runner.calls[0].args, "--path"),
		"--store", "store-one",
		"--theme", "101",
		"--only", "templates/product.lp4.json",
		"--nodelete",
	}
	if !reflect.DeepEqual(runner.calls[0].args, wantArgs) {
		t.Fatalf("pull args = %v, want %v", runner.calls[0].args, wantArgs)
	}
}

func TestShopifyCLIAdapter_ReadTemplateReportsMissingAsset(t *testing.T) {
	t.Parallel()

	client := NewShopifyCLIAdapter(&fakeRunner{}, "shopify")
	_, err := client.ReadTemplate(
		context.Background(),
		ResolvedTheme{Store: "store-one", ID: "101", Name: "Target", Role: "unpublished"},
		"page.missing",
	)
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("ReadTemplate() error = %v, want ErrTemplateNotFound", err)
	}
}

func TestShopifyCLIAdapter_NestedTemplateUsesScopedAssetPath(t *testing.T) {
	t.Parallel()

	theme := ResolvedTheme{Store: "store-one", ID: "101", Name: "Target", Role: "unpublished"}
	key := templatejson.TemplateKey("customers/account")
	wantBody := []byte(`{"sections":{},"order":[]}`)
	runner := &fakeRunner{run: func(_ string, args []string, _ string) ([]byte, error) {
		root := flagValue(t, args, "--path")
		asset := filepath.Join(root, "templates", "customers", "account.json")
		switch args[1] {
		case "pull":
			if err := os.MkdirAll(filepath.Dir(asset), 0o700); err != nil {
				t.Fatalf("creating nested pull directory: %v", err)
			}
			if err := os.WriteFile(asset, wantBody, 0o600); err != nil {
				t.Fatalf("writing nested pull asset: %v", err)
			}
		case "push":
			body, err := os.ReadFile(asset)
			if err != nil {
				t.Fatalf("reading nested push asset: %v", err)
			}
			if !reflect.DeepEqual(body, wantBody) {
				t.Fatalf("nested push body = %q, want %q", body, wantBody)
			}
		}
		return []byte(`{"theme":{"id":101}}`), nil
	}}
	client := NewShopifyCLIAdapter(runner, "shopify")

	if _, err := client.ReadTemplate(context.Background(), theme, key); err != nil {
		t.Fatalf("ReadTemplate(): %v", err)
	}
	if err := client.WriteTemplate(context.Background(), theme, key, wantBody); err != nil {
		t.Fatalf("WriteTemplate(): %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.calls))
	}
	for _, call := range runner.calls {
		if got := flagValue(t, call.args, "--only"); got != "templates/customers/account.json" {
			t.Fatalf("--only = %q, want nested template asset", got)
		}
	}
}

func TestShopifyCLIAdapter_WriteTemplateUsesSafePushFlags(t *testing.T) {
	t.Parallel()

	theme := ResolvedTheme{Store: "store-one", ID: "101", Name: "Target", Role: "unpublished"}
	key := templatejson.TemplateKey("collection.example")
	wantBody := []byte("{\"order\":[],\"sections\":{}}\n")
	var pushedBody []byte
	runner := &fakeRunner{run: func(_ string, args []string, _ string) ([]byte, error) {
		root := flagValue(t, args, "--path")
		asset := filepath.Join(root, filepath.FromSlash(key.AssetPath()))
		var err error
		pushedBody, err = os.ReadFile(asset)
		if err != nil {
			t.Fatalf("reading pushed template: %v", err)
		}
		return []byte(`{"theme":{"id":101,"name":"Target","role":"unpublished"}}`), nil
	}}
	client := NewShopifyCLIAdapter(runner, "shopify")

	if err := client.WriteTemplate(context.Background(), theme, key, wantBody); err != nil {
		t.Fatalf("WriteTemplate(): %v", err)
	}
	if !reflect.DeepEqual(pushedBody, wantBody) {
		t.Fatalf("pushed body = %q, want %q", pushedBody, wantBody)
	}
	wantArgs := []string{
		"theme", "push",
		"--path", flagValue(t, runner.calls[0].args, "--path"),
		"--store", "store-one",
		"--theme", "101",
		"--only", "templates/collection.example.json",
		"--nodelete",
		"--json",
	}
	if !reflect.DeepEqual(runner.calls[0].args, wantArgs) {
		t.Fatalf("push args = %v, want %v", runner.calls[0].args, wantArgs)
	}
	for _, forbidden := range []string{
		"--allow-live",
		"--live",
		"--publish",
		"--unpublished",
		"--development",
	} {
		if slices.Contains(runner.calls[0].args, forbidden) {
			t.Fatalf("push args contain forbidden flag %q: %v", forbidden, runner.calls[0].args)
		}
	}
}

func TestShopifyCLIAdapter_WriteTemplateRejectsLiveBeforeRunner(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	client := NewShopifyCLIAdapter(runner, "shopify")
	err := client.WriteTemplate(
		context.Background(),
		ResolvedTheme{Store: "store-one", ID: "101", Name: "Live", Role: "main"},
		"page.example",
		[]byte(`{"sections":{},"order":[]}`),
	)
	if err == nil {
		t.Fatal("WriteTemplate() error = nil")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %v, want none", runner.calls)
	}
}

func TestShopifyCLIAdapter_PostPushCleanupFailurePreservesWriteOutcome(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{run: func(string, []string, string) ([]byte, error) {
		return []byte(`{"theme":{"id":101}}`), nil
	}}
	client := NewShopifyCLIAdapter(runner, "shopify")
	client.removeAll = func(string) error {
		return errors.New("synthetic cleanup failure")
	}
	err := client.WriteTemplate(
		context.Background(),
		ResolvedTheme{Store: "store-one", ID: "101", Name: "Target", Role: "unpublished"},
		"page.example",
		[]byte(`{"sections":{},"order":[]}`),
	)
	if err == nil {
		t.Fatal("WriteTemplate() error = nil")
	}
	if !IsPostWriteCleanupError(err) {
		t.Fatalf("WriteTemplate() error = %v, want accepted-write cleanup error", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1 successful push", len(runner.calls))
	}
}

func TestOSCommandRunner_PreservesArgumentBoundaries(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	script := filepath.Join(directory, "capture.sh")
	output := filepath.Join(directory, "args.txt")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", output)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing helper: %v", err)
	}
	runner := OSCommandRunner{}
	if _, err := runner.Run(
		context.Background(),
		script,
		[]string{"one; echo injected", "two words"},
		directory,
	); err != nil {
		t.Fatalf("Run(): %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("reading captured args: %v", err)
	}
	if got, want := strings.Split(strings.TrimSpace(string(data)), "\n"), []string{"one; echo injected", "two words"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("captured args = %v, want %v", got, want)
	}
}

func TestOSCommandRunner_ScrubsShopifyFlagEnvironment(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "capture-env.sh")
	output := filepath.Join(directory, "env.txt")
	body := fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s\\n' \"${SHOPIFY_FLAG_PUBLISH-unset}\" \"${SHOPIFY_FLAG_ALLOW_LIVE-unset}\" \"${SHOPIFY_CLI_THEME_TOKEN-unset}\" > %q\n",
		output,
	)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing helper: %v", err)
	}
	t.Setenv("SHOPIFY_FLAG_PUBLISH", "1")
	t.Setenv("SHOPIFY_FLAG_ALLOW_LIVE", "1")
	t.Setenv("SHOPIFY_CLI_THEME_TOKEN", "synthetic-auth-session")

	runner := OSCommandRunner{}
	if _, err := runner.Run(context.Background(), script, []string{}, directory); err != nil {
		t.Fatalf("Run(): %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("reading captured environment: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"unset", "unset", "synthetic-auth-session"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captured environment = %v, want %v", got, want)
	}
}

func TestOSCommandRunner_ReturnsBoundedRedactedStderr(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	script := filepath.Join(directory, "fail.sh")
	const shopifySecret = "synthetic-shopify-secret"
	const bearerSecret = "synthetic-bearer-secret"
	const jsonSecret = "synthetic-json-secret"
	const basicSecret = "synthetic-basic-secret"
	const firstCookieSecret = "synthetic-cookie-first"
	const secondCookieSecret = "synthetic-cookie-second"
	const digestUser = "synthetic-digest-user"
	const digestResponse = "synthetic-digest-response"
	const digestNonce = "synthetic-digest-nonce"
	body := "#!/bin/sh\nprintf '%s\\n' 'Authentication failed SHOPIFY_ACCESS_TOKEN=" +
		shopifySecret + " Bearer " + bearerSecret +
		" {\"access_token\":\"" + jsonSecret + "\"}" +
		" Authorization: Basic " + basicSecret + "'" +
		" 'Cookie: first=" + firstCookieSecret + "; session=" + secondCookieSecret +
		"' 'Authorization: Digest username=\"" + digestUser + "\", response=\"" +
		digestResponse + "\", nonce=\"" + digestNonce + "\"' >&2\nexit 9\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing helper: %v", err)
	}

	runner := OSCommandRunner{}
	_, err := runner.Run(context.Background(), script, []string{}, directory)
	if err == nil {
		t.Fatal("Run() error = nil")
	}
	message := err.Error()
	if !strings.Contains(message, "Authentication failed") {
		t.Fatalf("Run() error = %q, want actionable stderr summary", message)
	}
	for _, secret := range []string{
		shopifySecret,
		bearerSecret,
		jsonSecret,
		basicSecret,
		firstCookieSecret,
		secondCookieSecret,
		digestUser,
		digestResponse,
		digestNonce,
	} {
		if strings.Contains(message, secret) {
			t.Fatalf("Run() error leaked %q: %q", secret, message)
		}
	}
	if !strings.Contains(message, "[redacted]") {
		t.Fatalf("Run() error = %q, want redaction marker", message)
	}
	if len(message) > 320 {
		t.Fatalf("Run() error length = %d, want bounded diagnostic", len(message))
	}
}

func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()

	index := slices.Index(args, flag)
	if index < 0 || index+1 >= len(args) {
		t.Fatalf("args %v do not contain value for %s", args, flag)
	}
	return args[index+1]
}

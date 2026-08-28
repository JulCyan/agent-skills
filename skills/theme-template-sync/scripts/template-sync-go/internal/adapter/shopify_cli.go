package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"theme-template-sync/internal/evidence"
	"theme-template-sync/internal/templatejson"
)

type CommandRunner interface {
	Run(context.Context, string, []string, string) ([]byte, error)
}

type OSCommandRunner struct{}

func (OSCommandRunner) Run(
	ctx context.Context,
	command string,
	args []string,
	cwd string,
) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = cwd
	cmd.Env = shopifyCommandEnvironment(os.Environ())
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.TrimSpace(stderr.String()) != "" {
			return nil, fmt.Errorf(
				"running %s: %w: %s",
				command,
				err,
				evidence.SanitizeMessage(stderr.String()),
			)
		}
		return nil, fmt.Errorf("running %s: %w", command, err)
	}
	return stdout.Bytes(), nil
}

func shopifyCommandEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "SHOPIFY_FLAG_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

type ShopifyCLIAdapter struct {
	runner    CommandRunner
	command   string
	removeAll func(string) error
}

func NewShopifyCLIAdapter(runner CommandRunner, command string) *ShopifyCLIAdapter {
	if runner == nil {
		runner = OSCommandRunner{}
	}
	if command == "" {
		command = "shopify"
	}
	return &ShopifyCLIAdapter{
		runner:    runner,
		command:   command,
		removeAll: os.RemoveAll,
	}
}

var (
	storePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(?:\.myshopify\.com)?$`)
	themeIDPattern = regexp.MustCompile(`^[0-9]+$`)
)

func (a *ShopifyCLIAdapter) ResolveTheme(
	ctx context.Context,
	store StoreRef,
	ref ThemeRef,
) (ResolvedTheme, error) {
	if err := validateStoreRef(store); err != nil {
		return ResolvedTheme{}, err
	}
	if err := validateThemeRef(ref); err != nil {
		return ResolvedTheme{}, err
	}

	output, err := a.runner.Run(
		ctx,
		a.command,
		[]string{"theme", "list", "--store", string(store), "--json"},
		"",
	)
	if err != nil {
		return ResolvedTheme{}, fmt.Errorf("listing themes for store %q: %w", store, err)
	}
	themes, err := decodeThemeList(output, string(store))
	if err != nil {
		return ResolvedTheme{}, err
	}

	matches := make([]ResolvedTheme, 0, 1)
	for _, theme := range themes {
		if theme.ID == string(ref) || (!themeIDPattern.MatchString(string(ref)) && theme.Name == string(ref)) {
			matches = append(matches, theme)
		}
	}
	if len(matches) == 0 {
		return ResolvedTheme{}, fmt.Errorf("theme %q was not found in store %q", ref, store)
	}
	if len(matches) > 1 {
		return ResolvedTheme{}, fmt.Errorf("theme name %q is not unique in store %q", ref, store)
	}
	return matches[0], nil
}

func (a *ShopifyCLIAdapter) ReadTemplate(
	ctx context.Context,
	theme ResolvedTheme,
	key templatejson.TemplateKey,
) (body []byte, returnErr error) {
	if err := validateResolvedTheme(theme); err != nil {
		return nil, err
	}
	if err := validateTemplateKey(key); err != nil {
		return nil, err
	}

	temporaryRoot, err := os.MkdirTemp("", "theme-template-sync-pull-*")
	if err != nil {
		return nil, fmt.Errorf("creating pull directory: %w", err)
	}
	defer func() {
		if err := a.removeTemporaryRoot(temporaryRoot); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("removing pull directory: %w", err))
		}
	}()

	args := []string{
		"theme", "pull",
		"--path", temporaryRoot,
		"--store", theme.Store,
		"--theme", theme.ID,
		"--only", key.AssetPath(),
		"--nodelete",
	}
	if _, err := a.runner.Run(ctx, a.command, args, ""); err != nil {
		return nil, fmt.Errorf("pulling template %q: %w", key, err)
	}
	assetPath := filepath.Join(temporaryRoot, filepath.FromSlash(key.AssetPath()))
	body, err = os.ReadFile(assetPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, key.AssetPath())
	}
	if err != nil {
		return nil, fmt.Errorf("reading pulled template: %w", err)
	}
	return body, nil
}

func (a *ShopifyCLIAdapter) WriteTemplate(
	ctx context.Context,
	theme ResolvedTheme,
	key templatejson.TemplateKey,
	body []byte,
) error {
	if err := validateResolvedTheme(theme); err != nil {
		return err
	}
	if theme.IsLive() {
		return fmt.Errorf("refusing to write Live theme %q", theme.Name)
	}
	if err := validateTemplateKey(key); err != nil {
		return err
	}

	temporaryRoot, err := os.MkdirTemp("", "theme-template-sync-push-*")
	if err != nil {
		return fmt.Errorf("creating push directory: %w", err)
	}
	operationErr := func() error {
		assetPath := filepath.Join(temporaryRoot, filepath.FromSlash(key.AssetPath()))
		if err := os.MkdirAll(filepath.Dir(assetPath), 0o700); err != nil {
			return fmt.Errorf("creating push asset directory: %w", err)
		}
		if err := os.WriteFile(assetPath, body, 0o600); err != nil {
			return fmt.Errorf("writing push asset: %w", err)
		}
		args := []string{
			"theme", "push",
			"--path", temporaryRoot,
			"--store", theme.Store,
			"--theme", theme.ID,
			"--only", key.AssetPath(),
			"--nodelete",
			"--json",
		}
		if _, err := a.runner.Run(ctx, a.command, args, ""); err != nil {
			return fmt.Errorf("pushing template %q: %w", key, err)
		}
		return nil
	}()
	cleanupErr := a.removeTemporaryRoot(temporaryRoot)
	if operationErr != nil {
		if cleanupErr != nil {
			return errors.Join(
				operationErr,
				fmt.Errorf("removing push directory: %w", cleanupErr),
			)
		}
		return operationErr
	}
	if cleanupErr != nil {
		return NewPostWriteCleanupError(cleanupErr)
	}
	return nil
}

func (a *ShopifyCLIAdapter) removeTemporaryRoot(path string) error {
	removeAll := a.removeAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	return removeAll(path)
}

type listedTheme struct {
	ID   any    `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

func decodeThemeList(data []byte, store string) ([]ResolvedTheme, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	listed := []listedTheme{}
	if err := decoder.Decode(&listed); err != nil {
		return nil, fmt.Errorf("decoding theme list for store %q: %w", store, err)
	}
	themes := make([]ResolvedTheme, 0, len(listed))
	for index, item := range listed {
		id, err := normalizeThemeID(item.ID)
		if err != nil {
			return nil, fmt.Errorf("decoding theme %d id: %w", index, err)
		}
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if !validThemeRole(role) {
			return nil, fmt.Errorf("theme %q has unsupported role %q", item.Name, item.Role)
		}
		if strings.TrimSpace(item.Name) == "" {
			return nil, fmt.Errorf("theme %s has an empty name", id)
		}
		themes = append(themes, ResolvedTheme{
			Store: store,
			ID:    id,
			Name:  item.Name,
			Role:  role,
		})
	}
	return themes, nil
}

func normalizeThemeID(value any) (string, error) {
	var id string
	switch typed := value.(type) {
	case json.Number:
		id = typed.String()
	case string:
		id = typed
	default:
		return "", fmt.Errorf("unsupported ID type %T", value)
	}
	if !themeIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid theme ID %q", id)
	}
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return "", fmt.Errorf("parsing theme ID %q: %w", id, err)
	}
	return id, nil
}

func validateStoreRef(store StoreRef) error {
	if !storePattern.MatchString(string(store)) {
		return fmt.Errorf("invalid store reference %q", store)
	}
	return nil
}

// ValidateStoreRef checks that a store reference is a bare Shopify shop name
// or its myshopify.com hostname.
func ValidateStoreRef(store StoreRef) error {
	return validateStoreRef(store)
}

func validateThemeRef(ref ThemeRef) error {
	value := string(ref)
	if strings.TrimSpace(value) == "" {
		return errors.New("theme reference is required")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("theme reference contains a control character")
		}
	}
	return nil
}

// ValidateThemeRef checks that a theme selector is non-empty and single-line.
func ValidateThemeRef(ref ThemeRef) error {
	return validateThemeRef(ref)
}

func validateResolvedTheme(theme ResolvedTheme) error {
	if err := validateStoreRef(StoreRef(theme.Store)); err != nil {
		return err
	}
	if !themeIDPattern.MatchString(theme.ID) {
		return fmt.Errorf("invalid resolved theme ID %q", theme.ID)
	}
	if strings.TrimSpace(theme.Name) == "" {
		return errors.New("resolved theme name is required")
	}
	if !validThemeRole(strings.ToLower(theme.Role)) {
		return fmt.Errorf("invalid resolved theme role %q", theme.Role)
	}
	return nil
}

// ValidateResolvedTheme checks the identity returned by an adapter before it
// is persisted as plan authority.
func ValidateResolvedTheme(theme ResolvedTheme) error {
	return validateResolvedTheme(theme)
}

func validateTemplateKey(key templatejson.TemplateKey) error {
	parsed, _, err := templatejson.ParseTemplateKey(string(key), "")
	if err != nil {
		return fmt.Errorf("validating template key: %w", err)
	}
	if parsed != key {
		return fmt.Errorf("template key %q is not canonical", key)
	}
	return nil
}

func validThemeRole(role string) bool {
	return role == "live" ||
		role == "main" ||
		role == "unpublished" ||
		role == "development"
}

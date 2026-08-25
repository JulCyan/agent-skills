package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"theme-template-sync/internal/adapter"
	"theme-template-sync/internal/syncengine"
	"theme-template-sync/internal/templatejson"
)

type EndpointRef struct {
	Store adapter.StoreRef `json:"store"`
	Theme adapter.ThemeRef `json:"theme"`
}

type PlanOptions struct {
	Template    templatejson.TemplateKey
	Warnings    []string
	Source      EndpointRef
	Targets     []EndpointRef
	Scope       syncengine.Scope
	AllowRemove bool
	RuntimeRoot string
}

type ApplyOptions struct {
	PlanPath    string
	Execute     bool
	AllowRemove bool
}

type stringValues []string

func (values *stringValues) String() string {
	return strings.Join(*values, ",")
}

func (values *stringValues) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func parsePlanOptions(args []string, callerCWD string) (PlanOptions, error) {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var templateValue string
	var pageValue string
	var sourceStore string
	var sourceTheme string
	var targetStores stringValues
	var targetThemes stringValues
	var modeValue string
	var sectionKey string
	var fieldValue string
	var allowRemove bool
	var runtimeRoot string
	flags.StringVar(&templateValue, "template", "", "Shopify JSON template key")
	flags.StringVar(&pageValue, "page", "", "deprecated page template alias")
	flags.StringVar(&sourceStore, "source-store", "", "source store")
	flags.StringVar(&sourceTheme, "source-theme", "", "source theme ID or exact name")
	flags.Var(&targetStores, "target-store", "target store; repeat for each target")
	flags.Var(&targetThemes, "target-theme", "target theme ID or exact name; repeat for each target")
	flags.StringVar(&modeValue, "mode", string(syncengine.ModeTemplate), "template, section, or field")
	flags.StringVar(&sectionKey, "section", "", "section key")
	flags.StringVar(&fieldValue, "field", "", "allowed section field")
	flags.BoolVar(&allowRemove, "allow-remove", false, "authorize reviewed removals")
	flags.StringVar(&runtimeRoot, "runtime-root", "", "explicit evidence root")
	if err := flags.Parse(args); err != nil {
		return PlanOptions{}, fmt.Errorf("parsing plan flags: %w", err)
	}
	if flags.NArg() != 0 {
		return PlanOptions{}, fmt.Errorf("plan does not accept positional arguments: %v", flags.Args())
	}

	templateKey, warnings, err := templatejson.ParseTemplateKey(templateValue, pageValue)
	if err != nil {
		return PlanOptions{}, err
	}
	if strings.TrimSpace(sourceStore) == "" || strings.TrimSpace(sourceTheme) == "" {
		return PlanOptions{}, errors.New("source store and theme are required")
	}
	if len(targetStores) == 0 || len(targetStores) != len(targetThemes) {
		return PlanOptions{}, errors.New("target store and theme flags must be non-empty paired values")
	}
	targets := make([]EndpointRef, 0, len(targetStores))
	for index := range targetStores {
		if strings.TrimSpace(targetStores[index]) == "" || strings.TrimSpace(targetThemes[index]) == "" {
			return PlanOptions{}, fmt.Errorf("target %d store and theme are required", index)
		}
		targets = append(targets, EndpointRef{
			Store: adapter.StoreRef(targetStores[index]),
			Theme: adapter.ThemeRef(targetThemes[index]),
		})
	}

	scope := syncengine.Scope{
		Mode:       syncengine.Mode(modeValue),
		SectionKey: sectionKey,
		Field:      fieldValue,
	}
	if err := scope.Validate(); err != nil {
		return PlanOptions{}, fmt.Errorf("validating sync scope: %w", err)
	}
	resolvedRoot, err := resolveCallerPath(callerCWD, runtimeRoot, false)
	if err != nil {
		return PlanOptions{}, fmt.Errorf("resolving runtime root: %w", err)
	}
	return PlanOptions{
		Template:    templateKey,
		Warnings:    warnings,
		Source:      EndpointRef{Store: adapter.StoreRef(sourceStore), Theme: adapter.ThemeRef(sourceTheme)},
		Targets:     targets,
		Scope:       scope,
		AllowRemove: allowRemove,
		RuntimeRoot: resolvedRoot,
	}, nil
}

func parseApplyOptions(args []string, callerCWD string) (ApplyOptions, error) {
	flags := flag.NewFlagSet("apply", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var planPath string
	var execute bool
	var allowRemove bool
	flags.StringVar(&planPath, "plan", "", "explicit plan path")
	flags.BoolVar(&execute, "execute", false, "perform plan-bound writes")
	flags.BoolVar(&allowRemove, "allow-remove", false, "authorize reviewed removals")
	if err := flags.Parse(args); err != nil {
		return ApplyOptions{}, fmt.Errorf("parsing apply flags: %w", err)
	}
	if flags.NArg() != 0 {
		return ApplyOptions{}, fmt.Errorf("apply does not accept positional arguments: %v", flags.Args())
	}
	resolvedPlan, err := resolveCallerPath(callerCWD, planPath, true)
	if err != nil {
		return ApplyOptions{}, fmt.Errorf("resolving plan path: %w", err)
	}
	return ApplyOptions{
		PlanPath:    resolvedPlan,
		Execute:     execute,
		AllowRemove: allowRemove,
	}, nil
}

func resolveCallerPath(callerCWD string, value string, required bool) (string, error) {
	if value == "" {
		if required {
			return "", errors.New("path is required")
		}
		return "", nil
	}
	if strings.TrimSpace(callerCWD) == "" {
		return "", errors.New("caller working directory is required")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	return filepath.Abs(filepath.Join(callerCWD, value))
}

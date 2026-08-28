package templatejson

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type TemplateKey string

var (
	templateKeyPattern = regexp.MustCompile(
		`^[a-z0-9][a-z0-9_-]*(?:\.[a-z0-9][a-z0-9_-]*)?(?:/[a-z0-9][a-z0-9_-]*(?:\.[a-z0-9][a-z0-9_-]*)?)?$`,
	)
	pageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

func ParseTemplateKey(template, page string) (TemplateKey, []string, error) {
	warnings := []string{}
	if template != "" && page != "" {
		return "", warnings, errors.New("template and page are mutually exclusive")
	}
	if template == "" && page == "" {
		return "", warnings, errors.New("template is required")
	}

	value := template
	if page != "" {
		if !pageNamePattern.MatchString(page) {
			return "", warnings, fmt.Errorf("invalid page name %q", page)
		}
		value = "page." + page
		warnings = append(warnings, "--page is deprecated; use --template page.<name>")
	}
	if strings.HasPrefix(value, "templates/") ||
		strings.HasSuffix(value, ".json") ||
		!templateKeyPattern.MatchString(value) {
		return "", warnings, fmt.Errorf("invalid template key %q", value)
	}

	return TemplateKey(value), warnings, nil
}

func (k TemplateKey) AssetPath() string {
	return "templates/" + string(k) + ".json"
}

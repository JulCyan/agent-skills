// Package safeurl validates public URLs and renders safe display strings.
package safeurl

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrInvalid = errors.New("invalid url")

func Validate(input string) (*url.URL, error) {
	if strings.TrimSpace(input) == "" {
		return nil, invalid("url is required")
	}

	parsed, err := url.ParseRequestURI(input)
	if err != nil {
		return nil, invalid("url is malformed")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, invalid("url scheme must be http or https")
	}
	if parsed.Host == "" {
		return nil, invalid("url host is required")
	}
	if parsed.User != nil {
		return nil, invalid("url userinfo is not allowed")
	}
	return parsed, nil
}

func Display(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}

	copy := *parsed
	copy.User = nil
	copy.Fragment = ""
	copy.RawFragment = ""
	copy.RawQuery = redactQuery(parsed.RawQuery)
	return copy.String()
}

func invalid(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalid, message)
}

func redactQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}

	parts := strings.Split(rawQuery, "&")
	redacted := make([]string, 0, len(parts))
	for _, part := range parts {
		name, _, _ := strings.Cut(part, "=")
		redacted = append(redacted, name+"=REDACTED")
	}
	return strings.Join(redacted, "&")
}

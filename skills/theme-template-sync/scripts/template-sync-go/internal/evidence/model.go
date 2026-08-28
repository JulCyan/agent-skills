package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"unicode"

	"theme-template-sync/internal/syncengine"
)

type Identity struct {
	Store string `json:"store"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

type BoundTarget struct {
	Identity      Identity `json:"identity"`
	BeforeSHA256  string   `json:"before_sha256"`
	PlannedSHA256 string   `json:"planned_sha256"`
}

type PreviewBinding struct {
	PlanSHA256         string           `json:"plan_sha256"`
	AllowRemove        bool             `json:"allow_remove"`
	Template           string           `json:"template"`
	Scope              syncengine.Scope `json:"scope"`
	Source             Identity         `json:"source"`
	SourceBeforeSHA256 string           `json:"source_before_sha256"`
	Targets            []BoundTarget    `json:"targets"`
}

type FailureRecord struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

var (
	sha256Pattern        = regexp.MustCompile(`^[a-f0-9]{64}$`)
	authorizationPattern = regexp.MustCompile(
		`(?i)["']?authorization["']?\s*[:=]\s*[^\r\n]*`,
	)
	cookieHeaderPattern = regexp.MustCompile(
		`(?i)["']?(?:set-cookie|cookie)["']?\s*[:=]\s*[^\r\n]*`,
	)
	credentialPattern = regexp.MustCompile(
		`(?i)["']?([a-z0-9_]*(?:access[_-]?token|token|cookie|password|secret|authorization)[a-z0-9_]*)["']?\s*[:=]\s*(?:bearer\s+)?(?:"[^"]*"|'[^']*'|[^\s,;]+)`,
	)
	bearerPattern = regexp.MustCompile(
		`(?i)\bbearer\s+(?:"[^"]*"|'[^']*'|[^\s,;]+)`,
	)
)

func SHA256JSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encoding JSON for SHA-256: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func MatchPreviewBinding(expected PreviewBinding, actual PreviewBinding) error {
	if err := expected.Validate(); err != nil {
		return fmt.Errorf("validating expected preview binding: %w", err)
	}
	if err := actual.Validate(); err != nil {
		return fmt.Errorf("validating actual preview binding: %w", err)
	}
	if !reflect.DeepEqual(expected, actual) {
		return errors.New("preview binding does not match current preflight")
	}
	return nil
}

func (b PreviewBinding) Validate() error {
	if !sha256Pattern.MatchString(b.PlanSHA256) {
		return errors.New("preview binding plan SHA-256 is invalid")
	}
	if b.Template == "" {
		return errors.New("preview binding template is required")
	}
	if err := b.Scope.Validate(); err != nil {
		return fmt.Errorf("validating preview binding scope: %w", err)
	}
	if err := validateIdentity(b.Source); err != nil {
		return fmt.Errorf("validating preview binding source: %w", err)
	}
	if !sha256Pattern.MatchString(b.SourceBeforeSHA256) {
		return errors.New("preview binding source SHA-256 is invalid")
	}
	if len(b.Targets) == 0 {
		return errors.New("preview binding requires at least one target")
	}
	for index, target := range b.Targets {
		if err := validateIdentity(target.Identity); err != nil {
			return fmt.Errorf("validating preview binding target %d: %w", index, err)
		}
		if !sha256Pattern.MatchString(target.BeforeSHA256) {
			return fmt.Errorf("preview binding target %d before SHA-256 is invalid", index)
		}
		if !sha256Pattern.MatchString(target.PlannedSHA256) {
			return fmt.Errorf("preview binding target %d planned SHA-256 is invalid", index)
		}
	}
	return nil
}

func NewFailureRecord(kind string, err error) FailureRecord {
	message := "operation failed"
	if err != nil {
		message = SanitizeMessage(err.Error())
	}
	return FailureRecord{Kind: kind, Message: message}
}

func validateIdentity(identity Identity) error {
	if identity.Store == "" || identity.ID == "" || identity.Name == "" || identity.Role == "" {
		return errors.New("identity store, id, name, and role are required")
	}
	return nil
}

// SanitizeMessage returns a bounded, single-line diagnostic with common
// credential-shaped values redacted.
func SanitizeMessage(message string) string {
	redactionInput := message
	quoteUnescaper := strings.NewReplacer(`\"`, `"`, `\'`, `'`)
	for range 3 {
		redactionInput = quoteUnescaper.Replace(redactionInput)
	}
	redacted := authorizationPattern.ReplaceAllString(redactionInput, "Authorization=[redacted]")
	redacted = cookieHeaderPattern.ReplaceAllString(redacted, "Cookie=[redacted]")
	redacted = credentialPattern.ReplaceAllString(redacted, "$1=[redacted]")
	redacted = bearerPattern.ReplaceAllString(redacted, "Bearer [redacted]")
	var safe strings.Builder
	for _, character := range redacted {
		if unicode.IsControl(character) {
			safe.WriteByte(' ')
			continue
		}
		safe.WriteRune(character)
	}
	result := strings.TrimSpace(safe.String())
	const maximumLength = 240
	runes := []rune(result)
	if len(runes) > maximumLength {
		result = string(runes[:maximumLength])
	}
	if result == "" {
		return "operation failed"
	}
	return result
}

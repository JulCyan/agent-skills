package syncengine

import (
	"errors"
	"fmt"
	"regexp"
)

type Mode string

const (
	ModeTemplate Mode = "template"
	ModeSection  Mode = "section"
	ModeField    Mode = "field"
)

type Scope struct {
	Mode       Mode   `json:"mode"`
	SectionKey string `json:"section_key,omitempty"`
	Field      string `json:"field,omitempty"`
}

var sectionKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (s Scope) Validate() error {
	switch s.Mode {
	case ModeTemplate:
		if s.SectionKey != "" || s.Field != "" {
			return errors.New("template mode does not accept section or field")
		}
	case ModeSection:
		if !sectionKeyPattern.MatchString(s.SectionKey) {
			return fmt.Errorf("invalid section key %q", s.SectionKey)
		}
		if s.Field != "" {
			return errors.New("section mode does not accept field")
		}
	case ModeField:
		if !sectionKeyPattern.MatchString(s.SectionKey) {
			return fmt.Errorf("invalid section key %q", s.SectionKey)
		}
		if s.Field != "custom_css" {
			return fmt.Errorf("field %q is not allowed; allowed field is custom_css", s.Field)
		}
	default:
		return fmt.Errorf("unsupported sync mode %q", s.Mode)
	}
	return nil
}

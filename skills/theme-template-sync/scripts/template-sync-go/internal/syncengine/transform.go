package syncengine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"theme-template-sync/internal/templatejson"
)

func Transform(
	source templatejson.Document,
	target templatejson.Document,
	scope Scope,
) (templatejson.Document, error) {
	if err := scope.Validate(); err != nil {
		return templatejson.Document{}, fmt.Errorf("validating sync scope: %w", err)
	}
	if err := source.Validate(); err != nil {
		return templatejson.Document{}, fmt.Errorf("validating source template: %w", err)
	}
	if err := target.Validate(); err != nil {
		return templatejson.Document{}, fmt.Errorf("validating target template: %w", err)
	}

	var planned templatejson.Document
	var err error
	switch scope.Mode {
	case ModeTemplate:
		planned, err = source.Clone()
	case ModeSection:
		planned, err = transformSection(source, target, scope.SectionKey)
	case ModeField:
		planned, err = transformCustomCSS(source, target, scope.SectionKey)
	default:
		return templatejson.Document{}, fmt.Errorf("unsupported sync mode %q", scope.Mode)
	}
	if err != nil {
		return templatejson.Document{}, err
	}
	if err := planned.Validate(); err != nil {
		return templatejson.Document{}, fmt.Errorf("validating planned template: %w", err)
	}
	return planned, nil
}

func transformSection(
	source templatejson.Document,
	target templatejson.Document,
	sectionKey string,
) (templatejson.Document, error) {
	sourceSections := source.Root["sections"].(map[string]any)
	sourceSection, ok := sourceSections[sectionKey]
	if !ok {
		return templatejson.Document{}, fmt.Errorf("source section %q is missing", sectionKey)
	}
	if _, ok := sourceSection.(map[string]any); !ok {
		return templatejson.Document{}, fmt.Errorf("source section %q must be an object", sectionKey)
	}

	planned, err := target.Clone()
	if err != nil {
		return templatejson.Document{}, fmt.Errorf("cloning target template: %w", err)
	}
	plannedSections := planned.Root["sections"].(map[string]any)
	clonedSection, err := cloneJSONValue(sourceSection)
	if err != nil {
		return templatejson.Document{}, fmt.Errorf("cloning source section %q: %w", sectionKey, err)
	}
	plannedSections[sectionKey] = clonedSection

	order := documentOrder(planned)
	if containsString(order, sectionKey) {
		return planned, nil
	}
	sourceOrder := documentOrder(source)
	planned.Root["order"] = stringsToAny(insertAfterSourcePredecessor(order, sourceOrder, sectionKey))
	return planned, nil
}

func transformCustomCSS(
	source templatejson.Document,
	target templatejson.Document,
	sectionKey string,
) (templatejson.Document, error) {
	sourceSection, err := sectionObject(source, sectionKey, "source")
	if err != nil {
		return templatejson.Document{}, err
	}
	targetSection, err := sectionObject(target, sectionKey, "target")
	if err != nil {
		return templatejson.Document{}, err
	}
	sourceCSS, err := stringArrayField(sourceSection, sectionKey, "source")
	if err != nil {
		return templatejson.Document{}, err
	}
	if _, err := stringArrayField(targetSection, sectionKey, "target"); err != nil {
		return templatejson.Document{}, err
	}

	planned, err := target.Clone()
	if err != nil {
		return templatejson.Document{}, fmt.Errorf("cloning target template: %w", err)
	}
	plannedSection := planned.Root["sections"].(map[string]any)[sectionKey].(map[string]any)
	plannedSection["custom_css"] = stringsToAny(sourceCSS)
	return planned, nil
}

func sectionObject(
	document templatejson.Document,
	sectionKey string,
	label string,
) (map[string]any, error) {
	sections := document.Root["sections"].(map[string]any)
	value, ok := sections[sectionKey]
	if !ok {
		return nil, fmt.Errorf("%s sections.%s is missing", label, sectionKey)
	}
	section, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s sections.%s must be an object", label, sectionKey)
	}
	return section, nil
}

func stringArrayField(
	section map[string]any,
	sectionKey string,
	label string,
) ([]string, error) {
	value, ok := section["custom_css"]
	if !ok {
		return nil, fmt.Errorf("%s sections.%s.custom_css is missing", label, sectionKey)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s sections.%s.custom_css must be a string array", label, sectionKey)
	}
	result := make([]string, 0, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf(
				"%s sections.%s.custom_css[%d] must be a string",
				label,
				sectionKey,
				index,
			)
		}
		result = append(result, text)
	}
	return result, nil
}

func documentOrder(document templatejson.Document) []string {
	values := document.Root["order"].([]any)
	order := make([]string, 0, len(values))
	for _, value := range values {
		order = append(order, value.(string))
	}
	return order
}

func insertAfterSourcePredecessor(
	targetOrder []string,
	sourceOrder []string,
	sectionKey string,
) []string {
	insertAt := len(targetOrder)
	sourceIndex := indexOf(sourceOrder, sectionKey)
	for index := sourceIndex - 1; index >= 0; index-- {
		targetIndex := indexOf(targetOrder, sourceOrder[index])
		if targetIndex >= 0 {
			insertAt = targetIndex + 1
			break
		}
	}

	result := make([]string, 0, len(targetOrder)+1)
	result = append(result, targetOrder[:insertAt]...)
	result = append(result, sectionKey)
	result = append(result, targetOrder[insertAt:]...)
	return result
}

func indexOf(values []string, wanted string) int {
	for index, value := range values {
		if value == wanted {
			return index
		}
	}
	return -1
}

func containsString(values []string, wanted string) bool {
	return indexOf(values, wanted) >= 0
}

func stringsToAny(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func cloneJSONValue(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encoding JSON value: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var clone any
	if err := decoder.Decode(&clone); err != nil {
		return nil, fmt.Errorf("decoding JSON value: %w", err)
	}
	if decoder.More() {
		return nil, errors.New("cloned JSON value contains trailing data")
	}
	return clone, nil
}

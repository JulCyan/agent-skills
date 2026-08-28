package syncengine

import (
	"fmt"
	"reflect"
	"slices"
	"sort"

	"theme-template-sync/internal/templatejson"
)

type Change struct {
	Path   string `json:"path"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

type OrderMove struct {
	SectionKey string `json:"section_key"`
	From       int    `json:"from"`
	To         int    `json:"to"`
}

type OrderDiff struct {
	Changed bool        `json:"changed"`
	Before  []string    `json:"before"`
	After   []string    `json:"after"`
	Added   []string    `json:"added"`
	Removed []string    `json:"removed"`
	Moved   []OrderMove `json:"moved"`
}

type SectionSummary struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

type Diff struct {
	Added    []Change       `json:"added"`
	Removed  []Change       `json:"removed"`
	Changed  []Change       `json:"changed"`
	Order    OrderDiff      `json:"order"`
	Sections SectionSummary `json:"sections"`
}

func Compare(
	before templatejson.Document,
	planned templatejson.Document,
) (Diff, error) {
	if err := before.Validate(); err != nil {
		return Diff{}, fmt.Errorf("validating before template: %w", err)
	}
	if err := planned.Validate(); err != nil {
		return Diff{}, fmt.Errorf("validating planned template: %w", err)
	}

	diff := newDiff()
	compareObjects("", before.Root, planned.Root, &diff, true)
	diff.Order = compareOrder(documentOrder(before), documentOrder(planned))
	diff.Sections = compareSections(
		before.Root["sections"].(map[string]any),
		planned.Root["sections"].(map[string]any),
	)
	sortChanges(diff.Added)
	sortChanges(diff.Removed)
	sortChanges(diff.Changed)
	return diff, nil
}

func (d Diff) Empty() bool {
	return len(d.Added) == 0 &&
		len(d.Removed) == 0 &&
		len(d.Changed) == 0 &&
		!d.Order.Changed
}

func (d Diff) HasRemoval() bool {
	return len(d.Removed) > 0 || len(d.Order.Removed) > 0
}

func newDiff() Diff {
	return Diff{
		Added:   []Change{},
		Removed: []Change{},
		Changed: []Change{},
		Order: OrderDiff{
			Before:  []string{},
			After:   []string{},
			Added:   []string{},
			Removed: []string{},
			Moved:   []OrderMove{},
		},
		Sections: SectionSummary{
			Added:   []string{},
			Removed: []string{},
			Changed: []string{},
		},
	}
}

func compareValues(path string, before any, after any, diff *Diff) {
	beforeObject, beforeIsObject := before.(map[string]any)
	afterObject, afterIsObject := after.(map[string]any)
	if beforeIsObject && afterIsObject {
		compareObjects(path, beforeObject, afterObject, diff, false)
		return
	}

	beforeArray, beforeIsArray := before.([]any)
	afterArray, afterIsArray := after.([]any)
	if beforeIsArray && afterIsArray {
		compareArrays(path, beforeArray, afterArray, diff)
		return
	}
	if reflect.DeepEqual(before, after) {
		return
	}
	diff.Changed = append(diff.Changed, Change{Path: path, Before: before, After: after})
	if beforeIsObject || beforeIsArray {
		recordContainerChanges(path, before, &diff.Removed, true)
	}
	if afterIsObject || afterIsArray {
		recordContainerChanges(path, after, &diff.Added, false)
	}
}

func recordContainerChanges(
	path string,
	value any,
	changes *[]Change,
	removed bool,
) {
	appendChange := func(changePath string, changeValue any) {
		change := Change{Path: changePath}
		if removed {
			change.Before = changeValue
		} else {
			change.After = changeValue
		}
		*changes = append(*changes, change)
	}

	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			appendChange(path, typed)
			return
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := joinPath(path, key)
			child := typed[key]
			switch child.(type) {
			case map[string]any, []any:
				recordContainerChanges(childPath, child, changes, removed)
			default:
				appendChange(childPath, child)
			}
		}
	case []any:
		if len(typed) == 0 {
			appendChange(path, typed)
			return
		}
		for index, child := range typed {
			childPath := indexPath(path, index)
			switch child.(type) {
			case map[string]any, []any:
				recordContainerChanges(childPath, child, changes, removed)
			default:
				appendChange(childPath, child)
			}
		}
	}
}

func compareObjects(
	path string,
	before map[string]any,
	after map[string]any,
	diff *Diff,
	isRoot bool,
) {
	keys := make([]string, 0, len(before)+len(after))
	seen := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range after {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
		if isRoot && key == "order" {
			continue
		}
		childPath := joinPath(path, key)
		beforeValue, beforeExists := before[key]
		afterValue, afterExists := after[key]
		switch {
		case !beforeExists:
			diff.Added = append(diff.Added, Change{Path: childPath, Before: nil, After: afterValue})
		case !afterExists:
			diff.Removed = append(diff.Removed, Change{Path: childPath, Before: beforeValue, After: nil})
		default:
			compareValues(childPath, beforeValue, afterValue, diff)
		}
	}
}

func compareArrays(path string, before []any, after []any, diff *Diff) {
	shared := min(len(before), len(after))
	for index := range shared {
		compareValues(indexPath(path, index), before[index], after[index], diff)
	}
	for index := shared; index < len(before); index++ {
		diff.Removed = append(
			diff.Removed,
			Change{Path: indexPath(path, index), Before: before[index], After: nil},
		)
	}
	for index := shared; index < len(after); index++ {
		diff.Added = append(
			diff.Added,
			Change{Path: indexPath(path, index), Before: nil, After: after[index]},
		)
	}
}

func compareOrder(before []string, after []string) OrderDiff {
	diff := OrderDiff{
		Changed: !slices.Equal(before, after),
		Before:  append([]string{}, before...),
		After:   append([]string{}, after...),
		Added:   []string{},
		Removed: []string{},
		Moved:   []OrderMove{},
	}
	beforeIndex := make(map[string]int, len(before))
	afterIndex := make(map[string]int, len(after))
	for index, key := range before {
		beforeIndex[key] = index
	}
	for index, key := range after {
		afterIndex[key] = index
	}
	for _, key := range after {
		if _, exists := beforeIndex[key]; !exists {
			diff.Added = append(diff.Added, key)
		}
	}
	for _, key := range before {
		if _, exists := afterIndex[key]; !exists {
			diff.Removed = append(diff.Removed, key)
		}
	}
	for _, key := range before {
		afterPosition, exists := afterIndex[key]
		if !exists || beforeIndex[key] == afterPosition {
			continue
		}
		diff.Moved = append(diff.Moved, OrderMove{
			SectionKey: key,
			From:       beforeIndex[key],
			To:         afterPosition,
		})
	}
	return diff
}

func compareSections(before map[string]any, after map[string]any) SectionSummary {
	summary := SectionSummary{
		Added:   []string{},
		Removed: []string{},
		Changed: []string{},
	}
	keys := make([]string, 0, len(before)+len(after))
	seen := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range after {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		beforeValue, beforeExists := before[key]
		afterValue, afterExists := after[key]
		switch {
		case !beforeExists:
			summary.Added = append(summary.Added, key)
		case !afterExists:
			summary.Removed = append(summary.Removed, key)
		case !reflect.DeepEqual(beforeValue, afterValue):
			summary.Changed = append(summary.Changed, key)
		}
	}
	return summary
}

func joinPath(parent string, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func indexPath(parent string, index int) string {
	return fmt.Sprintf("%s[%d]", parent, index)
}

func sortChanges(changes []Change) {
	sort.SliceStable(changes, func(left, right int) bool {
		return changes[left].Path < changes[right].Path
	})
}

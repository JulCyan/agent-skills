package templatejson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxOrderedSections = 25

type Document struct {
	Header []byte
	Root   map[string]any
}

func Parse(data []byte) (Document, error) {
	withoutBOM := bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	header, body, err := splitHeader(withoutBOM)
	if err != nil {
		return Document{}, err
	}
	if err := ValidateUniqueObjectKeys(body); err != nil {
		return Document{}, fmt.Errorf("validating template object keys: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return Document{}, fmt.Errorf("decoding template JSON: %w", err)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return Document{}, errors.New("template root must be an object")
	}

	var extra any
	err = decoder.Decode(&extra)
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return Document{}, errors.New("template contains more than one JSON value")
		}
		return Document{}, fmt.Errorf("decoding trailing template content: %w", err)
	}

	return Document{
		Header: append([]byte{}, header...),
		Root:   root,
	}, nil
}

func splitHeader(data []byte) ([]byte, []byte, error) {
	position := 0
	for {
		for position < len(data) && isJSONWhitespace(data[position]) {
			position++
		}
		if position >= len(data) {
			return nil, nil, errors.New("template JSON is empty")
		}
		if position+1 >= len(data) || data[position] != '/' {
			break
		}

		switch data[position+1] {
		case '*':
			end := bytes.Index(data[position+2:], []byte("*/"))
			if end < 0 {
				return nil, nil, errors.New("unterminated leading block comment")
			}
			position += end + 4
		case '/':
			end := bytes.IndexByte(data[position+2:], '\n')
			if end < 0 {
				return nil, nil, errors.New("template JSON is empty after leading comment")
			}
			position += end + 3
		default:
			return append([]byte{}, data[:position]...), data[position:], nil
		}
	}

	return append([]byte{}, data[:position]...), data[position:], nil
}

func isJSONWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

func (d Document) Validate() error {
	if d.Root == nil {
		return errors.New("template root is missing")
	}
	if _, ok := d.Root["sections"].(map[string]any); !ok {
		return errors.New("template sections must be an object")
	}
	order, ok := d.Root["order"].([]any)
	if !ok {
		return errors.New("template order must be a string array")
	}
	if len(order) > maxOrderedSections {
		return fmt.Errorf("template order has %d entries; maximum is %d", len(order), maxOrderedSections)
	}

	seen := make(map[string]struct{}, len(order))
	for index, item := range order {
		key, ok := item.(string)
		if !ok {
			return fmt.Errorf("template order item %d must be a string", index)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("template order contains duplicate section %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

package templatejson

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func (d Document) Clone() (Document, error) {
	canonical, err := d.Canonical()
	if err != nil {
		return Document{}, fmt.Errorf("encoding document clone: %w", err)
	}
	clone, err := Parse(canonical)
	if err != nil {
		return Document{}, fmt.Errorf("decoding document clone: %w", err)
	}
	clone.Header = append([]byte{}, d.Header...)
	return clone, nil
}

func (d Document) Canonical() ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(d.Root); err != nil {
		return nil, fmt.Errorf("encoding canonical template: %w", err)
	}
	return output.Bytes(), nil
}

func (d Document) Render() ([]byte, error) {
	canonical, err := d.Canonical()
	if err != nil {
		return nil, err
	}
	output := make([]byte, 0, len(d.Header)+len(canonical))
	output = append(output, d.Header...)
	output = append(output, canonical...)
	return output, nil
}

func (d Document) CanonicalSHA256() (string, error) {
	canonical, err := d.Canonical()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

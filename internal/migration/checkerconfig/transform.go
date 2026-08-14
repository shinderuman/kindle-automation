package checkerconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func applyMigration(body []byte) ([]byte, report, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, report{}, fmt.Errorf("decode top: %w", err)
	}
	rep := report{}
	for _, field := range topLevelRemove {
		if _, ok := top[field]; ok {
			delete(top, field)
			rep.RemovedTopLevel++
		}
	}
	for checker, fields := range perCheckerRemove {
		raw, ok := top[checker]
		if !ok {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, report{}, fmt.Errorf("decode %s: %w", checker, err)
		}
		changed := false
		for _, field := range fields {
			if _, ok := m[field]; ok {
				delete(m, field)
				rep.RemovedPerChecker++
				changed = true
			}
		}
		if checker == "NewReleaseChecker" {
			if added, err := ensureMinPrice(m); err != nil {
				return nil, report{}, err
			} else if added {
				rep.MinPriceAdded = true
				changed = true
			}
		}
		if changed {
			enc, err := json.Marshal(m)
			if err != nil {
				return nil, report{}, fmt.Errorf("encode %s: %w", checker, err)
			}
			top[checker] = enc
		}
	}
	normalized := make(map[string]any, len(top))
	for section, raw := range top {
		v, err := decodeSection(raw)
		if err != nil {
			return nil, report{}, fmt.Errorf("decode %s: %w", section, err)
		}
		normalized[section] = v
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "    ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalized); err != nil {
		return nil, report{}, fmt.Errorf("encode top: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), rep, nil
}

func ensureMinPrice(m map[string]json.RawMessage) (bool, error) {
	if _, ok := m["MinPrice"]; ok {
		return false, nil
	}
	enc, err := json.Marshal(defaultMinPrice)
	if err != nil {
		return false, fmt.Errorf("encode MinPrice: %w", err)
	}
	m["MinPrice"] = enc
	return true, nil
}

func decodeSection(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

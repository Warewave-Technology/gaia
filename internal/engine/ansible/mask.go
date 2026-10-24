package ansible

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// Masked replaces a secret value in events.
const Masked = "********"

// minSecret is the shortest value masked: shorter ones would mask
// ordinary words.
const minSecret = 6

// masker replaces secret values, and each long line of multi-line ones
// (keys), in text and in the strings of JSON.
type masker struct {
	r *strings.Replacer
}

func newMasker(secrets []string) *masker {
	seen := map[string]bool{}
	var vals []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if len(s) >= minSecret && !seen[s] {
			seen[s] = true
			vals = append(vals, s)
		}
	}
	for _, s := range secrets {
		add(s)
		if strings.Contains(s, "\n") {
			for _, l := range strings.Split(s, "\n") {
				if len(strings.TrimSpace(l)) >= 16 {
					add(l)
				}
			}
		}
	}
	// Longest first, so that a value is masked whole before its parts.
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	var pairs []string
	for _, v := range vals {
		pairs = append(pairs, v, Masked)
	}
	return &masker{r: strings.NewReplacer(pairs...)}
}

func (m *masker) mask(s string) string { return m.r.Replace(s) }

// maskJSON masks secrets in every string of a JSON value, keys included.
// The value is decoded first: Python escapes non-ASCII characters, so the
// raw text would not match. JSON that does not decode is dropped.
func (m *masker) maskJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return json.RawMessage(`{"invalid":true}`)
	}
	out, err := json.Marshal(m.walk(v))
	if err != nil {
		return json.RawMessage(`{"invalid":true}`)
	}
	return out
}

func (m *masker) walk(v any) any {
	switch x := v.(type) {
	case string:
		return m.mask(x)
	case []any:
		for i := range x {
			x[i] = m.walk(x[i])
		}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[m.mask(k)] = m.walk(e)
		}
		return out
	}
	return v
}

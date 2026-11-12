// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package configfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSON: nested objects. The file is read token by token into a tree that keeps its keys in the
// order the file had them (a Go map would shuffle them), and written back indented with four
// spaces. Numbers keep the text they had.

type jsonNode struct {
	kind byte // 'o' an object, 'a' an array, 's' anything else
	keys []string
	vals []*jsonNode
	raw  string // a scalar's JSON text
}

// maxJSONDepth keeps a hostile file from nesting without end.
const maxJSONDepth = 256

func parseJSON(data []byte) (*jsonNode, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return &jsonNode{kind: 'o'}, nil
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	n, err := jsonValue(d, 0)
	if err != nil {
		return nil, fmt.Errorf("configfile: %v", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("configfile: something after the JSON")
	}
	if n.kind != 'o' {
		return nil, errors.New("configfile: the JSON isn't an object")
	}
	return n, nil
}

func jsonValue(d *json.Decoder, depth int) (*jsonNode, error) {
	if depth > maxJSONDepth {
		return nil, errors.New("nested too deep")
	}
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &jsonNode{kind: 'o'}
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("an object key that isn't a string")
				}
				v, err := jsonValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				n.keys, n.vals = append(n.keys, k), append(n.vals, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			return n, nil
		case '[':
			n := &jsonNode{kind: 'a'}
			for d.More() {
				v, err := jsonValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				n.vals = append(n.vals, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			return n, nil
		}
		return nil, errors.New("a stray bracket")
	case string:
		return &jsonNode{kind: 's', raw: jsonString(t)}, nil
	case json.Number:
		return &jsonNode{kind: 's', raw: t.String()}, nil
	case bool:
		return &jsonNode{kind: 's', raw: strconv.FormatBool(t)}, nil
	case nil:
		return &jsonNode{kind: 's', raw: "null"}, nil
	}
	return nil, errors.New("a token this doesn't read")
}

func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func (n *jsonNode) write(b *strings.Builder, indent string) {
	switch n.kind {
	case 's':
		b.WriteString(n.raw)
	case 'o', 'a':
		open, close := "{", "}"
		if n.kind == 'a' {
			open, close = "[", "]"
		}
		if len(n.vals) == 0 {
			b.WriteString(open + close)
			return
		}
		b.WriteString(open + "\n")
		inner := indent + "    "
		for i, v := range n.vals {
			b.WriteString(inner)
			if n.kind == 'o' {
				b.WriteString(jsonString(n.keys[i]) + ": ")
			}
			v.write(b, inner)
			if i < len(n.vals)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + close)
	}
}

func jsonText(v any) string {
	switch t := v.(type) {
	case bool:
		return strconv.FormatBool(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return floatText(t)
	case string:
		return jsonString(t)
	}
	return "null"
}

func applyJSON(old []byte, edits []Edit) ([]byte, error) {
	root, err := parseJSON(old)
	if err != nil {
		return nil, err
	}
	for _, e := range edits {
		n := root
		for i, k := range e.Path {
			at := -1
			for j, key := range n.keys {
				if key == k {
					at = j
					break
				}
			}
			last := i == len(e.Path)-1
			if at < 0 {
				child := &jsonNode{kind: 'o'}
				if last {
					child = &jsonNode{kind: 's'}
				}
				n.keys, n.vals = append(n.keys, k), append(n.vals, child)
				at = len(n.vals) - 1
			}
			child := n.vals[at]
			if last && isComposite(e.Value) {
				n.vals[at] = jsonNodeOf(e.Value)
				break
			}
			if last {
				if child.kind != 's' {
					return nil, fmt.Errorf("configfile: %s holds an object or a list", strings.Join(e.Path, "."))
				}
				child.raw = jsonText(e.Value)
				break
			}
			if child.kind != 'o' {
				return nil, fmt.Errorf("configfile: %s runs through a value", strings.Join(e.Path, "."))
			}
			n = child
		}
	}
	var b strings.Builder
	root.write(&b, "")
	b.WriteByte('\n')
	return []byte(b.String()), nil
}

func entriesJSON(data []byte) ([]Entry, error) {
	root, err := parseJSON(data)
	if err != nil {
		return nil, err
	}
	var out []Entry
	var walk func(n *jsonNode, path []string)
	walk = func(n *jsonNode, path []string) {
		for i, v := range n.vals {
			p := append(clonePath(path), n.keys[i])
			switch v.kind {
			case 'o':
				walk(v, p)
			case 'a':
				var b strings.Builder
				v.write(&b, "")
				var c bytes.Buffer
				if json.Compact(&c, []byte(b.String())) == nil {
					out = append(out, Entry{Path: p, Value: c.String()})
				}
			default:
				if val := jsonScalar(v.raw); val != nil {
					out = append(out, Entry{Path: p, Value: val})
				}
			}
		}
	}
	walk(root, nil)
	return out, nil
}

func jsonScalar(raw string) any {
	switch {
	case raw == "true":
		return true
	case raw == "false":
		return false
	case raw == "null":
		return nil
	case strings.HasPrefix(raw, `"`):
		var s string
		if json.Unmarshal([]byte(raw), &s) == nil {
			return s
		}
		return nil
	case !strings.ContainsAny(raw, ".eE"):
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n
		}
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return f
	}
	return raw
}

// isComposite says whether a value is a list or an object, which only JSON holds as one value.
func isComposite(v any) bool {
	switch v.(type) {
	case []any, map[string]any:
		return true
	}
	return false
}

// checkJSONValue holds a list or an object to what jsonNodeOf can write: the scalars an edit
// may be, lists and objects of them, nested no deeper than a file this reads may be.
func checkJSONValue(v any, depth int) error {
	if depth > 32 {
		return errors.New("configfile: a value nested that deep")
	}
	switch t := v.(type) {
	case bool, int64, int:
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return errors.New("configfile: a number that isn't one")
		}
	case string:
		if !utf8.ValidString(t) {
			return errors.New("configfile: text that isn't UTF-8")
		}
	case []any:
		for _, x := range t {
			if err := checkJSONValue(x, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, x := range t {
			if !utf8.ValidString(k) {
				return errors.New("configfile: a key that isn't UTF-8")
			}
			if err := checkJSONValue(x, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("configfile: a value of type %T", v)
	}
	return nil
}

// jsonNodeOf makes a value into a node to write: an object's keys in order, so the same value
// always writes the same text.
func jsonNodeOf(v any) *jsonNode {
	switch t := v.(type) {
	case []any:
		n := &jsonNode{kind: 'a'}
		for _, x := range t {
			n.vals = append(n.vals, jsonNodeOf(x))
		}
		return n
	case map[string]any:
		n := &jsonNode{kind: 'o'}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			n.keys, n.vals = append(n.keys, k), append(n.vals, jsonNodeOf(t[k]))
		}
		return n
	case int:
		return &jsonNode{kind: 's', raw: strconv.Itoa(t)}
	}
	return &jsonNode{kind: 's', raw: jsonText(v)}
}

// jsonHolds says whether the file holds a list or an object at path, equal to v.
func jsonHolds(data []byte, path []string, v any) bool {
	root, err := parseJSON(data)
	if err != nil {
		return false
	}
	n := root
	for _, k := range path {
		i := slices.Index(n.keys, k)
		if n.kind != 'o' || i < 0 {
			return false
		}
		n = n.vals[i]
	}
	var got strings.Builder
	n.write(&got, "")
	var want strings.Builder
	jsonNodeOf(v).write(&want, "")
	var a, b any
	if json.Unmarshal([]byte(got.String()), &a) != nil || json.Unmarshal([]byte(want.String()), &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package configfile

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// XML, as Cemu writes its settings.xml: one root element, settings as elements whose text is
// the value, grouped in elements of their own. The file is read with encoding/xml to find where
// each element's text sits, and only those bytes change; attributes, comments and layout stay.

type xmlElem struct {
	path         []string
	start        int // the start tag's "<"
	contentStart int // just after the start tag
	contentEnd   int // the end tag's "<" (for <a/>, the same as contentStart)
	selfClosing  bool
	children     bool
	text         strings.Builder
}

// maxXMLDepth keeps a hostile file from nesting without end.
const maxXMLDepth = 64

var xmlName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9._-]*$`)

func scanXML(data []byte) ([]*xmlElem, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	var all, stack []*xmlElem
	for {
		before := int(d.InputOffset())
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("configfile: %v", err)
		}
		after := int(d.InputOffset())
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) >= maxXMLDepth {
				return nil, errors.New("configfile: elements nested too deep")
			}
			var path []string
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.children = true
				path = clonePath(top.path)
			}
			e := &xmlElem{path: append(path, t.Name.Local), start: before, contentStart: after}
			all = append(all, e)
			stack = append(stack, e)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("configfile: an end tag with no start")
			}
			e := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			e.contentEnd = before
			e.selfClosing = after == before
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		}
	}
	if len(stack) > 0 {
		return nil, errors.New("configfile: an element that doesn't close")
	}
	return all, nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// lineIndent is the run of spaces and tabs before position at, if only those stand between it
// and the start of its line.
func lineIndent(data []byte, at int) string {
	i := at
	for i > 0 && (data[i-1] == ' ' || data[i-1] == '\t') {
		i--
	}
	if i > 0 && data[i-1] != '\n' {
		return ""
	}
	return string(data[i:at])
}

func applyXML(old []byte, edits []Edit) ([]byte, error) {
	for _, e := range edits {
		for _, p := range e.Path {
			if !xmlName.MatchString(p) {
				return nil, fmt.Errorf("configfile: %q isn't an element name this writes", p)
			}
		}
	}
	data := old
	for _, e := range edits {
		var err error
		if data, err = setXML(data, e); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func setXML(data []byte, e Edit) ([]byte, error) {
	elems, err := scanXML(data)
	if err != nil {
		return nil, err
	}
	want := e.Path
	name := strings.Join(want, "/")
	text := xmlEscape(plainText(e.Value))
	nl := "\n"
	if lines := bytes.Count(data, []byte("\n")); lines > 0 && bytes.Count(data, []byte("\r\n")) == lines {
		nl = "\r\n"
	}
	splice := func(from, to int, with string) []byte {
		out := make([]byte, 0, len(data)+len(with))
		out = append(out, data[:from]...)
		out = append(out, with...)
		return append(out, data[to:]...)
	}
	var parent *xmlElem
	for _, el := range elems {
		if samePath(el.path, want) {
			if el.children {
				return nil, fmt.Errorf("configfile: %s holds other elements", name)
			}
			if el.selfClosing {
				tag := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(data[el.start:el.contentStart])), "/>"))
				raw := strings.TrimPrefix(strings.Fields(tag)[0], "<")
				return splice(el.start, el.contentStart, tag+">"+text+"</"+raw+">"), nil
			}
			return splice(el.contentStart, el.contentEnd, text), nil
		}
		if len(el.path) < len(want) && hasPrefix(want, el.path) && (parent == nil || len(el.path) > len(parent.path)) {
			parent = el
		}
	}
	unit := "    "
	if parent == nil {
		if len(elems) > 0 {
			return nil, fmt.Errorf("configfile: the file's root element isn't %s", want[0])
		}
		var b strings.Builder
		if len(bytes.TrimSpace(data)) == 0 {
			b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + nl)
		} else {
			b.Write(bytes.TrimRight(data, " \t\r\n"))
			b.WriteString(nl)
		}
		b.WriteString(xmlNest(want, text, "", unit, nl))
		b.WriteString(nl)
		return []byte(b.String()), nil
	}
	if strings.TrimSpace(parent.text.String()) != "" && !parent.children {
		return nil, fmt.Errorf("configfile: %s holds text, not elements", strings.Join(parent.path, "/"))
	}
	// Take the indentation from the file: the parent's own, and one step more for its children.
	pInd := lineIndent(data, parent.start)
	cInd := pInd + unit
	for _, el := range elems {
		if len(el.path) == len(parent.path)+1 && hasPrefix(el.path, parent.path) && el.start > parent.start {
			if ind := lineIndent(data, el.start); len(ind) > len(pInd) {
				cInd = ind
			}
			break
		}
	}
	if len(cInd) > len(pInd) {
		unit = cInd[len(pInd):]
	}
	add := nl + xmlNest(want[len(parent.path):], text, cInd, unit, nl)
	if parent.selfClosing {
		tag := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(data[parent.start:parent.contentStart])), "/>"))
		raw := strings.TrimPrefix(strings.Fields(tag)[0], "<")
		return splice(parent.start, parent.contentStart, tag+">"+add+nl+pInd+"</"+raw+">"), nil
	}
	if !parent.children {
		// Only blank space inside: it's replaced, so the close tag lands on its own line.
		return splice(parent.contentStart, parent.contentEnd, add+nl+pInd), nil
	}
	at := parent.contentEnd
	for at > parent.contentStart && strings.ContainsRune(" \t\r\n", rune(data[at-1])) {
		at--
	}
	return splice(at, at, add), nil
}

// xmlNest writes elements nested along path with the text in the last.
func xmlNest(path []string, text, indent, unit, nl string) string {
	if len(path) == 1 {
		return indent + "<" + path[0] + ">" + text + "</" + path[0] + ">"
	}
	return indent + "<" + path[0] + ">" + nl + xmlNest(path[1:], text, indent+unit, unit, nl) + nl + indent + "</" + path[0] + ">"
}

func entriesXML(data []byte) ([]Entry, error) {
	elems, err := scanXML(data)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, el := range elems {
		if !el.children {
			out = append(out, Entry{Path: el.path, Value: el.text.String()})
		}
	}
	return out, nil
}

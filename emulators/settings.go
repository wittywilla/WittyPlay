// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/wittywilla/wittyplay/emulators/configfile"
)

// Settings for an emulator, as data. A page draws its form from a list of Setting, the module
// checks what comes back against the same list, and Apply says how each value reaches the
// emulator: a line in one of its own settings files, a variable, or words on its command line.
//
// Values are layered: the default in the list, then what an administrator set for everyone,
// then what one person set for themselves, then what that person set for one game. A setting
// whose Scope is "admin" is the administrator's alone; the other layers can't change it.

// Scopes of a setting, and of a write.
const (
	ScopeAdmin = "admin"
	ScopeUser  = "user"
	ScopeGame  = "game"
)

// Kinds of setting.
const (
	KindToggle = "toggle"
	KindNumber = "number"
	KindText   = "text"
	KindChoice = "choice"
	KindMap    = "map"
)

// Choice is one value a setting may take, and what a page calls it.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Setting describes one setting.
type Setting struct {
	Key     string   `json:"key"`
	Kind    string   `json:"kind"`
	Label   string   `json:"label"`
	Help    string   `json:"help,omitempty"`
	Default any      `json:"default"`
	Min     float64  `json:"min,omitempty"`
	Max     float64  `json:"max,omitempty"`
	Step    float64  `json:"step,omitempty"`
	Choices []Choice `json:"choices,omitempty"`
	// Scope is who may set it: "admin", or "user" for each person (and each of their games).
	Scope string `json:"scope"`
	// MapOf and MapKeys describe a map's values and the keys it may have.
	MapOf   string   `json:"mapOf,omitempty"`
	MapKeys []Choice `json:"mapKeys,omitempty"`
	// Fixed: it's written as its default every time and never offered: where the sandbox
	// puts the BIOS folder, say.
	Fixed bool `json:"fixed,omitempty"`
	// Apply is how the value reaches the emulator. A setting without one is used by WittyPlay
	// itself (the stream's size, the keys).
	Apply *Apply `json:"apply,omitempty"`
	// Group is the heading a page lists it under: the emulator's own section, in words.
	Group string `json:"group,omitempty"`
	// Advanced settings are ones few people need (debugging, odd hardware): a page may tuck
	// them away.
	Advanced bool `json:"advanced,omitempty"`
}

// Apply is where a value goes. Exactly one of its parts is set.
type Apply struct {
	INI *INITarget `json:"ini,omitempty"`
	// File is a value in one of the emulator's settings files in any format configfile
	// knows: INI, TOML, YAML, XML or JSON.
	File *FileTarget `json:"file,omitempty"`
	// Env is a variable's name. It may not be one the sandbox sets.
	Env string `json:"env,omitempty"`
	// Args are added before the game's path; "{value}" in one is replaced by the value. For a
	// toggle they're added only when it's on.
	Args []string `json:"args,omitempty"`
}

// INITarget is a key in an INI file under the emulator's settings folder.
type INITarget struct {
	File    string `json:"file"` // relative to the settings folder, with forward slashes
	Section string `json:"section"`
	Key     string `json:"key"`
	// Values translates a setting's value to what the file says. Without one the value is
	// written as it is.
	Values map[string]string `json:"values,omitempty"`
}

// FileTarget is a value in a settings file under the emulator's settings folder.
type FileTarget struct {
	File   string `json:"file"`   // relative to the settings folder, with forward slashes
	Format string `json:"format"` // ini, toml, yaml, xml or json
	// Path names the value in the file: see configfile.Apply.
	Path []string `json:"path"`
	// Values translates a setting's value to what the file says, as INITarget's does.
	Values map[string]string `json:"values,omitempty"`
	// Type is how the file holds the value where its format tells types apart (TOML, YAML,
	// JSON): bool, int, float or string. Without one it's worked out from the setting: a
	// toggle a bool, a number by its step, a choice by what its value reads as.
	Type string `json:"type,omitempty"`
}

var (
	settingKey = regexp.MustCompile(`^[a-z][A-Za-z0-9.]{0,47}$`)
	iniName    = regexp.MustCompile(`^[A-Za-z0-9_. /:-]{1,64}$`)
	envName    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// checkSettings holds a list of settings valid, as part of the manifest's own check.
func checkSettings(list []Setting) error {
	seen := map[string]bool{}
	for _, s := range list {
		bad := func(format string, a ...any) error {
			return fmt.Errorf("setting %q: %s", s.Key, fmt.Sprintf(format, a...))
		}
		if !settingKey.MatchString(s.Key) || seen[s.Key] {
			return bad("the key isn't a plain name, or is listed twice")
		}
		seen[s.Key] = true
		if s.Label == "" {
			return bad("needs a label")
		}
		if s.Scope != ScopeAdmin && s.Scope != ScopeUser {
			return bad("scope %q is neither admin nor user", s.Scope)
		}
		switch s.Kind {
		case KindChoice:
			if len(s.Choices) == 0 {
				return bad("a choice needs choices")
			}
		case KindMap:
			if !slices.Contains([]string{KindToggle, KindChoice, KindText}, s.MapOf) || len(s.MapKeys) == 0 {
				return bad("a map needs mapOf and mapKeys")
			}
			if s.Apply != nil {
				return bad("a map can't be written to an emulator's files")
			}
		case KindToggle, KindNumber, KindText:
		default:
			return bad("kind %q is none this reads", s.Kind)
		}
		if _, err := s.Clean(s.Default); err != nil {
			return bad("its default isn't one of its own values: %v", err)
		}
		if a := s.Apply; a != nil {
			n := 0
			if a.INI != nil {
				n++
				f := a.INI.File
				if f == "" || strings.Contains(f, "\\") || path.Clean(f) != f || !filepath.IsLocal(filepath.FromSlash(f)) {
					return bad("the file %q isn't inside the settings folder", f)
				}
				if !iniName.MatchString(a.INI.Section) || !iniName.MatchString(a.INI.Key) || strings.ContainsAny(a.INI.Key, "=[]") {
					return bad("a bad section or key")
				}
				for _, v := range a.INI.Values {
					if strings.ContainsAny(v, "\r\n") {
						return bad("a value with a line break")
					}
				}
			}
			if f := a.File; f != nil {
				n++
				if err := checkFileTarget(f); err != nil {
					return bad("%v", err)
				}
			}
			if a.Env != "" {
				n++
				if !envName.MatchString(a.Env) || strings.HasPrefix(a.Env, "WITTYPLAY_") || strings.HasPrefix(a.Env, "LD_") ||
					slices.Contains([]string{"PATH", "HOME", "USER", "DISPLAY", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR"}, a.Env) {
					return bad("the variable %q can't be set", a.Env)
				}
			}
			if len(a.Args) > 0 {
				n++
			}
			if n != 1 {
				return bad("apply needs exactly one of ini, file, env and args")
			}
		} else if s.Fixed {
			return bad("a fixed setting with nowhere to go")
		}
	}
	return nil
}

// checkFileTarget holds a file target to what WriteFiles can write: a file inside the settings
// folder, a format configfile knows, and a path of short printable names that the format can
// hold.
func checkFileTarget(f *FileTarget) error {
	if f.File == "" || strings.Contains(f.File, "\\") || path.Clean(f.File) != f.File || !filepath.IsLocal(filepath.FromSlash(f.File)) || !iniName.MatchString(f.File) {
		return fmt.Errorf("the file %q isn't inside the settings folder", f.File)
	}
	if !configfile.Known(f.Format) {
		return fmt.Errorf("no format %q", f.Format)
	}
	if len(f.Path) == 0 {
		return errors.New("a file target needs a path")
	}
	for _, p := range f.Path {
		if p == "" || len(p) > 128 || !utf8.ValidString(p) || strings.IndexFunc(p, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			return fmt.Errorf("the name %q isn't up to 128 printable characters", p)
		}
	}
	for _, v := range f.Values {
		if strings.ContainsAny(v, "\r\n\x00") {
			return errors.New("a value with a line break")
		}
	}
	switch f.Type {
	case "", "bool", "int", "float", "string":
	default:
		return fmt.Errorf("no type %q", f.Type)
	}
	// The format's own rules (an INI path is a section and a key, an XML name is a name): an
	// edit of an empty file says whether the path can be written at all.
	if _, err := configfile.Apply(f.Format, nil, []configfile.Edit{{Path: f.Path, Value: ""}}); err != nil {
		return err
	}
	return nil
}

// Clean checks one value against its setting and returns it in the form it's kept in: a bool,
// a float64, a string, or a map of those.
func (s Setting) Clean(v any) (any, error) {
	switch s.Kind {
	case KindToggle:
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("is on or off")
		}
		return b, nil
	case KindNumber:
		var f float64
		switch n := v.(type) {
		case float64:
			f = n
		case int:
			f = float64(n)
		case int64:
			f = float64(n)
		default:
			return nil, errors.New("is a number")
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("is a number")
		}
		if s.Min != 0 || s.Max != 0 {
			if f < s.Min || f > s.Max {
				return nil, fmt.Errorf("is between %v and %v", s.Min, s.Max)
			}
		}
		return f, nil
	case KindText:
		t, ok := v.(string)
		if !ok || len(t) > 256 || strings.ContainsAny(t, "\r\n\x00") {
			return nil, errors.New("is one line of up to 256 characters")
		}
		return t, nil
	case KindChoice:
		t, ok := v.(string)
		if !ok || !slices.ContainsFunc(s.Choices, func(c Choice) bool { return c.Value == t }) {
			return nil, fmt.Errorf("is one of %s", choiceList(s.Choices))
		}
		return t, nil
	case KindMap:
		in, ok := v.(map[string]any)
		if !ok {
			if v == nil {
				return map[string]any{}, nil
			}
			return nil, errors.New("is a set of named values")
		}
		out := map[string]any{}
		inner := Setting{Kind: s.MapOf, Choices: s.Choices}
		for k, val := range in {
			if !slices.ContainsFunc(s.MapKeys, func(c Choice) bool { return c.Value == k }) {
				return nil, fmt.Errorf("has no %q", k)
			}
			c, err := inner.Clean(val)
			if err != nil {
				return nil, fmt.Errorf("%s %v", k, err)
			}
			out[k] = c
		}
		return out, nil
	}
	return nil, errors.New("can't be set")
}

func choiceList(cs []Choice) string {
	v := make([]string, len(cs))
	for i, c := range cs {
		v[i] = c.Value
	}
	return strings.Join(v, ", ")
}

// Offered is a list without its fixed settings: what a page shows.
func Offered(list []Setting) []Setting {
	out := make([]Setting, 0, len(list))
	for _, s := range list {
		if !s.Fixed {
			out = append(out, s)
		}
	}
	return out
}

// Values are settings' values by key.
type Values map[string]any

// CleanValues checks a write to one layer. A nil value clears a key. Only an administrator's
// layer may hold a setting whose scope is admin.
func CleanValues(list []Setting, scope string, in Values) (Values, error) {
	out := Values{}
	for k, v := range in {
		i := slices.IndexFunc(list, func(s Setting) bool { return s.Key == k && !s.Fixed })
		if i < 0 {
			return nil, fmt.Errorf("there's no setting named %q", k)
		}
		s := list[i]
		if scope != ScopeAdmin && s.Scope == ScopeAdmin {
			return nil, &ScopeError{Label: s.Label}
		}
		if v == nil {
			out[k] = nil
			continue
		}
		c, err := s.Clean(v)
		if err != nil {
			return nil, fmt.Errorf("%s %v", s.Label, err)
		}
		out[k] = c
	}
	return out, nil
}

// ScopeError is someone setting what only an administrator may.
type ScopeError struct{ Label string }

func (e *ScopeError) Error() string {
	return "only an administrator can change " + e.Label
}

// Effective layers values over the defaults: admin, then user, then game. A value that no
// longer fits its setting (the list changed) is passed over, and a layer below the
// administrator's never changes an admin-only setting, whatever a file says.
func Effective(list []Setting, admin, user, game Values) Values {
	out := Values{}
	for _, s := range list {
		out[s.Key] = s.Default
		if s.Fixed {
			continue
		}
		for i, layer := range []Values{admin, user, game} {
			if i > 0 && s.Scope == ScopeAdmin {
				break
			}
			if v, ok := layer[s.Key]; ok && v != nil {
				if c, err := s.Clean(v); err == nil {
					if s.Kind == KindMap {
						merged := map[string]any{}
						if prev, ok := out[s.Key].(map[string]any); ok {
							for k, v := range prev {
								merged[k] = v
							}
						}
						for k, v := range c.(map[string]any) {
							merged[k] = v
						}
						c = merged
					}
					out[s.Key] = c
				}
			}
		}
	}
	return out
}

// text is a value as an emulator reads it.
func text(v any) string {
	switch t := v.(type) {
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case string:
		return t
	}
	return ""
}

// Applied is what a set of values comes to for one start of an emulator.
type Applied struct {
	Env  []string
	Args []string
	// Files are the INI edits from INI targets, by file: section, key, value.
	Files map[string][]INIEdit
	// Edits are the edits from file targets, by file, in that file's format. A file may have
	// both INI edits and edits here when its format is INI.
	Edits map[string]FileEdits
}

// FileEdits are the edits to one settings file.
type FileEdits struct {
	Format string
	Edits  []configfile.Edit
}

// INIEdit sets one key.
type INIEdit struct{ Section, Key, Value string }

// Plan works out what the effective values mean for a start. It writes nothing.
func Plan(list []Setting, eff Values) Applied {
	a := Applied{Files: map[string][]INIEdit{}, Edits: map[string]FileEdits{}}
	for _, s := range list {
		v, ok := eff[s.Key]
		if !ok || s.Apply == nil {
			continue
		}
		val := text(v)
		switch {
		case s.Apply.INI != nil:
			t := s.Apply.INI
			if t.Values != nil {
				mapped, ok := t.Values[val]
				if !ok {
					continue
				}
				val = mapped
			}
			a.Files[t.File] = append(a.Files[t.File], INIEdit{t.Section, t.Key, val})
		case s.Apply.File != nil:
			t := s.Apply.File
			fe := a.Edits[t.File]
			if fe.Format != "" && fe.Format != t.Format {
				continue // one file has one format; a list that says otherwise is wrong
			}
			var typed any
			if t.Values != nil {
				mapped, ok := t.Values[val]
				if !ok {
					continue
				}
				typed = literal(mapped)
			} else {
				typed = fileValue(s, v)
			}
			if t.Type != "" {
				typed = asType(t.Type, typed)
			}
			fe.Format = t.Format
			fe.Edits = append(fe.Edits, configfile.Edit{Path: slices.Clone(t.Path), Value: typed})
			a.Edits[t.File] = fe
		case s.Apply.Env != "":
			a.Env = append(a.Env, s.Apply.Env+"="+val)
		case len(s.Apply.Args) > 0:
			if b, isToggle := v.(bool); isToggle && !b {
				continue
			}
			for _, arg := range s.Apply.Args {
				a.Args = append(a.Args, strings.ReplaceAll(arg, "{value}", val))
			}
		}
	}
	return a
}

// fileValue is a setting's value as a file holds it: a toggle a bool, a number an int64 when
// the setting steps in whole numbers and a float64 otherwise (TOML, YAML and JSON tell 2 from
// 2.0, and an emulator reading one may refuse the other), a choice what its value reads as,
// text a string.
func fileValue(s Setting, v any) any {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		if s.Step >= 1 && s.Step == math.Trunc(s.Step) && t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t)
		}
		return t
	case string:
		if s.Kind == KindChoice {
			return literal(t)
		}
		return t
	}
	return text(v)
}

// asType makes a value the type a file target says the file holds.
func asType(typ string, v any) any {
	t := text(v)
	if n, ok := v.(int64); ok {
		t = strconv.FormatInt(n, 10)
	}
	switch typ {
	case "string":
		return t
	case "bool":
		if b, err := strconv.ParseBool(t); err == nil {
			return b
		}
	case "int":
		if f, err := strconv.ParseFloat(t, 64); err == nil && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return int64(f)
		}
	case "float":
		if f, err := strconv.ParseFloat(t, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return f
		}
	}
	return v
}

// literal reads a choice's value, or what Values maps a value to, as the file should hold it:
// "true" a bool, "2" a number, anything else a string. In an INI or XML file it's all text
// again.
func literal(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && strconv.FormatInt(n, 10) == s {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(f, 0) && strings.Contains(s, ".") && !strings.ContainsAny(s, "xXpP_") {
		return f
	}
	return s
}

// maxINI is the largest settings file this will edit. An emulator's own is a few kilobytes.
const maxINI = 4 << 20

// WriteFiles makes the edits under dir, the emulator's settings folder on this machine, each
// file in its own format.
//
// The emulator can write to that folder too, and an emulator is treated as something that may
// have been taken over: it could leave a link there pointing anywhere. So every read and write
// goes through os.Root, which refuses to follow anything out of dir.
func (a Applied) WriteFiles(dir string) error {
	files := map[string]FileEdits{}
	for name, edits := range a.Files {
		fe := files[name]
		fe.Format = "ini"
		for _, e := range edits {
			fe.Edits = append(fe.Edits, configfile.Edit{Path: []string{e.Section, e.Key}, Value: e.Value})
		}
		files[name] = fe
	}
	for name, fe := range a.Edits {
		if len(fe.Edits) == 0 {
			continue
		}
		have := files[name]
		if have.Format != "" && have.Format != fe.Format {
			return fmt.Errorf("the settings file %s is written as both %s and %s", name, have.Format, fe.Format)
		}
		have.Format = fe.Format
		have.Edits = append(have.Edits, fe.Edits...)
		files[name] = have
	}
	if len(files) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		local := filepath.FromSlash(name)
		if !filepath.IsLocal(local) {
			return fmt.Errorf("the settings file %q isn't inside the settings folder", name)
		}
		if d := filepath.Dir(local); d != "." {
			if err := root.MkdirAll(d, 0o700); err != nil {
				return fmt.Errorf("the settings folder for %s: %w", name, err)
			}
		}
		var old []byte
		if st, err := root.Lstat(local); err == nil {
			if !st.Mode().IsRegular() {
				return fmt.Errorf("the settings file %s isn't a plain file", name)
			}
			if st.Size() > maxINI {
				return fmt.Errorf("the settings file %s is too large to edit", name)
			}
			if old, err = root.ReadFile(local); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fe := files[name]
		out, err := configfile.Apply(fe.Format, old, fe.Edits)
		if err != nil {
			// One value the file holds in a shape this won't rewrite (a list where a schema
			// expects one value, say) mustn't stop every other setting: try them one by one
			// and leave out the ones refused. When none can be made, the file itself is one
			// this can't edit safely (perhaps the emulator, or something that took it over,
			// wrote it that way): say so rather than write over the person's own.
			out = old
			applied := 0
			for _, e := range fe.Edits {
				if next, err := configfile.Apply(fe.Format, out, []configfile.Edit{e}); err == nil {
					out = next
					applied++
				}
			}
			if applied == 0 {
				return fmt.Errorf("the settings file %s: %w", name, err)
			}
		}
		tmp := local + ".wittyplay-tmp"
		_ = root.Remove(tmp)
		if err := root.WriteFile(tmp, out, 0o600); err != nil {
			return err
		}
		if err := root.Rename(tmp, local); err != nil {
			_ = root.Remove(tmp)
			return err
		}
	}
	return nil
}

// EditINI sets keys in an INI file's text and leaves everything else as it was: comments, other
// keys, their order. A section or key that isn't there is added. It's configfile's INI editor;
// an edit that one refuses (a key with "=" in it, a value with a line break) is left out.
func EditINI(old []byte, edits []INIEdit) []byte {
	conv := make([]configfile.Edit, 0, len(edits))
	for _, e := range edits {
		conv = append(conv, configfile.Edit{Path: []string{e.Section, e.Key}, Value: e.Value})
	}
	if out, err := configfile.Apply("ini", old, conv); err == nil {
		return out
	}
	out := old
	for _, e := range conv {
		if next, err := configfile.Apply("ini", out, []configfile.Edit{e}); err == nil {
			out = next
		}
	}
	return out
}

// ---------------------------------------------------------------- what's kept

// Store keeps every layer of every emulator's settings in one file.
type Store struct {
	Path string // "" keeps them in memory only

	mu     sync.Mutex
	loaded bool
	data   storeFile
}

// Limits on what one account may keep, so nobody fills the file.
const (
	maxGamesPerUser = 2000
	maxStoreBytes   = 16 << 20
)

type storeFile struct {
	// Admin: by emulator. Users: by emulator, then account. Games: by emulator, account, then game.
	Admin map[string]Values                       `json:"admin"`
	Users map[string]map[string]Values            `json:"users"`
	Games map[string]map[string]map[string]Values `json:"games"`
}

func (st *Store) load() {
	if st.loaded {
		return
	}
	st.loaded = true
	st.data = storeFile{Admin: map[string]Values{}, Users: map[string]map[string]Values{}, Games: map[string]map[string]map[string]Values{}}
	if st.Path == "" {
		return
	}
	b, err := os.ReadFile(st.Path)
	if err != nil || len(b) > maxStoreBytes {
		return
	}
	var d storeFile
	if json.Unmarshal(b, &d) != nil {
		return // unreadable: start again from the defaults rather than refuse every game
	}
	if d.Admin != nil {
		st.data.Admin = d.Admin
	}
	if d.Users != nil {
		st.data.Users = d.Users
	}
	if d.Games != nil {
		st.data.Games = d.Games
	}
}

// Layers returns one emulator's values for an account and, when game isn't 0, one game.
func (st *Store) Layers(emulator string, user, game int64) (admin, own, forGame Values) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.load()
	u, g := strconv.FormatInt(user, 10), strconv.FormatInt(game, 10)
	admin, own, forGame = clone(st.data.Admin[emulator]), clone(st.data.Users[emulator][u]), Values{}
	if game != 0 {
		forGame = clone(st.data.Games[emulator][u][g])
	}
	return admin, own, forGame
}

func clone(v Values) Values {
	out := Values{}
	for k, val := range v {
		out[k] = val
	}
	return out
}

// Set merges cleaned values into one layer and saves. A nil value clears its key.
func (st *Store) Set(emulator, scope string, user, game int64, vals Values) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.load()
	u, g := strconv.FormatInt(user, 10), strconv.FormatInt(game, 10)
	var layer Values
	switch scope {
	case ScopeAdmin:
		if st.data.Admin[emulator] == nil {
			st.data.Admin[emulator] = Values{}
		}
		layer = st.data.Admin[emulator]
	case ScopeUser:
		if st.data.Users[emulator] == nil {
			st.data.Users[emulator] = map[string]Values{}
		}
		if st.data.Users[emulator][u] == nil {
			st.data.Users[emulator][u] = Values{}
		}
		layer = st.data.Users[emulator][u]
	case ScopeGame:
		if game <= 0 {
			return errors.New("which game?")
		}
		if st.data.Games[emulator] == nil {
			st.data.Games[emulator] = map[string]map[string]Values{}
		}
		if st.data.Games[emulator][u] == nil {
			st.data.Games[emulator][u] = map[string]Values{}
		}
		if st.data.Games[emulator][u][g] == nil {
			if len(st.data.Games[emulator][u]) >= maxGamesPerUser {
				return errors.New("that's as many games as can have settings of their own; clear one first")
			}
			st.data.Games[emulator][u][g] = Values{}
		}
		layer = st.data.Games[emulator][u][g]
	default:
		return fmt.Errorf("scope %q is none of admin, user or game", scope)
	}
	for k, v := range vals {
		if v == nil {
			delete(layer, k)
		} else {
			layer[k] = v
		}
	}
	if scope == ScopeGame && len(layer) == 0 {
		delete(st.data.Games[emulator][u], g)
	}
	if st.Path == "" {
		return nil
	}
	b, err := json.MarshalIndent(st.data, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(st.Path, b, 0o600)
}

// ForgetUser drops one account's settings, when the server says the account has gone. Settings
// outlive an uninstall, as saves do, so that installing the emulator again finds them.
func (st *Store) ForgetUser(user int64) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.load()
	u := strconv.FormatInt(user, 10)
	for _, m := range st.data.Users {
		delete(m, u)
	}
	for _, m := range st.data.Games {
		delete(m, u)
	}
	if st.Path == "" {
		return nil
	}
	b, err := json.MarshalIndent(st.data, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(st.Path, b, 0o600)
}

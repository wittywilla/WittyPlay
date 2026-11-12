// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/wittywilla/wittyplay/emulators/configfile"
)

// Every option of each emulator, generated from the emulator's own source at the version
// WittyPlay runs (cmd/wittyplay-emuschema writes schemas/<id>.json). The manifest's own
// settings, written by hand, are laid over these: FullSchema.

//go:embed schemas/*.json
var schemaFiles embed.FS

// SchemaFile is what a generated schema file holds.
type SchemaFile struct {
	Emulator string    `json:"emulator"`
	Version  string    `json:"version"` // the upstream tag the settings were read from
	Source   string    `json:"source,omitempty"`
	Settings []Setting `json:"settings"`
}

var (
	schemaOnce sync.Once
	schemas    map[string]SchemaFile
)

func loadSchemas() {
	schemas = map[string]SchemaFile{}
	ents, _ := schemaFiles.ReadDir("schemas")
	for _, e := range ents {
		b, err := schemaFiles.ReadFile("schemas/" + e.Name())
		if err != nil {
			continue
		}
		var f SchemaFile
		if json.Unmarshal(b, &f) != nil || f.Emulator == "" {
			continue
		}
		// A schema that doesn't hold up isn't offered at all, rather than in part: the test
		// keeps every one valid, so this is only ever a file edited by hand.
		if checkSettings(f.Settings) != nil {
			continue
		}
		schemas[f.Emulator] = f
	}
}

// GeneratedSettings is every setting read from an emulator's own source, or nil when there's no
// schema for it. The list is the caller's own copy.
func GeneratedSettings(id string) []Setting {
	schemaOnce.Do(loadSchemas)
	f, ok := schemas[id]
	if !ok {
		return nil
	}
	// A deep enough copy that a caller changing one setting's target can't change ours.
	b, _ := json.Marshal(f.Settings)
	var out []Setting
	_ = json.Unmarshal(b, &out)
	return out
}

// GeneratedVersion is the upstream version an emulator's generated schema was read from.
func GeneratedVersion(id string) string {
	schemaOnce.Do(loadSchemas)
	return schemas[id].Version
}

// target is where a setting's value goes in a file, for telling two settings that write the
// same value apart from two that only share a name.
type target struct {
	file, format, path string
}

func targetOf(s Setting) (target, bool) {
	if s.Apply == nil {
		return target{}, false
	}
	if t := s.Apply.INI; t != nil {
		return target{t.File, "ini", t.Section + "\x00" + t.Key}, true
	}
	if t := s.Apply.File; t != nil {
		return target{t.File, t.Format, strings.Join(t.Path, "\x00")}, true
	}
	return target{}, false
}

// FullSchema lays the manifest's own settings for an emulator over its generated ones. A
// curated setting wins over a generated one with the same key, and over one that writes the
// same value in the same file: the curated one was written with care (friendlier choices, the
// sandbox's own paths), the generated one only says the option exists.
func FullSchema(curated []Setting, id string) []Setting {
	out := slices.Clone(curated)
	keys := map[string]bool{}
	targets := map[target]bool{}
	for _, s := range curated {
		keys[s.Key] = true
		if t, ok := targetOf(s); ok {
			targets[t] = true
		}
	}
	for _, s := range GeneratedSettings(id) {
		if keys[s.Key] {
			continue
		}
		if t, ok := targetOf(s); ok && targets[t] {
			continue
		}
		keys[s.Key] = true
		out = append(out, s)
	}
	return out
}

// Discover finds the options in a person's real settings file that no setting in known
// describes, so an option a newer emulator added still appears on the page. Each comes back as a
// setting for the person (scope user), its kind read from the value the file has now (which is
// also its default), labelled from its key and grouped by its section; one that names a path,
// an address or anything else that reaches outside the sandbox is fixed, never offered.
//
// The file the options are written back to is the one of known's that is in this format and
// shares the most values with data; with none, nothing is found.
func Discover(format string, data []byte, known []Setting) []Setting {
	entries, err := configfile.Entries(format, data)
	if err != nil {
		return nil
	}
	have := map[string]map[string]bool{} // file: path
	for _, s := range known {
		if t, ok := targetOf(s); ok && t.format == format {
			if have[t.file] == nil {
				have[t.file] = map[string]bool{}
			}
			have[t.file][t.path] = true
		}
	}
	file, best := "", -1
	files := make([]string, 0, len(have))
	for f := range have {
		files = append(files, f)
	}
	slices.Sort(files)
	for _, f := range files {
		n := 0
		for _, e := range entries {
			if have[f][strings.Join(e.Path, "\x00")] {
				n++
			}
		}
		if n > best {
			file, best = f, n
		}
	}
	if file == "" {
		return nil
	}
	taken := map[string]bool{}
	for _, s := range known {
		taken[s.Key] = true
	}
	var out []Setting
	seen := map[string]bool{}
	for _, e := range entries {
		p := strings.Join(e.Path, "\x00")
		if have[file][p] || seen[p] {
			continue
		}
		seen[p] = true
		s, ok := discovered(format, data, file, e, taken)
		if ok {
			out = append(out, s)
		}
	}
	return out
}

// discovered makes a setting for one value found in a file, or says it can't be offered: a
// list or a table written in one value, text too long for a text field, a name too long to
// write back.
func discovered(format string, data []byte, file string, e configfile.Entry, taken map[string]bool) (Setting, bool) {
	s := Setting{
		Label: SettingLabel(e.Path[len(e.Path)-1]),
		Scope: ScopeUser,
		Apply: &Apply{File: &FileTarget{File: file, Format: format, Path: slices.Clone(e.Path)}},
	}
	if len(e.Path) > 1 {
		parts := make([]string, 0, len(e.Path)-1)
		for _, p := range e.Path[:len(e.Path)-1] {
			parts = append(parts, SettingLabel(p))
		}
		s.Group = strings.Join(parts, ", ")
	}
	var written any
	switch v := e.Value.(type) {
	case bool:
		s.Kind, s.Default, written = KindToggle, v, v
	case int64:
		s.Kind, s.Default, s.Step, written = KindNumber, float64(v), 1, v
	case float64:
		s.Kind, s.Default, written = KindNumber, v, v
	case string:
		if format == "ini" || format == "xml" {
			// Text formats: a value that reads as on or off or as a number is offered as one,
			// and written back the way the file spells it.
			switch strings.ToLower(v) {
			case "true", "false":
				s.Kind, s.Default = KindToggle, strings.EqualFold(v, "true")
				on, off := "true", "false"
				if v != strings.ToLower(v) {
					on, off = matchCase("true", v), matchCase("false", v)
				}
				s.Apply.File.Values = map[string]string{"true": on, "false": off}
				written = v
			}
			if s.Kind == "" {
				if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) && strings.TrimSpace(v) == v && !strings.ContainsAny(v, "xXpP_") && v != "" {
					s.Kind, s.Default, written = KindNumber, f, v
				}
			}
		}
		if s.Kind == "" {
			if strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{") {
				return Setting{}, false // most likely a list or table read as its text
			}
			s.Kind, s.Default, written = KindText, v, v
		}
	default:
		return Setting{}, false
	}
	if s.Kind == KindText {
		if _, err := s.Clean(s.Default); err != nil {
			return Setting{}, false
		}
	}
	// The value has to go back into the file as it is, or the next start would fail to write it.
	if _, err := configfile.Apply(format, data, []configfile.Edit{{Path: e.Path, Value: written}}); err != nil {
		return Setting{}, false
	}
	s.Key = SettingKey("found", e.Path, taken)
	s.Fixed = Sensitive(append(slices.Clone(e.Path), s.Group)...)
	if checkSettings([]Setting{s}) != nil {
		return Setting{}, false
	}
	return s, true
}

// matchCase spells word the way example is spelled: "True", "TRUE".
func matchCase(word, example string) string {
	if strings.ToUpper(example) == example {
		return strings.ToUpper(word)
	}
	return strings.ToUpper(word[:1]) + word[1:]
}

// SettingLabel makes a label from a key as an emulator names it: "wideScreenHack" is "Wide
// screen hack", "upscale_multiplier" "Upscale multiplier", "EFBAccessEnable" "EFB access
// enable". Words in capitals stay so.
func SettingLabel(key string) string {
	words := splitWords(key)
	if len(words) == 0 {
		return key
	}
	for i, w := range words {
		if isAcronym(w) {
			continue
		}
		w = strings.ToLower(w)
		if i == 0 {
			w = strings.ToUpper(w[:1]) + w[1:]
		}
		words[i] = w
	}
	return strings.Join(words, " ")
}

func isAcronym(w string) bool {
	if len(w) < 2 {
		return false
	}
	for _, r := range w {
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
}

// splitWords splits a key at underscores, spaces, dots and the turns of camel case, keeping a
// run of capitals together ("EFBAccess" is "EFB", "Access").
func splitWords(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '_' || r == ' ' || r == '.' || r == '-' || r == '/' || r == ':':
			flush()
			continue
		case unicode.IsDigit(r) && len(cur) > 0 && unicode.IsLetter(cur[len(cur)-1]) && len(cur) > 1:
			// "Rumble1" is "Rumble 1"; "x264" stays.
			flush()
		case unicode.IsUpper(r) && len(cur) > 0:
			prev := cur[len(cur)-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			allDigits := strings.IndexFunc(string(cur), func(r rune) bool { return !unicode.IsDigit(r) }) < 0
			if unicode.IsLower(prev) || (unicode.IsDigit(prev) && !allDigits) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return words
}

var keyPart = regexp.MustCompile(`[^A-Za-z0-9]+`)

// SettingKey makes a setting's key from where its value sits in its file: a plain name of
// letters, digits and dots, under 48 characters, not one already taken (which it then takes).
func SettingKey(prefix string, p []string, taken map[string]bool) string {
	parts := []string{}
	if prefix != "" {
		parts = append(parts, prefix)
	}
	for _, s := range p {
		w := keyPart.ReplaceAllString(strings.Join(splitWords(s), " "), " ")
		fields := strings.Fields(w)
		for i, f := range fields {
			if i == 0 {
				fields[i] = strings.ToLower(f)
			} else {
				fields[i] = strings.ToUpper(f[:1]) + strings.ToLower(f[1:])
			}
		}
		if j := strings.Join(fields, ""); j != "" {
			parts = append(parts, j)
		}
	}
	key := strings.Join(parts, ".")
	if key == "" || !unicode.IsLetter(rune(key[0])) {
		key = "k" + key
	}
	key = strings.ToLower(key[:1]) + key[1:]
	if len(key) > 48 {
		sum := sha256.Sum256([]byte(strings.Join(p, "\x00")))
		key = strings.TrimRight(key[:39], ".") + "." + hex.EncodeToString(sum[:])[:7]
		key = strings.ReplaceAll(key, "..", ".")
	}
	base := key
	for n := 2; taken[key]; n++ {
		suffix := strconv.Itoa(n)
		if len(base)+len(suffix) > 48 {
			key = base[:48-len(suffix)] + suffix
		} else {
			key = base + suffix
		}
	}
	taken[key] = true
	return key
}

// Words that mark a setting that must never be offered: a path or a folder (the sandbox
// decides where files are), an address, a port or anything online (the sandbox decides what
// the emulator may reach), the updater, telemetry, and hardware passed through from the host.
var (
	sensitiveWords = map[string]bool{
		"path": true, "paths": true, "folder": true, "folders": true, "dir": true, "directory": true,
		"directories": true, "url": true, "uri": true, "urls": true, "server": true, "servers": true,
		"host": true, "hostname": true, "ip": true, "ipv4": true, "ipv6": true, "address": true,
		"port": true, "socket": true, "netplay": true, "network": true, "net": true, "online": true,
		"internet": true, "update": true, "updates": true, "updater": true, "autoupdate": true,
		"telemetry": true, "analytics": true, "discord": true, "proxy": true, "dns": true,
		"mac": true, "upload": true, "http": true, "https": true, "wifi": true, "lan": true,
		"bba": true, "tap": true, "tapserver": true, "gdb": true, "passthrough": true, "usb": true,
		"bluetooth": true, "achievements": true, "cheevos": true, "retroachievements": true,
		"token": true, "password": true, "username": true, "login": true, "account": true,
		"email": true, "rpc": true, "upnp": true, "relay": true, "infrastructure": true,
		"adhoc": true, "xlink": true, "ssl": true, "certificate": true, "certificates": true,
		"nus": true, "wiilink": true, "pcap": true, "file": true, "filename": true, "iso": true,
		"isos": true, "bios": true, "nand": true, "sd": true, "sdcard": true, "memcard": true,
		"gci": true, "shop": true, "web": true, "webserver": true, "fetch": true, "download": true, "downloads": true, "twitch": true,
		"pipe": true, "dhcp": true, "gateway": true, "subnet": true, "ipc": true,
		"redirections": true, "redirect": true, "upstream": true,
	}
	// Parts of a word that mark one too, for names run together ("GCIFolderAPath").
	sensitiveFragments = []string{"path", "folder", "directory", "url", "netplay", "telemetry", "analytic", "discord", "passthrough", "achievement", "cheevos", "password"}
)

// Sensitive says whether a setting, named by the parts of its path in its file (and its group),
// is one that must never be offered: see sensitiveWords.
func Sensitive(names ...string) bool {
	for _, n := range names {
		for _, w := range splitWords(n) {
			if sensitiveWords[strings.ToLower(w)] {
				return true
			}
		}
		lower := strings.ToLower(n)
		for _, f := range sensitiveFragments {
			if strings.Contains(lower, f) {
				return true
			}
		}
	}
	return false
}

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Just enough C++ reading to pull settings out of an emulator's source: comments stripped, the
// branches of #if that a Linux x86-64 build takes, enums and their values, constants, and the
// arithmetic a default is sometimes written as. Nothing here runs the source; it only reads it.

// stripComments removes // and /* */ comments, leaving strings alone.
func stripComments(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(src) && src[j] != c && src[j] != '\n' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(src) {
				j = len(src) - 1
			}
			b.WriteString(src[i : j+1])
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				b.WriteByte('\n')
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			// Keep the line count, for anyone reading an error against the source.
			b.WriteString(strings.Repeat("\n", strings.Count(src[i:i+2+end+2], "\n")))
			i += 2 + end + 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Macros a Linux x86-64 build defines, and ones it doesn't. Anything else in an #if is taken to
// be true: most such tests are features a full build has.
var (
	cppDefined   = map[string]bool{"__linux__": true, "__unix__": true, "_M_X86_64": true, "_M_X86": true, "__x86_64__": true, "_M_SSE": true, "HAS_LIBMGBA": true, "USE_VULKAN": true, "ENABLE_VULKAN": true, "ENABLE_OPENGL": true, "HAVE_VULKAN": true, "HAVE_OPENGL": true, "__unix": true, "linux": true, "USE_DISCORD_PRESENCE": true, "HAVE_X11": true, "USE_OPENGL": true, "JIT_ENABLED": true, "BOOST_OS_UNIX": true, "BOOST_OS_LINUX": true}
	cppUndefined = map[string]bool{"ANDROID": true, "__ANDROID__": true, "_WIN32": true, "_WIN64": true, "WIN32": true, "_MSC_VER": true, "__APPLE__": true, "__APPLE_CC__": true, "IOS": true, "__IOS__": true, "TARGET_OS_IOS": true, "_M_ARM_64": true, "_M_ARM64": true, "__aarch64__": true, "__arm__": true, "_M_ARM": true, "__FreeBSD__": true, "__OpenBSD__": true, "__NetBSD__": true, "__HAIKU__": true, "__SWITCH__": true, "__LIBRETRO__": true, "LIBRETRO": true, "_DEBUG": true, "TARGET_UWP": true, "PPSSPP_PLATFORM_UWP": true, "MOBILE_DEVICE": true, "__EMSCRIPTEN__": true, "GOLD": true, "TARGET_IPHONE": true, "TARGET_NO_NIXPROF": true, "__vita__": true, "__PS3__": true, "__ORBIS__": true, "__wasm__": true, "WINAPI_FAMILY": true, "PCSX2_DEVBUILD": true, "PCSX2_DEBUG": true, "USE_DX11": true, "BOOST_OS_WINDOWS": true, "BOOST_OS_MACOS": true, "USE_DX9": true, "VIDEO_ROUTING": true, "USE_OMX": true}
	// Macros with a value on a Linux x86-64 build.
	cppValues      = map[string]string{"HOST_CPU": "CPU_X64", "HOST_OS": "OS_LINUX"}
	cppCompare     = regexp.MustCompile(`(\w+)\s*(==|!=)\s*(\w+)`)
	cppDefinedCall = regexp.MustCompile(`defined\s*\(?\s*(\w+)\s*\)?`)
	cppPlatform    = regexp.MustCompile(`PPSSPP_PLATFORM\s*\(\s*(\w+)\s*\)|PPSSPP_ARCH\s*\(\s*(\w+)\s*\)`)
)

// cppCond evaluates the condition of an #if well enough: defined(X) of the macros above, the
// PPSSPP_PLATFORM() and PPSSPP_ARCH() tests, !, && and ||. What it can't read counts as true.
func cppCond(cond string) bool {
	cond = strings.TrimSpace(cond)
	// Comparisons of macros with values: HOST_CPU == CPU_ARM.
	cond = cppCompare.ReplaceAllStringFunc(cond, func(m string) string {
		sub := cppCompare.FindStringSubmatch(m)
		a, b := sub[1], sub[3]
		if v, ok := cppValues[a]; ok {
			a = v
		}
		if v, ok := cppValues[b]; ok {
			b = v
		}
		if (a == b) == (sub[2] == "==") {
			return "1"
		}
		return "0"
	})
	cond = cppPlatform.ReplaceAllStringFunc(cond, func(m string) string {
		sub := cppPlatform.FindStringSubmatch(m)
		name := sub[1] + sub[2]
		switch name {
		case "LINUX", "UNIX", "AMD64", "X86", "64BIT":
			return "1"
		}
		return "0"
	})
	cond = cppDefinedCall.ReplaceAllStringFunc(cond, func(m string) string {
		name := cppDefinedCall.FindStringSubmatch(m)[1]
		switch {
		case cppDefined[name]:
			return "1"
		case cppUndefined[name]:
			return "0"
		}
		return "1"
	})
	// Bare macro names: the platform ones are 0 or 1, others true.
	cond = regexp.MustCompile(`[A-Za-z_]\w*`).ReplaceAllStringFunc(cond, func(name string) string {
		switch {
		case cppDefined[name]:
			return "1"
		case cppUndefined[name]:
			return "0"
		}
		return "1"
	})
	return evalBool(cond)
}

// evalBool evaluates 0/1 with !, &&, || and brackets.
func evalBool(s string) bool {
	s = strings.ReplaceAll(s, " ", "")
	p := &boolParser{s: s}
	v := p.or()
	return v
}

type boolParser struct {
	s string
	i int
}

func (p *boolParser) or() bool {
	v := p.and()
	for strings.HasPrefix(p.s[p.i:], "||") {
		p.i += 2
		w := p.and()
		v = v || w
	}
	return v
}

func (p *boolParser) and() bool {
	v := p.unary()
	for strings.HasPrefix(p.s[p.i:], "&&") {
		p.i += 2
		w := p.unary()
		v = v && w
	}
	return v
}

func (p *boolParser) unary() bool {
	if p.i < len(p.s) && p.s[p.i] == '!' {
		p.i++
		return !p.unary()
	}
	if p.i < len(p.s) && p.s[p.i] == '(' {
		p.i++
		v := p.or()
		if p.i < len(p.s) && p.s[p.i] == ')' {
			p.i++
		}
		return v
	}
	start := p.i
	for p.i < len(p.s) && !strings.ContainsRune("&|()!", rune(p.s[p.i])) {
		p.i++
	}
	tok := p.s[start:p.i]
	// A comparison or anything else unreadable is taken as true.
	return tok != "0"
}

// preprocess strips comments and keeps only the lines a Linux x86-64 build compiles.
func preprocess(src string) string {
	src = stripComments(src)
	lines := strings.Split(src, "\n")
	type frame struct{ parent, taken, active bool }
	var stack []frame
	active := true
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") {
			d := strings.TrimSpace(t[1:])
			switch {
			case strings.HasPrefix(d, "ifdef"):
				c := cppCond("defined(" + strings.TrimSpace(d[5:]) + ")")
				stack = append(stack, frame{active, c, active && c})
				active = active && c
				out = append(out, "")
				continue
			case strings.HasPrefix(d, "ifndef"):
				// An include guard, mostly: only a macro this knows a Linux build defines is
				// taken as defined here.
				c := !cppDefined[strings.TrimSpace(d[6:])]
				stack = append(stack, frame{active, c, active && c})
				active = active && c
				out = append(out, "")
				continue
			case strings.HasPrefix(d, "if"):
				c := cppCond(d[2:])
				stack = append(stack, frame{active, c, active && c})
				active = active && c
				out = append(out, "")
				continue
			case strings.HasPrefix(d, "elif"):
				if n := len(stack); n > 0 {
					f := &stack[n-1]
					c := !f.taken && cppCond(d[4:])
					f.taken = f.taken || c
					active = f.parent && c
				}
				out = append(out, "")
				continue
			case strings.HasPrefix(d, "else"):
				if n := len(stack); n > 0 {
					f := &stack[n-1]
					active = f.parent && !f.taken
					f.taken = true
				}
				out = append(out, "")
				continue
			case strings.HasPrefix(d, "endif"):
				if n := len(stack); n > 0 {
					active = stack[n-1].parent
					stack = stack[:n-1]
				}
				out = append(out, "")
				continue
			}
		}
		if active {
			out = append(out, l)
		} else {
			out = append(out, "")
		}
	}
	return strings.Join(out, "\n")
}

// enumerator is one value of a C++ enum.
type enumerator struct {
	Name  string
	Value int64
}

var enumDecl = regexp.MustCompile(`(?s)\benum\s+(?:class\s+|struct\s+)?(\w*)\s*(?::\s*[\w:\s]+?)?\s*\{(.*?)\}`)

// parseEnums finds every enum in src: by name, its values in order.
func parseEnums(src string, consts map[string]string) map[string][]enumerator {
	out := map[string][]enumerator{}
	for i, m := range enumDecl.FindAllStringSubmatch(src, -1) {
		name, body := m[1], m[2]
		if name == "" {
			name = "(anonymous " + strconv.Itoa(i) + ")" // kept apart, for their names' values
		}
		var list []enumerator
		next := int64(0)
		local := map[string]string{}
		for k, v := range consts {
			local[k] = v
		}
		for _, item := range splitTop(body, ',') {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			en, expr, hasValue := strings.Cut(item, "=")
			en = strings.TrimSpace(en)
			if !regexp.MustCompile(`^\w+$`).MatchString(en) {
				continue
			}
			if hasValue {
				if v, ok := evalNum(expr, local); ok {
					next = int64(v)
				}
			}
			list = append(list, enumerator{en, next})
			local[en] = strconv.FormatInt(next, 10)
			local[name+"::"+en] = strconv.FormatInt(next, 10)
			next++
		}
		if _, dup := out[name]; !dup || len(out[name]) == 0 {
			out[name] = list
		}
	}
	return out
}

// splitTop splits s at sep where it isn't inside brackets or a string.
func splitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	inStr := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			if c == '\\' {
				i++
			} else if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			if c == '\'' && i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
				continue // a digit separator: 100'000
			}
			inStr = c
		case '(', '{', '[': // not < and >: "1 << 6" is more common in a default than a template
			depth++
		case ')', '}', ']':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

var (
	constDecl  = regexp.MustCompile(`(?m)^\s*(?:static\s+)?(?:inline\s+)?(?:constexpr|const)\s+(?:static\s+)?[\w:<>]+\s+(\w+)\s*=\s*([^;]+);`)
	defineDecl = regexp.MustCompile(`(?m)^\s*#\s*define\s+(\w+)\s+(.+)$`)
)

// parseConsts finds constants and simple #defines.
func parseConsts(src string, into map[string]string) map[string]string {
	if into == nil {
		into = map[string]string{}
	}
	for _, m := range constDecl.FindAllStringSubmatch(src, -1) {
		into[m[1]] = strings.TrimSpace(m[2])
	}
	for _, m := range defineDecl.FindAllStringSubmatch(src, -1) {
		into[m[1]] = strings.TrimSpace(m[2])
	}
	return into
}

var castRe = regexp.MustCompile(`(?:static_cast|reinterpret_cast)\s*<[^>]*>|\(\s*(?:u8|u16|u32|u64|s8|s16|s32|s64|int|float|double|unsigned|uint32_t|int32_t|bool)\s*\)`)

// evalNum evaluates a numeric C++ expression: literals in any base, with suffixes, true and
// false, named constants, casts, and + - * / % << >> | & ~.
func evalNum(expr string, consts map[string]string) (float64, bool) {
	return evalNumDepth(expr, consts, 0)
}

func evalNumDepth(expr string, consts map[string]string, depth int) (float64, bool) {
	if depth > 8 {
		return 0, false
	}
	expr = castRe.ReplaceAllString(expr, "")
	p := &numParser{s: strings.TrimSpace(expr), consts: consts, depth: depth}
	v, ok := p.expr(0)
	p.space()
	if !ok || p.i != len(p.s) {
		return 0, false
	}
	return v, true
}

type numParser struct {
	s      string
	i      int
	consts map[string]string
	depth  int
}

func (p *numParser) space() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

var binPrec = map[string]int{"|": 1, "^": 2, "&": 3, "<<": 4, ">>": 4, "+": 5, "-": 5, "*": 6, "/": 6, "%": 6}

func (p *numParser) op() string {
	p.space()
	for _, o := range []string{"<<", ">>", "|", "^", "&", "+", "-", "*", "/", "%"} {
		if strings.HasPrefix(p.s[p.i:], o) && !strings.HasPrefix(p.s[p.i:], "||") && !strings.HasPrefix(p.s[p.i:], "&&") {
			return o
		}
	}
	return ""
}

func (p *numParser) expr(minPrec int) (float64, bool) {
	l, ok := p.unary()
	if !ok {
		return 0, false
	}
	for {
		o := p.op()
		prec, isOp := binPrec[o]
		if !isOp || prec <= minPrec {
			return l, true
		}
		p.i += len(o)
		r, ok := p.expr(prec)
		if !ok {
			return 0, false
		}
		switch o {
		case "+":
			l += r
		case "-":
			l -= r
		case "*":
			l *= r
		case "/":
			if r == 0 {
				return 0, false
			}
			l /= r
		case "%":
			if int64(r) == 0 {
				return 0, false
			}
			l = float64(int64(l) % int64(r))
		case "<<":
			l = float64(int64(l) << uint(r))
		case ">>":
			l = float64(int64(l) >> uint(r))
		case "|":
			l = float64(int64(l) | int64(r))
		case "&":
			l = float64(int64(l) & int64(r))
		case "^":
			l = float64(int64(l) ^ int64(r))
		}
	}
}

var (
	numLit = regexp.MustCompile(`^(0[xX][0-9a-fA-F']+|0[bB][01']+|[0-9][0-9']*\.?[0-9']*(?:[eE][-+]?[0-9]+)?|\.[0-9]+(?:[eE][-+]?[0-9]+)?)([uUlLfF]*)`)
	ident  = regexp.MustCompile(`^[A-Za-z_][\w]*(?:::[A-Za-z_]\w*)*`)
)

func (p *numParser) unary() (float64, bool) {
	p.space()
	if p.i >= len(p.s) {
		return 0, false
	}
	switch c := p.s[p.i]; c {
	case '-':
		p.i++
		v, ok := p.unary()
		return -v, ok
	case '+':
		p.i++
		return p.unary()
	case '~':
		p.i++
		v, ok := p.unary()
		return float64(^int64(v)), ok
	case '(':
		p.i++
		v, ok := p.expr(0)
		p.space()
		if !ok || p.i >= len(p.s) || p.s[p.i] != ')' {
			return 0, false
		}
		p.i++
		return v, true
	}
	if m := numLit.FindStringSubmatch(p.s[p.i:]); m != nil {
		p.i += len(m[0])
		lit := strings.ReplaceAll(m[1], "'", "")
		if strings.HasPrefix(lit, "0x") || strings.HasPrefix(lit, "0X") {
			n, err := strconv.ParseUint(lit[2:], 16, 64)
			return float64(n), err == nil
		}
		if strings.HasPrefix(lit, "0b") || strings.HasPrefix(lit, "0B") {
			n, err := strconv.ParseUint(lit[2:], 2, 64)
			return float64(n), err == nil
		}
		f, err := strconv.ParseFloat(strings.TrimSuffix(lit, "."), 64)
		return f, err == nil
	}
	if m := ident.FindString(p.s[p.i:]); m != "" {
		p.i += len(m)
		switch m {
		case "true":
			return 1, true
		case "false":
			return 0, true
		}
		// A function call, or a braced constructor, isn't something this evaluates.
		p.space()
		if p.i < len(p.s) && (p.s[p.i] == '(' || p.s[p.i] == '{') {
			return 0, false
		}
		v, ok := lookupConst(m, p.consts)
		if !ok {
			return 0, false
		}
		return evalNumDepth(v, p.consts, p.depth+1)
	}
	return 0, false
}

// lookupConst finds a name, trying it with fewer namespace parts: Memory::MEM1_SIZE is found as
// MEM1_SIZE.
func lookupConst(name string, consts map[string]string) (string, bool) {
	for {
		if v, ok := consts[name]; ok {
			return v, true
		}
		_, rest, ok := strings.Cut(name, "::")
		if !ok {
			return "", false
		}
		name = rest
	}
}

// cString reads a C string literal (adjacent literals joined); ok is false for anything else.
func cString(expr string) (string, bool) {
	expr = strings.TrimSpace(expr)
	for _, pre := range []string{"std::string_view(", "std::string(", "TinyString(", "SmallString("} {
		if strings.HasPrefix(expr, pre) && strings.HasSuffix(expr, ")") {
			expr = strings.TrimSpace(expr[len(pre) : len(expr)-1])
		}
	}
	if strings.HasPrefix(expr, "u8\"") {
		expr = expr[2:]
	}
	if expr == "{}" || expr == "\"\"" {
		return "", true
	}
	var b strings.Builder
	for expr != "" {
		if expr[0] != '"' {
			return "", false
		}
		i := 1
		for ; i < len(expr) && expr[i] != '"'; i++ {
			if expr[i] == '\\' && i+1 < len(expr) {
				i++
				switch expr[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(expr[i])
				}
				continue
			}
			b.WriteByte(expr[i])
		}
		if i >= len(expr) {
			return "", false
		}
		expr = strings.TrimSpace(expr[i+1:])
	}
	return b.String(), true
}

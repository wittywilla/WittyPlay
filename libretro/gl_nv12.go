// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"fmt"
	"strings"
	"unsafe"
)

// A frame a core drew with OpenGL, turned into NV12 on the graphics card before it's read back,
// as vk_nv12.go does for Vulkan: a fragment shader draws a single-channel target laid out as the
// frame will be, the luma plane in its first rows and the chroma plane after, each pixel of it one
// byte. It runs in the core's own context, so everything it changes is put back as it was.

const (
	glRed                = 0x1903
	glR8                 = 0x8229
	glFragmentShader     = 0x8B30
	glVertexShader       = 0x8B31
	glCompileStatus      = 0x8B81
	glLinkStatus         = 0x8B82
	glCurrentProgram     = 0x8B8D
	glVertexArrayBinding = 0x85B5
	glDrawFramebuffer    = 0x8CA9
	glDrawFramebufferBnd = 0x8CA6
	glViewport           = 0x0BA2
	glActiveTexture      = 0x84E0
	glTexture0           = 0x84C0
	glSamplerBinding     = 0x8919
	glTriangles          = 0x0004
	glMajorVersion       = 0x821B
	glMinorVersion       = 0x821C
	glColorWritemask     = 0x0C23
)

// The capabilities turned off while the frame is converted, and put back after.
var glNV12Off = []uint32{0x0BE2 /* BLEND */, 0x0B71 /* DEPTH_TEST */, 0x0B90 /* STENCIL_TEST */, 0x0C11, /* SCISSOR_TEST */
	0x0B44 /* CULL_FACE */, 0x8DB9 /* FRAMEBUFFER_SRGB */, 0x8C89 /* RASTERIZER_DISCARD */, 0x0BD0 /* DITHER */}

const glNV12Vertex = `
void main() {
	vec2 p = vec2(gl_VertexID == 1 ? 3.0 : -1.0, gl_VertexID == 2 ? 3.0 : -1.0);
	gl_Position = vec4(p, 0.0, 1.0);
}
`

// The target's rows are the frame's: row y of it is read back as row y of the frame, the top one
// first. Chroma rows follow the luma rows, U and V alternating.
const glNV12Fragment = `
uniform sampler2D src;
uniform ivec4 dims; // the picture's width and height, the frame's (padded) width and height
uniform int flip;   // the core draws upside down, as OpenGL does
out vec4 result;

vec3 px(int x, int y) {
	x = min(x, dims.x - 1);
	y = min(y, dims.y - 1);
	if (flip != 0) {
		y = dims.y - 1 - y;
	}
	return clamp(texelFetch(src, ivec2(x, y), 0).rgb, 0.0, 1.0);
}

void main() {
	int x = int(gl_FragCoord.x);
	int y = int(gl_FragCoord.y);
	int oh = dims.w;
	const vec3 k = vec3(0.2126, 0.7152, 0.0722);
	if (y < oh) {
		result = vec4((16.0 + 219.0 * dot(px(x, y), k)) / 255.0, 0.0, 0.0, 1.0);
		return;
	}
	int by = (y - oh) * 2;
	int bx = (x / 2) * 2;
	vec3 m = (px(bx, by) + px(bx + 1, by) + px(bx, by + 1) + px(bx + 1, by + 1)) * 0.25;
	float l = dot(m, k);
	float c = (x - bx) == 0 ? (m.b - l) / 1.8556 : (m.r - l) / 1.5748;
	result = vec4(clamp(128.0 + 224.0 * c, 16.0, 240.0) / 255.0, 0.0, 0.0, 1.0);
}
`

type glNV12Funcs struct {
	createShader, shaderSource, compileShader, getShaderiv, getShaderInfoLog, deleteShader uintptr
	createProgram, attachShader, linkProgram, getProgramiv, useProgram, deleteProgram      uintptr
	getUniformLocation, uniform1i, uniform4i, genVertexArrays, bindVertexArray             uintptr
	deleteVertexArrays, drawArrays, activeTexture, enable, disable, isEnabled, colorMask   uintptr
	bindSampler                                                                            uintptr // optional: OpenGL 3.3 and ES 3
}

// glNV12 is the program and its target.
type glNV12 struct {
	f                     glNV12Funcs
	prog, vao, tex, fbo   uint32
	locSrc, locDims, locF int32
	w, h                  int // the target's size: the frame's width by one and a half times its height
}

// newGLNV12 makes the program for this context, or says why it can't: OpenGL before 3.0 and ES
// before 3 have no texelFetch or gl_VertexID, and keep the frames as they are drawn.
func newGLNV12(o *glOut) (*glNV12, error) {
	n := &glNV12{}
	for name, slot := range map[string]*uintptr{
		"glCreateShader": &n.f.createShader, "glShaderSource": &n.f.shaderSource, "glCompileShader": &n.f.compileShader,
		"glGetShaderiv": &n.f.getShaderiv, "glGetShaderInfoLog": &n.f.getShaderInfoLog, "glDeleteShader": &n.f.deleteShader,
		"glCreateProgram": &n.f.createProgram, "glAttachShader": &n.f.attachShader, "glLinkProgram": &n.f.linkProgram,
		"glGetProgramiv": &n.f.getProgramiv, "glUseProgram": &n.f.useProgram, "glDeleteProgram": &n.f.deleteProgram,
		"glGetUniformLocation": &n.f.getUniformLocation, "glUniform1i": &n.f.uniform1i, "glUniform4i": &n.f.uniform4i,
		"glGenVertexArrays": &n.f.genVertexArrays, "glBindVertexArray": &n.f.bindVertexArray, "glDeleteVertexArrays": &n.f.deleteVertexArrays,
		"glDrawArrays": &n.f.drawArrays, "glActiveTexture": &n.f.activeTexture, "glEnable": &n.f.enable, "glDisable": &n.f.disable,
		"glIsEnabled": &n.f.isEnabled, "glColorMask": &n.f.colorMask,
	} {
		if *slot = o.p.proc(name); *slot == 0 {
			return nil, fmt.Errorf("no %s", name)
		}
	}
	n.f.bindSampler = o.p.proc("glBindSampler")
	version := glString(call(o.f.getString, glVersion))
	es := strings.HasPrefix(version, "OpenGL ES")
	var major, minor int32
	call(o.f.getIntegerv, glMajorVersion, uintptr(unsafe.Pointer(&major)))
	call(o.f.getIntegerv, glMinorVersion, uintptr(unsafe.Pointer(&minor)))
	call(o.f.getError) // a context before 3.0 doesn't know those names
	var head string
	switch {
	case es && major >= 3:
		head = "#version 300 es\nprecision highp float;\nprecision highp int;\nprecision highp sampler2D;\n"
	case es || major < 3:
		return nil, fmt.Errorf("OpenGL %s has no texelFetch", version)
	case major == 3 && minor < 2:
		head = "#version 130\n"
	default:
		head = "#version 150\n"
	}
	vs, err := n.compile(glVertexShader, head+glNV12Vertex)
	if err != nil {
		return nil, err
	}
	fs, err := n.compile(glFragmentShader, head+glNV12Fragment)
	if err != nil {
		call(n.f.deleteShader, uintptr(vs))
		return nil, err
	}
	n.prog = uint32(call(n.f.createProgram))
	call(n.f.attachShader, uintptr(n.prog), uintptr(vs))
	call(n.f.attachShader, uintptr(n.prog), uintptr(fs))
	call(n.f.linkProgram, uintptr(n.prog))
	call(n.f.deleteShader, uintptr(vs))
	call(n.f.deleteShader, uintptr(fs))
	var ok int32
	call(n.f.getProgramiv, uintptr(n.prog), glLinkStatus, uintptr(unsafe.Pointer(&ok)))
	if ok == 0 {
		n.destroy(o)
		return nil, fmt.Errorf("the conversion program didn't link")
	}
	n.locSrc = int32(call(n.f.getUniformLocation, uintptr(n.prog), uintptr(unsafe.Pointer(vkStr("src")))))
	n.locDims = int32(call(n.f.getUniformLocation, uintptr(n.prog), uintptr(unsafe.Pointer(vkStr("dims")))))
	n.locF = int32(call(n.f.getUniformLocation, uintptr(n.prog), uintptr(unsafe.Pointer(vkStr("flip")))))
	call(n.f.genVertexArrays, 1, uintptr(unsafe.Pointer(&n.vao)))
	call(o.f.genFramebuffers, 1, uintptr(unsafe.Pointer(&n.fbo)))
	call(o.f.genTextures, 1, uintptr(unsafe.Pointer(&n.tex)))
	if e := call(o.f.getError); e != 0 {
		n.destroy(o)
		return nil, fmt.Errorf("OpenGL error %#x setting up the conversion", e)
	}
	return n, nil
}

func (n *glNV12) compile(kind uint32, src string) (uint32, error) {
	s := uint32(call(n.f.createShader, uintptr(kind)))
	text := vkStr(src)
	p := uintptr(unsafe.Pointer(text))
	call(n.f.shaderSource, uintptr(s), 1, uintptr(unsafe.Pointer(&p)), 0)
	call(n.f.compileShader, uintptr(s))
	var ok int32
	call(n.f.getShaderiv, uintptr(s), glCompileStatus, uintptr(unsafe.Pointer(&ok)))
	if ok == 0 {
		var log [512]byte
		var n2 int32
		call(n.f.getShaderInfoLog, uintptr(s), uintptr(len(log)), uintptr(unsafe.Pointer(&n2)), uintptr(unsafe.Pointer(&log[0])))
		call(n.f.deleteShader, uintptr(s))
		return 0, fmt.Errorf("the conversion shader didn't compile: %s", strings.TrimSpace(string(log[:max(0, min(int(n2), len(log)))])))
	}
	return s, nil
}

// render draws the frame in the core's framebuffer, w by h, into the target as NV12 of outW by
// outH, and leaves everything else as it found it. It says whether it did.
func (n *glNV12) render(o *glOut, w, h, outW, outH int) bool {
	f, g := &o.f, &n.f
	tw, th := outW, outH*3/2
	// Everything changed here, as it was.
	var prog, vao, drawFB, active, tex0, sampler int32
	var vp [4]int32
	var mask [4]byte
	call(f.getIntegerv, glCurrentProgram, uintptr(unsafe.Pointer(&prog)))
	call(f.getIntegerv, glVertexArrayBinding, uintptr(unsafe.Pointer(&vao)))
	call(f.getIntegerv, glDrawFramebufferBnd, uintptr(unsafe.Pointer(&drawFB)))
	call(f.getIntegerv, glViewport, uintptr(unsafe.Pointer(&vp[0])))
	call(f.getIntegerv, glActiveTexture, uintptr(unsafe.Pointer(&active)))
	call(g.activeTexture, glTexture0)
	call(f.getIntegerv, glTextureBinding2D, uintptr(unsafe.Pointer(&tex0)))
	if g.bindSampler != 0 {
		call(f.getIntegerv, glSamplerBinding, uintptr(unsafe.Pointer(&sampler)))
	}
	getBool := o.p.proc("glGetBooleanv")
	if getBool != 0 {
		call(getBool, glColorWritemask, uintptr(unsafe.Pointer(&mask[0])))
	}
	enabled := make([]bool, len(glNV12Off))
	for i, cap := range glNV12Off {
		enabled[i] = call(g.isEnabled, uintptr(cap))&0xff != 0
	}
	call(f.getError) // a capability the context doesn't know of isn't a fault

	if tw != n.w || th != n.h {
		call(f.bindTexture, glTexture2D, uintptr(n.tex))
		call(f.texParameteri, glTexture2D, glTexMinFilter, glNearest)
		call(f.texParameteri, glTexture2D, glTexMagFilter, glNearest)
		call(f.texImage2D, glTexture2D, 0, glR8, uintptr(tw), uintptr(th), 0, glRed, glUnsignedByte, 0)
		call(f.bindFramebuffer, glFramebuffer, uintptr(n.fbo))
		call(f.framebufferTexture2D, glFramebuffer, glColorAttachment0, glTexture2D, uintptr(n.tex), 0)
		if call(f.checkFramebufferStatus, glFramebuffer) != glFramebufferDone {
			n.w, n.h = 0, 0
		} else {
			n.w, n.h = tw, th
		}
	}
	done := n.w == tw && n.h == th
	if done {
		call(f.bindFramebuffer, glDrawFramebuffer, uintptr(n.fbo))
		call(f.viewport, 0, 0, uintptr(tw), uintptr(th))
		for _, cap := range glNV12Off {
			call(g.disable, uintptr(cap))
		}
		call(g.colorMask, 1, 1, 1, 1)
		call(g.useProgram, uintptr(n.prog))
		call(g.bindVertexArray, uintptr(n.vao))
		call(f.bindTexture, glTexture2D, uintptr(o.color))
		if g.bindSampler != 0 {
			call(g.bindSampler, 0, 0)
		}
		flip := 0
		if o.flip {
			flip = 1
		}
		call(g.uniform1i, uintptr(n.locSrc), 0)
		call(g.uniform4i, uintptr(n.locDims), uintptr(w), uintptr(h), uintptr(outW), uintptr(outH))
		call(g.uniform1i, uintptr(n.locF), uintptr(flip))
		call(g.drawArrays, glTriangles, 0, 3)
		done = call(f.getError) == 0
	}

	// Put back.
	call(g.useProgram, uintptr(uint32(prog)))
	call(g.bindVertexArray, uintptr(uint32(vao)))
	call(f.bindTexture, glTexture2D, uintptr(uint32(tex0)))
	if g.bindSampler != 0 {
		call(g.bindSampler, 0, uintptr(uint32(sampler)))
	}
	call(g.activeTexture, uintptr(uint32(active)))
	call(f.bindFramebuffer, glDrawFramebuffer, uintptr(uint32(drawFB)))
	call(f.viewport, uintptr(vp[0]), uintptr(vp[1]), uintptr(vp[2]), uintptr(vp[3]))
	if getBool != 0 {
		call(g.colorMask, uintptr(mask[0]), uintptr(mask[1]), uintptr(mask[2]), uintptr(mask[3]))
	}
	for i, cap := range glNV12Off {
		if enabled[i] {
			call(g.enable, uintptr(cap))
		}
	}
	call(f.getError)
	return done
}

func (n *glNV12) destroy(o *glOut) {
	if n.prog != 0 {
		call(n.f.deleteProgram, uintptr(n.prog))
		n.prog = 0
	}
	if n.vao != 0 {
		call(n.f.deleteVertexArrays, 1, uintptr(unsafe.Pointer(&n.vao)))
		n.vao = 0
	}
	if n.fbo != 0 {
		call(o.f.deleteFramebuffers, 1, uintptr(unsafe.Pointer(&n.fbo)))
		n.fbo = 0
	}
	if n.tex != 0 {
		call(o.f.deleteTextures, 1, uintptr(unsafe.Pointer(&n.tex)))
		n.tex = 0
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Hardware rendering: a core that draws with a graphics card asks for a context,
// draws into a framebuffer we made, and passes the marker below instead of
// pixels. The frame is then read back and goes the same way as any other.

// hwFrameValid is RETRO_HW_FRAME_BUFFER_VALID, the marker in place of pixels.
const hwFrameValid = ^uintptr(0)

// The libretro context types.
const (
	hwNone = iota
	hwOpenGL
	hwGLES2
	hwOpenGLCore
	hwGLES3
	hwGLESVersion
	hwVulkan
	hwD3D11
	hwD3D10
	hwD3D12
	hwD3D9
)

// hwRenderC is struct retro_hw_render_callback. Go lays it out as C does.
type hwRenderC struct {
	contextType           int32
	contextReset          uintptr
	getCurrentFramebuffer uintptr
	getProcAddress        uintptr
	depth, stencil        bool
	bottomLeftOrigin      bool
	versionMajor          uint32
	versionMinor          uint32
	cacheContext          bool
	contextDestroy        uintptr
	debugContext          bool
}

var (
	cbGetFramebuffer, cbGetProc uintptr
)

// hwState is what a core asked for and what was made for it.
type hwState struct {
	req     *hwRenderC // the core's own struct
	reset   uintptr
	destroy uintptr
	gl      *glOut
	vk      *vkOut // a Vulkan context instead of gl
}

// vulkanAllowed lets a core have a Vulkan context, where this machine has a card that can make
// one. It's set by the runner's -vulkan, which the server gives cores whose Vulkan renderer is
// known to do better here than OpenGL. Elsewhere they keep OpenGL, as before.
var vulkanAllowed bool

// AllowVulkan lets cores draw with Vulkan.
func AllowVulkan() { vulkanAllowed = true }

// vulkanOffered says whether a core asking for Vulkan gets it here, or why not.
func vulkanOffered() error {
	if !vulkanAllowed {
		return errors.New("Vulkan isn't offered to this core")
	}
	return vkUsable()
}

func (c *Core) setHWRender(data unsafe.Pointer) bool {
	req := (*hwRenderC)(data)
	switch req.contextType {
	case hwOpenGL, hwOpenGLCore, hwGLES2, hwGLES3, hwGLESVersion:
	case hwVulkan:
		// Refused where no card can make a device, so a core goes on to OpenGL (LRPS2 and Dolphin
		// both try it next) rather than load expecting a context that never comes.
		if why := vulkanOffered(); why != nil {
			c.hwRefused = true
			c.logf(2, "Vulkan: %v", why)
			return false
		}
		c.hw = &hwState{req: req, reset: req.contextReset, destroy: req.contextDestroy}
		return true
	default:
		c.hwRefused = true
		c.logf(2, "this runner can't offer hardware context type %d", req.contextType)
		return false
	}
	// Where no context can be made at all, saying so now lets a core that has a software
	// renderer of its own fall back to it (PPSSPP, LRPS2), and one that hasn't stop loading
	// cleanly, rather than load expecting a context that never comes.
	if why := glUnavailable(); why != nil {
		c.hwRefused = true
		c.logf(2, "%v", why)
		return false
	}
	c.hw = &hwState{req: req, reset: req.contextReset, destroy: req.contextDestroy}
	req.getCurrentFramebuffer = cbGetFramebuffer
	req.getProcAddress = cbGetProc
	return true
}

// startHW makes the context once the game has loaded, which is when a core has
// said what it needs, and tells the core it can start drawing.
func (c *Core) startHW(maxW, maxH int) error {
	if c.hw == nil {
		return nil
	}
	req := c.hw.req
	if req.contextType == hwVulkan {
		v, err := newVKOut(c.vkNeg)
		if err != nil {
			return err
		}
		c.hw.vk = v
		c.logf(2, "drawing with %s", v.renderer)
		if c.hw.reset != 0 {
			purego.SyscallN(c.hw.reset)
		}
		return nil
	}
	gr := glRequest{major: int(req.versionMajor), minor: int(req.versionMinor), debug: req.debugContext}
	switch req.contextType {
	case hwOpenGLCore:
		gr.core = true
	case hwGLES2:
		gr.es, gr.major, gr.minor = true, 2, 0
	case hwGLES3:
		gr.es, gr.major, gr.minor = true, 3, 0
	case hwGLESVersion:
		gr.es = true
	}
	o, err := newGLOut(gr, req.depth, req.stencil, req.bottomLeftOrigin)
	if err != nil {
		return err
	}
	if err := o.resize(maxW, maxH); err != nil {
		o.destroy()
		return err
	}
	c.hw.gl = o
	c.logf(2, "drawing with %s", o.renderer)
	if c.hw.reset != 0 {
		purego.SyscallN(c.hw.reset)
	}
	return nil
}

// Renderer names the graphics card in use, "" for a software-rendered core.
func (c *Core) Renderer() string {
	if c.hw != nil && c.hw.gl != nil {
		return c.hw.gl.renderer
	}
	if c.hw != nil && c.hw.vk != nil {
		return c.hw.vk.renderer
	}
	return ""
}

// stopCoreHW tells the core its context is going away. Unloading comes after this,
// and the context itself after that, because a core may still touch it.
func (c *Core) stopCoreHW() {
	if c.hw != nil && (c.hw.gl != nil || c.hw.vk != nil) && c.hw.destroy != 0 {
		purego.SyscallN(c.hw.destroy)
		c.hw.destroy = 0
	}
}

func (c *Core) stopHW() {
	if c.hw != nil && c.hw.gl != nil {
		c.hw.gl.destroy()
		c.hw.gl = nil
	}
	if c.hw != nil && c.hw.vk != nil {
		c.hw.vk.destroy()
		c.hw.vk = nil
	}
}

// startHWRead begins bringing back a frame the core drew on the card.
func (c *Core) startHWRead(w, h int) {
	switch {
	case c.hw == nil:
	case c.hw.gl != nil:
		c.hw.gl.startRead(c.h, w, h)
	case c.hw.vk != nil:
		c.hw.vk.startRead(w, h)
	}
}

// collectHW hands over a frame the card has finished, waiting at most wait, and says whether one
// is still on its way.
func (c *Core) collectHW(wait time.Duration) bool {
	switch {
	case c.hw == nil:
	case c.hw.gl != nil:
		return c.hw.gl.collect(c.h, wait)
	case c.hw.vk != nil:
		return c.hw.vk.collect(c.h, wait)
	}
	return false
}

func (c *Core) logf(level int, format string, a ...any) {
	if c.h.Log != nil {
		c.h.Log(level, fmt.Sprintf(format, a...))
	}
}

func onGetFramebuffer() uintptr {
	if c := cur; c != nil && c.hw != nil && c.hw.gl != nil {
		return uintptr(c.hw.gl.fbo)
	}
	return 0
}

func onGetProc(name uintptr) uintptr {
	c := cur
	if c == nil || c.hw == nil || c.hw.gl == nil || name == 0 {
		return 0
	}
	return c.hw.gl.p.proc(cString((*byte)(ptr(name))))
}

// OptionList is the core's options as it described them, for working out which
// settings to pass it.
func (c *Core) OptionList() []string { return c.traceOptions }

// GPUInfo is what the graphics card a game would draw with says about itself.
type GPUInfo struct {
	Renderer string // the card, as its driver names it
	Vendor   string // the driver's maker
	Version  string // the OpenGL version and driver
	VRAMMB   int    // its own memory, where the driver says; 0 when it doesn't
	Vulkan   string // the card cores that draw with Vulkan get, or why there's none
}

// ProbeGPU makes an OpenGL context the way a game would and describes the graphics card
// behind it, or says why there isn't one.
func ProbeGPU() (GPUInfo, error) {
	o, err := newGLOut(glRequest{major: 3, minor: 3, core: true}, false, false, false)
	if err != nil {
		return GPUInfo{}, err
	}
	defer o.destroy()
	f := &o.f
	vk, vkErr := vkProbe()
	if vkErr != nil {
		vk = "none: " + vkErr.Error()
	}
	info := GPUInfo{Vulkan: vk, Renderer: o.renderer, Vendor: glString(call(f.getString, glVendor)), Version: glString(call(f.getString, glVersion))}
	// NVIDIA's and AMD's extensions, in KiB; any other driver refuses the name, which only
	// sets an error, cleared here.
	var kb [4]int32
	call(f.getIntegerv, 0x9047, uintptr(unsafe.Pointer(&kb[0]))) // GPU_MEMORY_INFO_DEDICATED_VIDMEM_NVX
	if call(f.getError) == 0 && kb[0] > 0 {
		info.VRAMMB = int(kb[0] / 1024)
	} else {
		kb = [4]int32{}
		call(f.getIntegerv, 0x87FC, uintptr(unsafe.Pointer(&kb[0]))) // TEXTURE_FREE_MEMORY_ATI: what's free now
		if call(f.getError) == 0 && kb[0] > 0 {
			info.VRAMMB = int(kb[0] / 1024)
		}
	}
	return info, nil
}

var errCoreThread = errors.New("the system would not start a thread for the core")

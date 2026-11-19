// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
)

// The core's Vulkan functions come from the runner: it looks every one up through the
// get_instance_proc_addr it's handed. The shim stands in for the loader's, so that the runner can
// watch (the runner's -vk-trace) or correct (its -core-fixes, vk_fixes.go) what a core asks of
// Vulkan without touching the core. Without either, the core gets the loader's own.

type vkShimState struct {
	once       sync.Once
	gipa, gdpa uintptr // the shims
	realGIPA   uintptr
	realGDPA   atomic.Uintptr

	mu   sync.Mutex
	orig map[string]uintptr // name → the real function, once looked up
	fns  map[string]uintptr // name → its shim

	// What's been seen, for tracing.
	views   map[uint64]uint64   // image view → image
	fbs     map[uint64][]uint64 // framebuffer → its attachments' views
	fbSize  map[uint64][2]uint32
	cmdFB   map[uintptr]uint64 // command buffer → the framebuffer of the render pass it's in
	draws   map[uint64]int     // framebuffer → draws in it since last logged
	images  map[uint64]string  // image → how it was made
	logged  int
	submits atomic.Int64
}

var vkShim vkShimState

var vkTrace atomic.Bool

// TraceVulkan logs what a core asks of Vulkan, to the runner's error output.
func TraceVulkan(on bool) { vkTrace.Store(on) }

func vkTraceOn() bool { return vkTrace.Load() }

func vkTracef(format string, a ...any) {
	vkShim.mu.Lock()
	n := vkShim.logged
	vkShim.logged++
	vkShim.mu.Unlock()
	if n < 6000 {
		fmt.Fprintf(os.Stderr, "vktrace: "+format+"\n", a...)
	}
}

// vkShimFor is the get_instance_proc_addr to hand a core: the shim when it has work to do, else
// the loader's own.
func vkShimFor(real uintptr) uintptr {
	if !vkTraceOn() && !vkFixesOn() {
		return real
	}
	s := &vkShim
	s.once.Do(func() {
		s.realGIPA = real
		s.orig, s.fns = map[string]uintptr{}, map[string]uintptr{}
		s.views, s.fbs, s.fbSize, s.cmdFB, s.draws, s.images =
			map[uint64]uint64{}, map[uint64][]uint64{}, map[uint64][2]uint32{}, map[uintptr]uint64{}, map[uint64]int{}, map[uint64]string{}
		s.gdpa = purego.NewCallback(func(dev, name uintptr) uintptr {
			return s.lookup(s.realGDPA.Load(), dev, name)
		})
		s.gipa = purego.NewCallback(func(inst, name uintptr) uintptr {
			if cString((*byte)(ptr(name))) == "vkGetDeviceProcAddr" {
				r := call(s.realGIPA, inst, name)
				if r == 0 {
					return 0
				}
				s.realGDPA.Store(r)
				return s.gdpa
			}
			return s.lookup(s.realGIPA, inst, name)
		})
		if vkTraceOn() {
			s.makeTracers()
		}
		if vkFixesOn() {
			s.makeFixes()
		}
	})
	return s.gipa
}

// lookup finds a function through real, and hands back its shim when there is one.
func (s *vkShimState) lookup(real, handle, name uintptr) uintptr {
	if real == 0 {
		return 0
	}
	r := call(real, handle, name)
	if r == 0 {
		return 0
	}
	n := cString((*byte)(ptr(name)))
	s.mu.Lock()
	defer s.mu.Unlock()
	fn, ok := s.fns[n]
	if !ok {
		return r
	}
	s.orig[n] = r
	return fn
}

func (s *vkShimState) real(name string) uintptr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orig[name]
}

func at[T any](p uintptr, off uintptr) T { return *(*T)(ptr(p + off)) }

// noteSetImage records an image handed over, for tracing.
func (s *vkShimState) noteSetImage(img *vkRetroImage) {
	if img == nil || s.views == nil {
		return
	}
	s.mu.Lock()
	desc := s.images[img.createInfo.image]
	s.mu.Unlock()
	vkTracef("set_image image=%#x view=%#x layout=%d format=%d made=%q", img.createInfo.image, img.imageView, img.layout, img.createInfo.format, desc)
}

func (s *vkShimState) makeTracers() {
	add := func(name string, fn any) { s.fns[name] = purego.NewCallback(fn) }
	add("vkCreateImage", func(dev, info, alloc, out uintptr) uintptr {
		r := call(s.real("vkCreateImage"), dev, info, alloc, out)
		// VkImageCreateInfo: flags 16, imageType 20, format 24, extent 28, mips 40, layers 44,
		// samples 48, tiling 52, usage 56.
		img := *(*uint64)(ptr(out))
		desc := fmt.Sprintf("%dx%d format=%d usage=%#x tiling=%d", at[uint32](info, 28), at[uint32](info, 32), at[uint32](info, 24), at[uint32](info, 56), at[uint32](info, 52))
		s.mu.Lock()
		s.images[img] = desc
		s.mu.Unlock()
		vkTracef("createImage %#x %s r=%d", img, desc, int32(r))
		return r
	})
	add("vkCreateImageView", func(dev, info, alloc, out uintptr) uintptr {
		r := call(s.real("vkCreateImageView"), dev, info, alloc, out)
		view, img := *(*uint64)(ptr(out)), at[uint64](info, 24)
		s.mu.Lock()
		s.views[view] = img
		s.mu.Unlock()
		return r
	})
	add("vkCreateFramebuffer", func(dev, info, alloc, out uintptr) uintptr {
		r := call(s.real("vkCreateFramebuffer"), dev, info, alloc, out)
		fb := *(*uint64)(ptr(out))
		n, atts := at[uint32](info, 32), at[uintptr](info, 40)
		w, h := at[uint32](info, 48), at[uint32](info, 52)
		var views []uint64
		var imgs []string
		s.mu.Lock()
		for i := uintptr(0); i < uintptr(n); i++ {
			v := *(*uint64)(ptr(atts + 8*i))
			views = append(views, v)
			imgs = append(imgs, fmt.Sprintf("%#x", s.views[v]))
		}
		s.fbs[fb], s.fbSize[fb] = views, [2]uint32{w, h}
		s.mu.Unlock()
		vkTracef("createFramebuffer %#x %dx%d images=%v renderPass=%#x", fb, w, h, imgs, at[uint64](info, 24))
		return r
	})
	add("vkCmdBeginRenderPass", func(cmd, info, contents uintptr) uintptr {
		fb := at[uint64](info, 24)
		ox, oy := at[int32](info, 32), at[int32](info, 36)
		w, h := at[uint32](info, 40), at[uint32](info, 44)
		nClear, clears := at[uint32](info, 48), at[uintptr](info, 56)
		s.mu.Lock()
		s.cmdFB[cmd] = fb
		var imgs []uint64
		for _, v := range s.fbs[fb] {
			imgs = append(imgs, s.views[v])
		}
		s.mu.Unlock()
		if len(imgs) == 1 {
			var c [4]float32
			if nClear > 0 {
				c = *(*[4]float32)(ptr(clears))
			}
			vkTracef("beginRenderPass cmd=%#x fb=%#x image=%#x area=%d,%d %dx%d rp=%#x clears=%d first=%v", cmd, fb, imgs[0], ox, oy, w, h, at[uint64](info, 16), nClear, c)
		}
		call(s.real("vkCmdBeginRenderPass"), cmd, info, contents)
		return 0
	})
	add("vkCmdEndRenderPass", func(cmd uintptr) uintptr {
		s.mu.Lock()
		fb := s.cmdFB[cmd]
		d := s.draws[fb]
		delete(s.cmdFB, cmd)
		s.draws[fb] = 0
		single := len(s.fbs[fb]) == 1
		s.mu.Unlock()
		if single {
			vkTracef("endRenderPass cmd=%#x fb=%#x draws=%d", cmd, fb, d)
		}
		call(s.real("vkCmdEndRenderPass"), cmd)
		return 0
	})
	add("vkCmdDraw", func(cmd, vc, ic, fv, fi uintptr) uintptr {
		s.mu.Lock()
		s.draws[s.cmdFB[cmd]]++
		s.mu.Unlock()
		call(s.real("vkCmdDraw"), cmd, vc, ic, fv, fi)
		return 0
	})
	add("vkCmdSetViewport", func(cmd, first, count, vps uintptr) uintptr {
		s.mu.Lock()
		fb := s.cmdFB[cmd]
		single := len(s.fbs[fb]) == 1
		s.mu.Unlock()
		if single && count&0xffffffff > 0 {
			vp := *(*[6]float32)(ptr(vps))
			vkTracef("setViewport cmd=%#x fb=%#x %v", cmd, fb, vp)
		}
		call(s.real("vkCmdSetViewport"), cmd, first, count, vps)
		return 0
	})
	add("vkCmdSetScissor", func(cmd, first, count, rects uintptr) uintptr {
		s.mu.Lock()
		fb := s.cmdFB[cmd]
		single := len(s.fbs[fb]) == 1
		s.mu.Unlock()
		if single && count&0xffffffff > 0 {
			r := *(*[4]int32)(ptr(rects))
			vkTracef("setScissor cmd=%#x fb=%#x %v", cmd, fb, r)
		}
		call(s.real("vkCmdSetScissor"), cmd, first, count, rects)
		return 0
	})
	add("vkQueueSubmit", func(queue, count, submits, fence uintptr) uintptr {
		n := s.submits.Add(1)
		if n <= 40 || n%300 == 0 {
			var cmds []uintptr
			for i := uintptr(0); i < count&0xffffffff; i++ {
				si := submits + 72*i
				c, p := at[uint32](si, 40), at[uintptr](si, 48)
				for j := uintptr(0); j < uintptr(c); j++ {
					cmds = append(cmds, *(*uintptr)(ptr(p + 8*j)))
				}
			}
			vkTracef("queueSubmit #%d queue=%#x cmds=%#x fence=%#x", n, queue, cmds, fence)
		}
		return call(s.real("vkQueueSubmit"), queue, count, submits, fence)
	})
	add("vkCmdPipelineBarrier", func(cmd, src, dst, dep, nMem, mem, nBuf, buf, nImg, imgs uintptr) uintptr {
		for i := uintptr(0); i < nImg&0xffffffff; i++ {
			// VkImageMemoryBarrier: srcAccess 16, dstAccess 20, oldLayout 24, newLayout 28, image 40.
			b := imgs + 72*i
			img := at[uint64](b, 40)
			s.mu.Lock()
			desc, known := s.images[img]
			s.mu.Unlock()
			if known && (at[uint32](b, 28) == 1000001002 || at[uint32](b, 24) == 1000001002 || at[uint32](b, 28) == 5 || at[uint32](b, 24) == 5) {
				vkTracef("barrier cmd=%#x image=%#x (%s) %d→%d", cmd, img, desc, at[uint32](b, 24), at[uint32](b, 28))
			}
		}
		call(s.real("vkCmdPipelineBarrier"), cmd, src, dst, dep, nMem, mem, nBuf, buf, nImg, imgs)
		return 0
	})
}

// vkTraceCall notes a call from the core, with the thread it came on, the first few times.
func vkTraceCall(name string) {
	n := vkCallsSeen.Add(1)
	if n <= 60 {
		vkTracef("call %s thread=%d", name, currentThreadID())
	}
}

var vkCallsSeen atomic.Int64

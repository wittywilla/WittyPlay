// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"errors"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/wittywilla/wittyplay/engine"
)

// The structures vk.go lays out by hand, against what GCC gives on x86-64 with vulkan.h,
// libretro.h and libretro_vulkan.h (build-tools/wsl080/vklayout.c printed these). A mistake here
// would hand a core, or the driver, fields in the wrong places.
func TestVulkanLayouts(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("laid out for 64-bit systems")
	}
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"VkApplicationInfo", unsafe.Sizeof(vkApplicationInfo{}), 48},
		{"VkApplicationInfo.apiVersion", unsafe.Offsetof(vkApplicationInfo{}.apiVersion), 44},
		{"VkInstanceCreateInfo", unsafe.Sizeof(vkInstanceCreateInfo{}), 64},
		{"VkInstanceCreateInfo.ppEnabledExtensionNames", unsafe.Offsetof(vkInstanceCreateInfo{}.ppEnabledExtensionNames), 56},
		{"VkQueueFamilyProperties", unsafe.Sizeof(vkQueueFamilyProperties{}), 24},
		{"VkDeviceQueueCreateInfo", unsafe.Sizeof(vkDeviceQueueCreateInfo{}), 40},
		{"VkDeviceQueueCreateInfo.pQueuePriorities", unsafe.Offsetof(vkDeviceQueueCreateInfo{}.pQueuePriorities), 32},
		{"VkDeviceCreateInfo", unsafe.Sizeof(vkDeviceCreateInfo{}), 72},
		{"VkDeviceCreateInfo.pEnabledFeatures", unsafe.Offsetof(vkDeviceCreateInfo{}.pEnabledFeatures), 64},
		{"VkCommandPoolCreateInfo", unsafe.Sizeof(vkCommandPoolCreateInfo{}), 24},
		{"VkCommandBufferAllocateInfo", unsafe.Sizeof(vkCommandBufferAllocateInfo{}), 32},
		{"VkCommandBufferAllocateInfo.commandBufferCount", unsafe.Offsetof(vkCommandBufferAllocateInfo{}.commandBufferCount), 28},
		{"VkCommandBufferBeginInfo", unsafe.Sizeof(vkCommandBufferBeginInfo{}), 32},
		{"VkFenceCreateInfo", unsafe.Sizeof(vkFenceCreateInfo{}), 24},
		{"VkBufferCreateInfo", unsafe.Sizeof(vkBufferCreateInfo{}), 56},
		{"VkBufferCreateInfo.size", unsafe.Offsetof(vkBufferCreateInfo{}.size), 24},
		{"VkBufferCreateInfo.pQueueFamilyIndices", unsafe.Offsetof(vkBufferCreateInfo{}.pQueueFamilyIndices), 48},
		{"VkMemoryRequirements", unsafe.Sizeof(vkMemoryRequirements{}), 24},
		{"VkMemoryAllocateInfo", unsafe.Sizeof(vkMemoryAllocateInfo{}), 32},
		{"VkMemoryAllocateInfo.memoryTypeIndex", unsafe.Offsetof(vkMemoryAllocateInfo{}.memoryTypeIndex), 24},
		{"VkImageCreateInfo", unsafe.Sizeof(vkImageCreateInfo{}), 88},
		{"VkImageCreateInfo.extent", unsafe.Offsetof(vkImageCreateInfo{}.extent), 28},
		{"VkImageCreateInfo.initialLayout", unsafe.Offsetof(vkImageCreateInfo{}.initialLayout), 80},
		{"VkImageMemoryBarrier", unsafe.Sizeof(vkImageMemoryBarrier{}), 72},
		{"VkImageMemoryBarrier.image", unsafe.Offsetof(vkImageMemoryBarrier{}.image), 40},
		{"VkImageMemoryBarrier.subresourceRange", unsafe.Offsetof(vkImageMemoryBarrier{}.subresourceRange), 48},
		{"VkImageViewCreateInfo", unsafe.Sizeof(vkImageViewCreateInfo{}), 80},
		{"VkImageViewCreateInfo.image", unsafe.Offsetof(vkImageViewCreateInfo{}.image), 24},
		{"VkImageViewCreateInfo.format", unsafe.Offsetof(vkImageViewCreateInfo{}.format), 36},
		{"VkImageViewCreateInfo.subresourceRange", unsafe.Offsetof(vkImageViewCreateInfo{}.subresourceRange), 56},
		{"retro_vulkan_image", unsafe.Sizeof(vkRetroImage{}), 96},
		{"retro_vulkan_image.image_layout", unsafe.Offsetof(vkRetroImage{}.layout), 8},
		{"retro_vulkan_image.create_info", unsafe.Offsetof(vkRetroImage{}.createInfo), 16},
		{"VkBufferImageCopy", unsafe.Sizeof(vkBufferImageCopy{}), 56},
		{"VkBufferImageCopy.imageOffset", unsafe.Offsetof(vkBufferImageCopy{}.imageOffset), 32},
		{"VkBufferImageCopy.imageExtent", unsafe.Offsetof(vkBufferImageCopy{}.imageExtent), 44},
		{"VkImageBlit", unsafe.Sizeof(vkImageBlit{}), 80},
		{"VkImageBlit.dstSubresource", unsafe.Offsetof(vkImageBlit{}.dstSubresource), 40},
		{"VkSubmitInfo", unsafe.Sizeof(vkSubmitInfo{}), 72},
		{"VkSubmitInfo.pWaitSemaphores", unsafe.Offsetof(vkSubmitInfo{}.pWaitSemaphores), 24},
		{"VkSubmitInfo.pSignalSemaphores", unsafe.Offsetof(vkSubmitInfo{}.pSignalSemaphores), 64},
		{"retro_vulkan_context", unsafe.Sizeof(vkRetroContext{}), 48},
		{"retro_vulkan_context.queue_family_index", unsafe.Offsetof(vkRetroContext{}.queueFamilyIndex), 24},
		{"retro_vulkan_context.presentation_queue_family_index", unsafe.Offsetof(vkRetroContext{}.presentationQueueFamilyIndex), 40},
		{"retro_hw_render_context_negotiation_interface_vulkan", unsafe.Sizeof(vkNegotiation{}), 48},
		{"retro_hw_render_context_negotiation_interface_vulkan.create_device2", unsafe.Offsetof(vkNegotiation{}.createDevice2), 40},
		{"retro_hw_render_interface_vulkan", unsafe.Sizeof(vkInterface{}), 136},
		{"retro_hw_render_interface_vulkan.queue_index", unsafe.Offsetof(vkInterface{}.queueIndex), 64},
		{"retro_hw_render_interface_vulkan.set_image", unsafe.Offsetof(vkInterface{}.setImage), 72},
		{"retro_hw_render_interface_vulkan.set_signal_semaphore", unsafe.Offsetof(vkInterface{}.setSignalSemaphore), 128},
	} {
		if c.got != c.want {
			t.Errorf("%s: %d, C has %d", c.name, c.got, c.want)
		}
	}
	if vkPhysicalDevicePropertiesSize != 824 || vkPhysicalDeviceFeaturesSize != 220 || vkPhysicalDeviceMemPropsSize != 520 {
		t.Error("the sizes of the structures read as bytes are wrong")
	}
}

func TestPickGPU(t *testing.T) {
	cpu := vkGPUInfo{kind: vkDeviceTypeCPU, name: "llvmpipe"}
	igpu := vkGPUInfo{kind: vkDeviceTypeIntegrated, name: "Intel"}
	dgpu := vkGPUInfo{kind: vkDeviceTypeDiscrete, name: "NVIDIA"}
	if g, ok := pickGPU([]vkGPUInfo{cpu, igpu, dgpu}); !ok || g.name != "NVIDIA" {
		t.Errorf("a card of its own comes first: %+v", g)
	}
	if g, ok := pickGPU([]vkGPUInfo{cpu, igpu}); !ok || g.name != "Intel" {
		t.Errorf("then one built in: %+v", g)
	}
	if _, ok := pickGPU([]vkGPUInfo{cpu}); ok {
		t.Error("the processor's Vulkan is never chosen: OpenGL keeps that case")
	}
}

// A frame drawn with Vulkan comes back through the same path a core's does: an image in the
// layout a core leaves it in, copied, blitted where its format isn't B8G8R8A8, and handed over
// the right way up. Needs a Vulkan card; skips without one.
func TestVulkanReadBack(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// The processor's Vulkan will do for this where there's no card (lavapipe on a Linux machine).
	vkTestOnCPU = true
	defer func() { vkTestOnCPU = false }()
	v, err := newVKOut(nil)
	if errors.Is(err, ErrNoVulkan) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer v.destroy()
	t.Log("on", v.renderer)
	for _, format := range []uint32{vkFormatB8G8R8A8Unorm, 37 /* R8G8B8A8_UNORM, LRPS2's */, 64 /* A2B10G10R10_UNORM_PACK32, Dolphin's */} {
		img, mem := v.testImage(t, format, 32, 16)
		v.setImage(&vkRetroImage{layout: vkLayoutGeneral, createInfo: vkImageViewCreateInfo{image: img, format: format,
			subresourceRange: vkImageSubresourceRange{vkAspectColor, 0, 1, 0, 1}}}, 0, 0)
		v.startRead(32, 16)
		var got []byte
		var w, h int
		deadline := time.Now().Add(5 * time.Second)
		for got == nil && time.Now().Before(deadline) {
			v.collect(engine.Handlers{Video: func(d []byte, fw, fh, s, f int) { got, w, h = append([]byte(nil), d...), fw, fh }}, time.Second)
		}
		if got == nil || w != 32 || h != 16 {
			t.Fatalf("format %d: no frame (%dx%d)", format, w, h)
		}
		// The test image is red at the top and blue below the middle: BGRA in memory.
		if top := got[:4]; top[0] != 0 || top[2] != 0xff {
			t.Errorf("format %d: top = %v, want red", format, top)
		}
		if bottom := got[32*4*15:]; bottom[0] != 0xff || bottom[2] != 0 {
			t.Errorf("format %d: bottom = %v, want blue", format, bottom[:4])
		}
		call(v.f.destroyImage, v.device, uintptr(img), 0)
		call(v.f.freeMemory, v.device, uintptr(mem), 0)
	}
}

// testImage makes a w by h image in format, red in its top half and blue below, in the general
// layout, as a core might hand one over.
func (v *vkOut) testImage(t *testing.T, format uint32, w, h int) (img, mem uint64) {
	t.Helper()
	ii := vkImageCreateInfo{sType: vkStructImageCreateInfo, imageType: vkImageType2D, format: format, extent: vkExtent3D{uint32(w), uint32(h), 1}, mipLevels: 1, arrayLayers: 1,
		samples: vkSampleCount1, tiling: vkTilingOptimal, usage: vkImageUsageTransferSrc | vkImageUsageTransferDst | 0x4 /* SAMPLED, as libretro asks of a core's */, sharingMode: vkSharingExclusive}
	if r := call(v.f.createImage, v.device, uintptr(unsafe.Pointer(&ii)), 0, uintptr(unsafe.Pointer(&img))); int32(r) != vkSuccess {
		t.Fatalf("vkCreateImage: %d", int32(r))
	}
	var req vkMemoryRequirements
	call(v.f.getImageMemoryRequirements, v.device, uintptr(img), uintptr(unsafe.Pointer(&req)))
	mt, _ := v.memoryType(req.memoryTypeBits, 0, vkMemoryHostVisible)
	ai := vkMemoryAllocateInfo{sType: vkStructMemoryAllocateInfo, allocationSize: req.size, memoryTypeIndex: mt}
	call(v.f.allocateMemory, v.device, uintptr(unsafe.Pointer(&ai)), 0, uintptr(unsafe.Pointer(&mem)))
	call(v.f.bindImageMemory, v.device, uintptr(img), uintptr(mem), 0)

	// The pixels, through a slot's buffer, which startRead will make again as it needs.
	s := &v.slots[vkSyncSlots-1]
	if err := v.ensureBuffer(s, uint64(w*h*4)); err != nil {
		t.Fatal(err)
	}
	px := unsafe.Slice((*byte)(ptr(s.bufMapped)), w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, b := byte(0xff), byte(0)
			if y >= h/2 {
				r, b = 0, 0xff
			}
			p := px[(y*w+x)*4:]
			if format == 64 {
				v := uint32(r)<<2 | uint32(r)>>6 | (uint32(b)<<2|uint32(b)>>6)<<20 | 3<<30
				p[0], p[1], p[2], p[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
				continue
			}
			if format == vkFormatB8G8R8A8Unorm {
				p[0], p[1], p[2], p[3] = b, 0, r, 0xff
			} else {
				p[0], p[1], p[2], p[3] = r, 0, b, 0xff
			}
		}
	}
	// Uploading reads from the buffer, so it has to be usable as a source too.
	v.freeBuffer(s)
	bi := vkBufferCreateInfo{sType: vkStructBufferCreateInfo, size: uint64(w * h * 4), usage: 0x1 | vkBufferUsageTransferDst}
	call(v.f.createBuffer, v.device, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&s.buf)))
	var breq vkMemoryRequirements
	call(v.f.getBufferMemoryRequirements, v.device, uintptr(s.buf), uintptr(unsafe.Pointer(&breq)))
	bt, _ := v.memoryType(breq.memoryTypeBits, vkMemoryHostVisible|vkMemoryHostCoherent, 0)
	bai := vkMemoryAllocateInfo{sType: vkStructMemoryAllocateInfo, allocationSize: breq.size, memoryTypeIndex: bt}
	call(v.f.allocateMemory, v.device, uintptr(unsafe.Pointer(&bai)), 0, uintptr(unsafe.Pointer(&s.bufMem)))
	call(v.f.bindBufferMemory, v.device, uintptr(s.buf), uintptr(s.bufMem), 0)
	call(v.f.mapMemory, v.device, uintptr(s.bufMem), 0, vkMaxArg, 0, uintptr(unsafe.Pointer(&s.bufMapped)))
	s.bufSize = uint64(w * h * 4)
	copy(unsafe.Slice((*byte)(ptr(s.bufMapped)), w*h*4), append([]byte(nil), px...))

	cmd := s.cmd
	call(v.f.resetCommandBuffer, cmd, 0)
	begin := vkCommandBufferBeginInfo{sType: vkStructCommandBufferBeginInfo, flags: vkCommandBufferOneTimeSubmit}
	call(v.f.beginCommandBuffer, cmd, uintptr(unsafe.Pointer(&begin)))
	rng := vkImageSubresourceRange{vkAspectColor, 0, 1, 0, 1}
	b := barrier(img, rng, vkLayoutUndefined, vkLayoutTransferDst, 0, vkAccessTransferWrite)
	v.cmdBarrier(cmd, vkStageTopOfPipe, vkStageTransfer, &b)
	region := vkBufferImageCopy{imageSubresource: vkImageSubresourceLayers{vkAspectColor, 0, 0, 1}, imageExtent: vkExtent3D{uint32(w), uint32(h), 1}}
	call(v.f.cmdCopyBufferToImage, cmd, uintptr(s.buf), uintptr(img), vkLayoutTransferDst, 1, uintptr(unsafe.Pointer(&region)))
	b = barrier(img, rng, vkLayoutTransferDst, vkLayoutGeneral, vkAccessTransferWrite, vkAccessTransferRead)
	v.cmdBarrier(cmd, vkStageTransfer, vkStageTransfer, &b)
	call(v.f.endCommandBuffer, cmd)
	si := vkSubmitInfo{sType: vkStructSubmitInfo, commandBufferCount: 1, pCommandBuffers: uintptr(unsafe.Pointer(&cmd))}
	call(v.f.resetFences, v.device, 1, uintptr(unsafe.Pointer(&s.fence)))
	call(v.f.queueSubmit, v.queue, 1, uintptr(unsafe.Pointer(&si)), uintptr(s.fence))
	call(v.f.waitForFences, v.device, 1, uintptr(unsafe.Pointer(&s.fence)), 1, vkMaxArg)
	v.freeBuffer(s)
	return img, mem
}

// Frames converted to NV12 on the card come out with BT.709 limited-range values, padded to a
// width that's a multiple of 4 and an even height. Needs a Vulkan device; the processor's will do.
func TestVulkanNV12(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	vkTestOnCPU, nv12Wanted = true, true
	defer func() { vkTestOnCPU, nv12Wanted = false, false }()
	v, err := newVKOut(nil)
	if errors.Is(err, ErrNoVulkan) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer v.destroy()
	if v.nv12 == nil {
		t.Fatalf("no NV12 pipeline: %s", v.nv12Problem)
	}
	near := func(got byte, want int) bool { return int(got) >= want-2 && int(got) <= want+2 }
	for _, size := range [][2]int{{32, 16}, {30, 14}} {
		w, h := size[0], size[1]
		img, mem := v.testImage(t, 37, w, h)
		view := v.testView(t, img, 37)
		v.setImage(&vkRetroImage{imageView: view, layout: vkLayoutGeneral, createInfo: vkImageViewCreateInfo{image: img, format: 37,
			subresourceRange: vkImageSubresourceRange{vkAspectColor, 0, 1, 0, 1}}}, 0, 0)
		v.startRead(w, h)
		var got []byte
		var gw, gh, gf int
		deadline := time.Now().Add(5 * time.Second)
		for got == nil && time.Now().Before(deadline) {
			v.collect(engine.Handlers{VideoReady: func(d []byte, fw, fh, s, f int) { got, gw, gh, gf = append([]byte(nil), d...), fw, fh, f }}, time.Second)
		}
		ow, oh := (w+3)&^3, (h+1)&^1
		if got == nil || gf != engine.FormatNV12 || gw != ow || gh != oh || len(got) != engine.FrameSize(engine.FormatNV12, ow, oh) {
			t.Fatalf("%dx%d: format %d, %dx%d, %d bytes", w, h, gf, gw, gh, len(got))
		}
		// Red above the middle, blue below.
		if y := got[0]; !near(y, 63) {
			t.Errorf("%dx%d: red's luma %d, want 63", w, h, y)
		}
		if y := got[(oh-1)*ow]; !near(y, 32) {
			t.Errorf("%dx%d: blue's luma %d, want 32", w, h, y)
		}
		uv := got[ow*oh:]
		if u, vv := uv[0], uv[1]; !near(u, 102) || !near(vv, 240) {
			t.Errorf("%dx%d: red's chroma %d,%d, want 102,240", w, h, u, vv)
		}
		last := uv[(oh/2-1)*ow:]
		if u, vv := last[0], last[1]; !near(u, 240) || !near(vv, 118) {
			t.Errorf("%dx%d: blue's chroma %d,%d, want 240,118", w, h, u, vv)
		}
		call(v.f.destroyImageView, v.device, uintptr(view), 0)
		call(v.f.destroyImage, v.device, uintptr(img), 0)
		call(v.f.freeMemory, v.device, uintptr(mem), 0)
	}
}

// testView makes a view of a test image, as a core hands one over.
func (v *vkOut) testView(t *testing.T, img uint64, format uint32) uint64 {
	t.Helper()
	ci := vkImageViewCreateInfo{sType: vkStructImageViewCreateInfo, image: img, viewType: 1, format: format,
		subresourceRange: vkImageSubresourceRange{vkAspectColor, 0, 1, 0, 1}}
	var view uint64
	if r := call(v.f.createImageView, v.device, uintptr(unsafe.Pointer(&ci)), 0, uintptr(unsafe.Pointer(&view))); int32(r) != vkSuccess {
		t.Fatalf("vkCreateImageView: %d", int32(r))
	}
	return view
}

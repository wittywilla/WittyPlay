// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/wittywilla/wittyplay/engine"
)

// Vulkan for cores that draw with it: libretro's RETRO_HW_CONTEXT_VULKAN. The runner makes an
// instance and a device with no window (or lets the core make the device, through its context
// negotiation interface), hands the core libretro's Vulkan interface, and copies each frame the
// core presents into memory the processor can read, a frame at a time, without waiting for it.
//
// Everything here goes through the Vulkan loader (vulkan-1.dll, libvulkan.so.1) with purego, so no
// C compiler is needed. The structures are laid out as C lays them out on 64-bit systems;
// vk_test.go checks them against the sizes the C compiler gives.

// Vulkan's numbers, from vulkan_core.h.
const (
	vkSuccess  = 0
	vkNotReady = 1

	vkStructApplicationInfo         = 0
	vkStructInstanceCreateInfo      = 1
	vkStructDeviceQueueCreateInfo   = 2
	vkStructDeviceCreateInfo        = 3
	vkStructSubmitInfo              = 4
	vkStructMemoryAllocateInfo      = 5
	vkStructFenceCreateInfo         = 8
	vkStructBufferCreateInfo        = 12
	vkStructImageCreateInfo         = 14
	vkStructCommandPoolCreateInfo   = 39
	vkStructCommandBufferAllocInfo  = 40
	vkStructCommandBufferBeginInfo  = 42
	vkStructImageMemoryBarrier      = 45
	vkQueueGraphicsBit              = 0x1
	vkDeviceTypeIntegrated          = 1
	vkDeviceTypeDiscrete            = 2
	vkDeviceTypeVirtual             = 3
	vkDeviceTypeCPU                 = 4
	vkMemoryHostVisible             = 0x2
	vkMemoryHostCoherent            = 0x4
	vkMemoryHostCached              = 0x8
	vkBufferUsageTransferDst        = 0x2
	vkImageUsageTransferSrc         = 0x1
	vkImageUsageTransferDst         = 0x2
	vkLayoutUndefined               = 0
	vkLayoutGeneral                 = 1
	vkLayoutTransferSrc             = 6
	vkLayoutTransferDst             = 7
	vkAccessShaderRead              = 0x20
	vkAccessColorAttachmentWrite    = 0x100
	vkAccessTransferRead            = 0x800
	vkAccessTransferWrite           = 0x1000
	vkAccessHostRead                = 0x2000
	vkAccessMemoryWrite             = 0x10000
	vkStageTransfer                 = 0x1000
	vkStageAllCommands              = 0x10000
	vkStageHost                     = 0x4000
	vkStageTopOfPipe                = 0x1
	vkAspectColor                   = 0x1
	vkFormatR8G8B8A8Srgb            = 43
	vkFormatB8G8R8A8Unorm           = 44
	vkFormatB8G8R8A8Srgb            = 50
	vkFenceCreateSignaled           = 0x1
	vkCommandPoolResetBuffer        = 0x2
	vkCommandBufferOneTimeSubmit    = 0x1
	vkQueueFamilyIgnored            = ^uint32(0)
	vkWholeSize                     = ^uint64(0)
	vkAPIVersion11                  = 1<<22 | 1<<12
	vkPhysicalDevicePropertiesSize  = 824
	vkPhysicalDeviceFeaturesSize    = 220
	vkPhysicalDeviceMemPropsSize    = 520
	vkHWRenderInterfaceVulkan       = 0
	vkHWRenderInterfaceVersion      = 5
	vkNegotiationInterfaceVulkan    = 0
	vkNegotiationVersionWeSupport   = 2
	vkSyncSlots                     = 3
	vkTimeoutNever                  = ^uint64(0)
	envGetHWRenderInterface         = 41 | envExperimental
	envSetHWNegotiationInterface    = 43 | envExperimental
	envGetHWNegotiationSupport      = 73 | envExperimental
	vkNegotiationVersionV1          = 1
	vkMaxDevices                    = 16
	vkFilterNearest                 = 0
	vkImageType2D                   = 1
	vkSampleCount1                  = 1
	vkTilingOptimal                 = 0
	vkSharingExclusive              = 0
	vkCommandBufferLevelPrimary     = 0
	vkImageViewCreateInfoFormatOffs = 36
)

// The C structures, laid out as on 64-bit systems.

type vkApplicationInfo struct {
	sType              uint32
	pNext              uintptr
	pApplicationName   *byte
	applicationVersion uint32
	pEngineName        *byte
	engineVersion      uint32
	apiVersion         uint32
}

type vkInstanceCreateInfo struct {
	sType                   uint32
	pNext                   uintptr
	flags                   uint32
	pApplicationInfo        *vkApplicationInfo
	enabledLayerCount       uint32
	ppEnabledLayerNames     uintptr
	enabledExtensionCount   uint32
	ppEnabledExtensionNames uintptr
}

type vkQueueFamilyProperties struct {
	queueFlags, queueCount, timestampValidBits uint32
	granularity                                [3]uint32
}

type vkDeviceQueueCreateInfo struct {
	sType            uint32
	pNext            uintptr
	flags            uint32
	queueFamilyIndex uint32
	queueCount       uint32
	pQueuePriorities *float32
}

type vkDeviceCreateInfo struct {
	sType                   uint32
	pNext                   uintptr
	flags                   uint32
	queueCreateInfoCount    uint32
	pQueueCreateInfos       *vkDeviceQueueCreateInfo
	enabledLayerCount       uint32
	ppEnabledLayerNames     uintptr
	enabledExtensionCount   uint32
	ppEnabledExtensionNames uintptr
	pEnabledFeatures        uintptr
}

type vkCommandPoolCreateInfo struct {
	sType            uint32
	pNext            uintptr
	flags            uint32
	queueFamilyIndex uint32
}

type vkCommandBufferAllocateInfo struct {
	sType              uint32
	pNext              uintptr
	commandPool        uint64
	level              uint32
	commandBufferCount uint32
}

type vkCommandBufferBeginInfo struct {
	sType            uint32
	pNext            uintptr
	flags            uint32
	pInheritanceInfo uintptr
}

type vkFenceCreateInfo struct {
	sType uint32
	pNext uintptr
	flags uint32
}

type vkBufferCreateInfo struct {
	sType                 uint32
	pNext                 uintptr
	flags                 uint32
	size                  uint64
	usage                 uint32
	sharingMode           uint32
	queueFamilyIndexCount uint32
	pQueueFamilyIndices   uintptr
}

type vkMemoryRequirements struct {
	size, alignment uint64
	memoryTypeBits  uint32
}

type vkMemoryAllocateInfo struct {
	sType           uint32
	pNext           uintptr
	allocationSize  uint64
	memoryTypeIndex uint32
}

type vkExtent3D struct{ width, height, depth uint32 }

type vkImageCreateInfo struct {
	sType                 uint32
	pNext                 uintptr
	flags                 uint32
	imageType             uint32
	format                uint32
	extent                vkExtent3D
	mipLevels             uint32
	arrayLayers           uint32
	samples               uint32
	tiling                uint32
	usage                 uint32
	sharingMode           uint32
	queueFamilyIndexCount uint32
	pQueueFamilyIndices   uintptr
	initialLayout         uint32
}

type vkImageSubresourceRange struct {
	aspectMask, baseMipLevel, levelCount, baseArrayLayer, layerCount uint32
}

type vkImageSubresourceLayers struct {
	aspectMask, mipLevel, baseArrayLayer, layerCount uint32
}

type vkImageMemoryBarrier struct {
	sType               uint32
	pNext               uintptr
	srcAccessMask       uint32
	dstAccessMask       uint32
	oldLayout           uint32
	newLayout           uint32
	srcQueueFamilyIndex uint32
	dstQueueFamilyIndex uint32
	image               uint64
	subresourceRange    vkImageSubresourceRange
}

type vkImageViewCreateInfo struct {
	sType            uint32
	pNext            uintptr
	flags            uint32
	image            uint64
	viewType         uint32
	format           uint32
	components       [4]uint32
	subresourceRange vkImageSubresourceRange
}

// vkRetroImage is struct retro_vulkan_image.
type vkRetroImage struct {
	imageView  uint64
	layout     uint32
	createInfo vkImageViewCreateInfo
}

type vkOffset3D struct{ x, y, z int32 }

type vkBufferImageCopy struct {
	bufferOffset      uint64
	bufferRowLength   uint32
	bufferImageHeight uint32
	imageSubresource  vkImageSubresourceLayers
	imageOffset       vkOffset3D
	imageExtent       vkExtent3D
}

type vkImageBlit struct {
	srcSubresource vkImageSubresourceLayers
	srcOffsets     [2]vkOffset3D
	dstSubresource vkImageSubresourceLayers
	dstOffsets     [2]vkOffset3D
}

type vkSubmitInfo struct {
	sType                uint32
	pNext                uintptr
	waitSemaphoreCount   uint32
	pWaitSemaphores      uintptr
	pWaitDstStageMask    uintptr
	commandBufferCount   uint32
	pCommandBuffers      uintptr
	signalSemaphoreCount uint32
	pSignalSemaphores    uintptr
}

// vkRetroContext is struct retro_vulkan_context.
type vkRetroContext struct {
	gpu                          uintptr
	device                       uintptr
	queue                        uintptr
	queueFamilyIndex             uint32
	presentationQueue            uintptr
	presentationQueueFamilyIndex uint32
}

// vkNegotiation is struct retro_hw_render_context_negotiation_interface_vulkan, the core's own.
type vkNegotiation struct {
	interfaceType      uint32
	interfaceVersion   uint32
	getApplicationInfo uintptr
	createDevice       uintptr
	destroyDevice      uintptr
	createInstance     uintptr // v2
	createDevice2      uintptr // v2
}

// vkInterface is struct retro_hw_render_interface_vulkan, which the core asks for.
type vkInterface struct {
	interfaceType       uint32
	interfaceVersion    uint32
	handle              uintptr
	instance            uintptr
	gpu                 uintptr
	device              uintptr
	getDeviceProcAddr   uintptr
	getInstanceProcAddr uintptr
	queue               uintptr
	queueIndex          uint32
	setImage            uintptr
	getSyncIndex        uintptr
	getSyncIndexMask    uintptr
	setCommandBuffers   uintptr
	waitSyncIndex       uintptr
	lockQueue           uintptr
	unlockQueue         uintptr
	setSignalSemaphore  uintptr
}

// vkFuncs are the Vulkan functions used here.
type vkFuncs struct {
	getInstanceProcAddr, getDeviceProcAddr                                                 uintptr
	createInstance, destroyInstance, enumeratePhysicalDevices, getPhysicalDeviceProperties uintptr
	getPhysicalDeviceQueueFamilyProperties, getPhysicalDeviceMemoryProperties              uintptr
	getPhysicalDeviceFeatures, createDevice, destroyDevice, getDeviceQueue, deviceWaitIdle uintptr
	createCommandPool, destroyCommandPool, allocateCommandBuffers, beginCommandBuffer      uintptr
	endCommandBuffer, resetCommandBuffer, cmdPipelineBarrier, cmdCopyImageToBuffer         uintptr
	cmdBlitImage, queueSubmit, createFence, destroyFence, resetFences, waitForFences       uintptr
	getFenceStatus, createBuffer, destroyBuffer, getBufferMemoryRequirements               uintptr
	allocateMemory, freeMemory, bindBufferMemory, mapMemory, unmapMemory                   uintptr
	createImage, destroyImage, getImageMemoryRequirements, bindImageMemory                 uintptr
	cmdCopyBufferToImage                                                                   uintptr // tests only
}

func vkStr(s string) *byte { b := append([]byte(s), 0); return &b[0] }

// vkSlot is one of the frames that can be on their way back at once: the core's sync index.
type vkSlot struct {
	cmd           uintptr
	fence         uint64
	submitted     bool // the fence will signal for this slot's last submission
	pending       bool // a frame waits in buf to be handed over
	seq           uint64
	buf, bufMem   uint64
	bufSize       uint64
	bufMapped     uintptr
	blit, blitMem uint64 // a B8G8R8A8 image for frames in other formats
	blitW, blitH  uint32
	blitFormat    uint32
	w, h          int
}

// vkOut is a Vulkan device lent to a core, and its frames on their way back.
type vkOut struct {
	f           vkFuncs
	lib         uintptr
	instance    uintptr
	gpu         uintptr
	device      uintptr
	queue       uintptr
	family      uint32
	pool        uint64
	memProps    [vkPhysicalDeviceMemPropsSize]byte
	neg         *vkNegotiation // the core's, if it gave one
	surface     uint64         // to present to, for cores that want one; 0 where none could be made
	window      uintptr        // the hidden window it's on, where it needs one
	ownDev      bool           // the device was made here, not by the core
	surfaceExts bool           // the instance has the extensions a surface needs
	renderer    string

	iface vkInterface

	queueMu sync.Mutex // the core's lock_queue, and every submission here

	mu        sync.Mutex
	slots     [vkSyncSlots]vkSlot
	index     uint32 // the sync index the core is on now
	image     *vkRetroImage
	imageCopy vkRetroImage
	waitSems  []uint64
	cmds      []uintptr
	signal    uint64
	seq       uint64
	copied    []byte
	gone      bool
}

var (
	vkCallbacks struct {
		once                                                        sync.Once
		setImage, getSyncIndex, getSyncIndexMask, setCommandBuffers uintptr
		waitSyncIndex, lockQueue, unlockQueue, setSignalSemaphore   uintptr
		instanceWrapper, deviceWrapper                              uintptr
	}
	vkCurrent   *vkOut
	vkCurrentMu sync.Mutex
)

func currentVK() *vkOut {
	vkCurrentMu.Lock()
	defer vkCurrentMu.Unlock()
	return vkCurrent
}

func makeVKCallbacks() {
	vkCallbacks.once.Do(func() {
		cb := &vkCallbacks
		cb.setImage = purego.NewCallback(func(handle, image, numSems, sems, srcFamily uintptr) uintptr {
			if v := currentVK(); v != nil {
				v.setImage((*vkRetroImage)(ptr(image)), uint32(numSems), sems)
			}
			return 0
		})
		cb.getSyncIndex = purego.NewCallback(func(handle uintptr) uintptr {
			if v := currentVK(); v != nil {
				v.mu.Lock()
				defer v.mu.Unlock()
				return uintptr(v.index)
			}
			return 0
		})
		cb.getSyncIndexMask = purego.NewCallback(func(handle uintptr) uintptr { return 1<<vkSyncSlots - 1 })
		cb.setCommandBuffers = purego.NewCallback(func(handle, num, cmds uintptr) uintptr {
			if v := currentVK(); v != nil && num > 0 && cmds != 0 {
				v.mu.Lock()
				v.cmds = append(v.cmds[:0], unsafe.Slice((*uintptr)(ptr(cmds)), int(num))...)
				v.mu.Unlock()
			}
			return 0
		})
		cb.waitSyncIndex = purego.NewCallback(func(handle uintptr) uintptr {
			if v := currentVK(); v != nil {
				v.waitSyncIndex()
			}
			return 0
		})
		cb.lockQueue = purego.NewCallback(func(handle uintptr) uintptr {
			if v := currentVK(); v != nil {
				v.queueMu.Lock()
			}
			return 0
		})
		cb.unlockQueue = purego.NewCallback(func(handle uintptr) uintptr {
			if v := currentVK(); v != nil {
				v.queueMu.Unlock()
			}
			return 0
		})
		cb.setSignalSemaphore = purego.NewCallback(func(handle, sem uintptr) uintptr {
			if v := currentVK(); v != nil {
				v.mu.Lock()
				v.signal = uint64(sem)
				v.mu.Unlock()
			}
			return 0
		})
		cb.instanceWrapper = purego.NewCallback(func(opaque, info uintptr) uintptr {
			v := currentVK()
			if v == nil || info == 0 {
				return 0
			}
			var inst uintptr
			if r := call(v.f.createInstance, info, 0, uintptr(unsafe.Pointer(&inst))); int32(r) != vkSuccess {
				return 0
			}
			return inst
		})
		cb.deviceWrapper = purego.NewCallback(func(gpu, opaque, info uintptr) uintptr {
			v := currentVK()
			if v == nil || info == 0 {
				return 0
			}
			var dev uintptr
			if r := call(v.f.createDevice, gpu, info, 0, uintptr(unsafe.Pointer(&dev))); int32(r) != vkSuccess {
				return 0
			}
			return dev
		})
	})
}

// vkMaxArg is VK_WHOLE_SIZE and UINT64_MAX as an argument. Vulkan's 64-bit arguments only fit
// one machine word on 64-bit systems, so it's offered only there (vkProbe); on others this
// compiles and goes unused.
var vkMaxArg = ^uintptr(0)

// ErrNoVulkan is why Vulkan couldn't be offered.
var ErrNoVulkan = errors.New("no usable Vulkan device")

// loadVK finds the loader and its functions that need no instance.
func loadVK() (*vkOut, error) {
	lib, err := openVulkanLoader()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoVulkan, err)
	}
	v := &vkOut{lib: lib}
	if v.f.getInstanceProcAddr, err = symbol(lib, "vkGetInstanceProcAddr"); err != nil {
		return nil, fmt.Errorf("%w: the loader has no vkGetInstanceProcAddr", ErrNoVulkan)
	}
	v.f.createInstance = v.instanceProc(0, "vkCreateInstance")
	if v.f.createInstance == 0 {
		return nil, fmt.Errorf("%w: the loader has no vkCreateInstance", ErrNoVulkan)
	}
	return v, nil
}

func (v *vkOut) instanceProc(inst uintptr, name string) uintptr {
	return call(v.f.getInstanceProcAddr, inst, uintptr(unsafe.Pointer(vkStr(name))))
}

// loadInstanceFuncs fills in what an instance gives.
func (v *vkOut) loadInstanceFuncs() error {
	for name, slot := range map[string]*uintptr{
		"vkDestroyInstance": &v.f.destroyInstance, "vkEnumeratePhysicalDevices": &v.f.enumeratePhysicalDevices,
		"vkGetPhysicalDeviceProperties": &v.f.getPhysicalDeviceProperties, "vkGetPhysicalDeviceQueueFamilyProperties": &v.f.getPhysicalDeviceQueueFamilyProperties,
		"vkGetPhysicalDeviceMemoryProperties": &v.f.getPhysicalDeviceMemoryProperties, "vkGetPhysicalDeviceFeatures": &v.f.getPhysicalDeviceFeatures,
		"vkCreateDevice": &v.f.createDevice, "vkGetDeviceProcAddr": &v.f.getDeviceProcAddr,
	} {
		if *slot = v.instanceProc(v.instance, name); *slot == 0 {
			return fmt.Errorf("%w: no %s", ErrNoVulkan, name)
		}
	}
	return nil
}

func (v *vkOut) loadDeviceFuncs() error {
	for name, slot := range map[string]*uintptr{
		"vkDestroyDevice": &v.f.destroyDevice, "vkGetDeviceQueue": &v.f.getDeviceQueue, "vkDeviceWaitIdle": &v.f.deviceWaitIdle,
		"vkCreateCommandPool": &v.f.createCommandPool, "vkDestroyCommandPool": &v.f.destroyCommandPool, "vkAllocateCommandBuffers": &v.f.allocateCommandBuffers,
		"vkBeginCommandBuffer": &v.f.beginCommandBuffer, "vkEndCommandBuffer": &v.f.endCommandBuffer, "vkResetCommandBuffer": &v.f.resetCommandBuffer,
		"vkCmdPipelineBarrier": &v.f.cmdPipelineBarrier, "vkCmdCopyImageToBuffer": &v.f.cmdCopyImageToBuffer, "vkCmdBlitImage": &v.f.cmdBlitImage,
		"vkQueueSubmit": &v.f.queueSubmit, "vkCreateFence": &v.f.createFence, "vkDestroyFence": &v.f.destroyFence, "vkResetFences": &v.f.resetFences,
		"vkWaitForFences": &v.f.waitForFences, "vkGetFenceStatus": &v.f.getFenceStatus, "vkCreateBuffer": &v.f.createBuffer,
		"vkDestroyBuffer": &v.f.destroyBuffer, "vkGetBufferMemoryRequirements": &v.f.getBufferMemoryRequirements, "vkAllocateMemory": &v.f.allocateMemory,
		"vkFreeMemory": &v.f.freeMemory, "vkBindBufferMemory": &v.f.bindBufferMemory, "vkMapMemory": &v.f.mapMemory, "vkUnmapMemory": &v.f.unmapMemory,
		"vkCreateImage": &v.f.createImage, "vkDestroyImage": &v.f.destroyImage, "vkGetImageMemoryRequirements": &v.f.getImageMemoryRequirements,
		"vkBindImageMemory": &v.f.bindImageMemory, "vkCmdCopyBufferToImage": &v.f.cmdCopyBufferToImage,
	} {
		if *slot = call(v.f.getDeviceProcAddr, v.device, uintptr(unsafe.Pointer(vkStr(name)))); *slot == 0 {
			return fmt.Errorf("%w: the device has no %s", ErrNoVulkan, name)
		}
	}
	return nil
}

// createInstance makes the instance, from the core's application info where it gives one.
func (v *vkOut) createInstance(neg *vkNegotiation) error {
	app := &vkApplicationInfo{sType: vkStructApplicationInfo, pApplicationName: vkStr("WittyPlay"), pEngineName: vkStr("WittyPlay"), apiVersion: vkAPIVersion11}
	if neg != nil && neg.getApplicationInfo != 0 {
		if p := call(neg.getApplicationInfo); p != 0 {
			app = (*vkApplicationInfo)(ptr(p))
			if app.apiVersion < vkAPIVersion11 {
				own := *app
				own.apiVersion = vkAPIVersion11
				app = &own
			}
		}
	}
	// The surface extensions, where the driver has them: some cores want a surface (createSurface).
	var exts []*byte
	have := v.instanceExtensions()
	for _, e := range vkSurfaceExtensions {
		if have[e] {
			exts = append(exts, vkStr(e))
		}
	}
	info := vkInstanceCreateInfo{sType: vkStructInstanceCreateInfo, pApplicationInfo: app}
	if len(exts) == len(vkSurfaceExtensions) && len(exts) > 0 {
		info.enabledExtensionCount, info.ppEnabledExtensionNames = uint32(len(exts)), uintptr(unsafe.Pointer(&exts[0]))
		v.surfaceExts = true
	}
	if r := call(v.f.createInstance, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&v.instance))); int32(r) != vkSuccess || v.instance == 0 {
		return fmt.Errorf("%w: vkCreateInstance failed (%d)", ErrNoVulkan, int32(r))
	}
	return v.loadInstanceFuncs()
}

// instanceExtensions are the extensions the loader offers instances.
func (v *vkOut) instanceExtensions() map[string]bool {
	enum := v.instanceProc(0, "vkEnumerateInstanceExtensionProperties")
	if enum == 0 {
		return nil
	}
	var n uint32
	call(enum, 0, uintptr(unsafe.Pointer(&n)), 0)
	if n == 0 || n > 512 {
		return nil
	}
	props := make([]byte, int(n)*260) // VkExtensionProperties: a 256-byte name and a version
	call(enum, 0, uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&props[0])))
	out := map[string]bool{}
	for i := 0; i < int(n); i++ {
		name := string(props[i*260 : i*260+256])
		if j := strings.IndexByte(name, 0); j >= 0 {
			name = name[:j]
		}
		out[name] = true
	}
	return out
}

// gpuInfo is what a physical device says about itself.
type vkGPUInfo struct {
	handle     uintptr
	kind       uint32
	name       string
	apiVersion uint32
}

func (v *vkOut) gpus() []vkGPUInfo {
	var n uint32
	call(v.f.enumeratePhysicalDevices, v.instance, uintptr(unsafe.Pointer(&n)), 0)
	if n == 0 {
		return nil
	}
	n = min(n, vkMaxDevices)
	handles := make([]uintptr, n)
	call(v.f.enumeratePhysicalDevices, v.instance, uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&handles[0])))
	var out []vkGPUInfo
	for _, h := range handles[:n] {
		var props [vkPhysicalDevicePropertiesSize]byte
		call(v.f.getPhysicalDeviceProperties, h, uintptr(unsafe.Pointer(&props[0])))
		name := string(props[20:276])
		if i := strings.IndexByte(name, 0); i >= 0 {
			name = name[:i]
		}
		out = append(out, vkGPUInfo{handle: h, kind: *(*uint32)(unsafe.Pointer(&props[16])), name: name, apiVersion: *(*uint32)(unsafe.Pointer(&props[0]))})
	}
	return out
}

// vkTestOnCPU lets the tests use the processor's Vulkan (lavapipe) where there's no card, so the
// path is tested on machines without one. Nothing else sets it.
var vkTestOnCPU bool

// pickGPU chooses a card: one of its own first, then one built into the processor, never the
// processor itself (lavapipe, SwiftShader): OpenGL keeps that case.
func pickGPU(gpus []vkGPUInfo) (vkGPUInfo, bool) {
	kinds := []uint32{vkDeviceTypeDiscrete, vkDeviceTypeIntegrated, vkDeviceTypeVirtual}
	if vkTestOnCPU {
		kinds = append(kinds, vkDeviceTypeCPU)
	}
	for _, want := range kinds {
		for _, g := range gpus {
			if g.kind == want {
				return g, true
			}
		}
	}
	return vkGPUInfo{}, false
}

func (v *vkOut) graphicsFamily(gpu uintptr) (uint32, bool) {
	var n uint32
	call(v.f.getPhysicalDeviceQueueFamilyProperties, gpu, uintptr(unsafe.Pointer(&n)), 0)
	if n == 0 {
		return 0, false
	}
	props := make([]vkQueueFamilyProperties, n)
	call(v.f.getPhysicalDeviceQueueFamilyProperties, gpu, uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&props[0])))
	for i, p := range props[:n] {
		if p.queueFlags&vkQueueGraphicsBit != 0 && p.queueCount > 0 {
			return uint32(i), true
		}
	}
	return 0, false
}

// createDevice makes the device: through the core's negotiation interface where it has one, so it
// gets the extensions and features it needs, or here.
func (v *vkOut) createDevice(neg *vkNegotiation, g vkGPUInfo) error {
	v.gpu = g.handle
	var ctx vkRetroContext
	switch {
	case neg != nil && neg.interfaceVersion >= 2 && neg.createDevice2 != 0:
		if call(neg.createDevice2, uintptr(unsafe.Pointer(&ctx)), v.instance, g.handle, uintptr(v.surface), v.f.getInstanceProcAddr, vkCallbacks.deviceWrapper, 0)&0xff == 0 {
			return fmt.Errorf("%w: the core could not make its Vulkan device", ErrNoVulkan)
		}
	case neg != nil && neg.createDevice != 0:
		if call(neg.createDevice, uintptr(unsafe.Pointer(&ctx)), v.instance, g.handle, uintptr(v.surface), v.f.getInstanceProcAddr, 0, 0, 0, 0, 0)&0xff == 0 {
			return fmt.Errorf("%w: the core could not make its Vulkan device", ErrNoVulkan)
		}
	default:
		family, ok := v.graphicsFamily(g.handle)
		if !ok {
			return fmt.Errorf("%w: %s has no graphics queue", ErrNoVulkan, g.name)
		}
		// Every feature the card has, except robust buffer access, which costs speed and which
		// cores that need features ask for through negotiation.
		var feats [vkPhysicalDeviceFeaturesSize]byte
		call(v.f.getPhysicalDeviceFeatures, g.handle, uintptr(unsafe.Pointer(&feats[0])))
		feats[0], feats[1], feats[2], feats[3] = 0, 0, 0, 0
		prio := float32(1)
		q := vkDeviceQueueCreateInfo{sType: vkStructDeviceQueueCreateInfo, queueFamilyIndex: family, queueCount: 1, pQueuePriorities: &prio}
		info := vkDeviceCreateInfo{sType: vkStructDeviceCreateInfo, queueCreateInfoCount: 1, pQueueCreateInfos: &q, pEnabledFeatures: uintptr(unsafe.Pointer(&feats[0]))}
		if r := call(v.f.createDevice, g.handle, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&ctx.device))); int32(r) != vkSuccess {
			return fmt.Errorf("%w: vkCreateDevice failed (%d)", ErrNoVulkan, int32(r))
		}
		ctx.gpu, ctx.queueFamilyIndex, v.ownDev = g.handle, family, true
	}
	if ctx.device == 0 {
		return fmt.Errorf("%w: no device was made", ErrNoVulkan)
	}
	if ctx.gpu != 0 {
		v.gpu = ctx.gpu
	}
	v.device, v.family, v.queue = ctx.device, ctx.queueFamilyIndex, ctx.queue
	if err := v.loadDeviceFuncs(); err != nil {
		return err
	}
	if v.queue == 0 {
		call(v.f.getDeviceQueue, v.device, uintptr(v.family), 0, uintptr(unsafe.Pointer(&v.queue)))
	}
	call(v.f.getPhysicalDeviceMemoryProperties, v.gpu, uintptr(unsafe.Pointer(&v.memProps[0])))
	return nil
}

// setUp makes the per-frame objects: a command buffer and a fence for each sync index.
func (v *vkOut) setUp() error {
	pi := vkCommandPoolCreateInfo{sType: vkStructCommandPoolCreateInfo, flags: vkCommandPoolResetBuffer, queueFamilyIndex: v.family}
	if r := call(v.f.createCommandPool, v.device, uintptr(unsafe.Pointer(&pi)), 0, uintptr(unsafe.Pointer(&v.pool))); int32(r) != vkSuccess {
		return fmt.Errorf("vkCreateCommandPool failed (%d)", int32(r))
	}
	cmds := make([]uintptr, vkSyncSlots)
	ai := vkCommandBufferAllocateInfo{sType: vkStructCommandBufferAllocInfo, commandPool: v.pool, level: vkCommandBufferLevelPrimary, commandBufferCount: vkSyncSlots}
	if r := call(v.f.allocateCommandBuffers, v.device, uintptr(unsafe.Pointer(&ai)), uintptr(unsafe.Pointer(&cmds[0]))); int32(r) != vkSuccess {
		return fmt.Errorf("vkAllocateCommandBuffers failed (%d)", int32(r))
	}
	for i := range v.slots {
		v.slots[i].cmd = cmds[i]
		fi := vkFenceCreateInfo{sType: vkStructFenceCreateInfo, flags: vkFenceCreateSignaled}
		if r := call(v.f.createFence, v.device, uintptr(unsafe.Pointer(&fi)), 0, uintptr(unsafe.Pointer(&v.slots[i].fence))); int32(r) != vkSuccess {
			return fmt.Errorf("vkCreateFence failed (%d)", int32(r))
		}
	}
	makeVKCallbacks()
	cb := &vkCallbacks
	v.iface = vkInterface{interfaceType: vkHWRenderInterfaceVulkan, interfaceVersion: vkHWRenderInterfaceVersion, handle: 1,
		instance: v.instance, gpu: v.gpu, device: v.device, getDeviceProcAddr: v.f.getDeviceProcAddr, getInstanceProcAddr: v.f.getInstanceProcAddr,
		queue: v.queue, queueIndex: v.family, setImage: cb.setImage, getSyncIndex: cb.getSyncIndex, getSyncIndexMask: cb.getSyncIndexMask,
		setCommandBuffers: cb.setCommandBuffers, waitSyncIndex: cb.waitSyncIndex, lockQueue: cb.lockQueue, unlockQueue: cb.unlockQueue,
		setSignalSemaphore: cb.setSignalSemaphore}
	return nil
}

// newVKOut makes a device for a core, or says why it can't.
func newVKOut(neg *vkNegotiation) (*vkOut, error) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return nil, fmt.Errorf("%w: offered on 64-bit systems only", ErrNoVulkan)
	}
	makeVKCallbacks()
	v, err := loadVK()
	if err != nil {
		return nil, err
	}
	vkCurrentMu.Lock()
	vkCurrent = v // the wrappers the core calls while making its device find it here
	vkCurrentMu.Unlock()
	fail := func(err error) (*vkOut, error) {
		v.destroy()
		return nil, err
	}
	v.neg = neg
	if err := v.createInstance(neg); err != nil {
		return fail(err)
	}
	g, ok := pickGPU(v.gpus())
	if !ok {
		return fail(fmt.Errorf("%w: Vulkan here is only the processor's", ErrNoVulkan))
	}
	v.renderer = g.name + " (Vulkan)"
	// A surface for the cores that want one (Dolphin); without one they can't present, so for
	// them it's the same as no Vulkan, and they'd better have OpenGL. Others don't mind.
	if v.surfaceExts {
		_ = v.createSurface()
	}
	if err := v.createDevice(neg, g); err != nil {
		return fail(err)
	}
	if err := v.setUp(); err != nil {
		return fail(err)
	}
	return v, nil
}

// vkProbe names the Vulkan card a core would be given, checked once, without making a device: an
// instance and the list of cards.
var vkProbe = sync.OnceValues(func() (string, error) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return "", fmt.Errorf("%w: offered on 64-bit systems only", ErrNoVulkan)
	}
	v, err := loadVK()
	if err != nil {
		return "", err
	}
	defer v.destroy()
	if err := v.createInstance(nil); err != nil {
		return "", err
	}
	g, ok := pickGPU(v.gpus())
	if !ok {
		return "", fmt.Errorf("%w: Vulkan here is only the processor's", ErrNoVulkan)
	}
	return g.name, nil
})

// vkUsable says whether a Vulkan card can be had here at all.
func vkUsable() error {
	_, err := vkProbe()
	return err
}

func (v *vkOut) setImage(img *vkRetroImage, numSems uint32, sems uintptr) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if img == nil {
		v.image = nil
		return
	}
	v.imageCopy = *img
	v.image = &v.imageCopy
	v.waitSems = v.waitSems[:0]
	if numSems > 0 && sems != 0 {
		v.waitSems = append(v.waitSems, unsafe.Slice((*uint64)(ptr(sems)), int(numSems))...)
	}
}

// waitSyncIndex is the core asking for what the current sync index held before to be done with.
func (v *vkOut) waitSyncIndex() {
	v.mu.Lock()
	s := &v.slots[v.index]
	fence, wait := s.fence, s.submitted
	v.mu.Unlock()
	if wait {
		call(v.f.waitForFences, v.device, 1, uintptr(unsafe.Pointer(&fence)), 1, vkMaxArg)
	}
}

func (v *vkOut) memoryType(bits uint32, want, avoid uint32) (uint32, bool) {
	count := *(*uint32)(unsafe.Pointer(&v.memProps[0]))
	best, found := uint32(0), false
	for i := uint32(0); i < count && i < 32; i++ {
		flags := *(*uint32)(unsafe.Pointer(&v.memProps[4+8*i]))
		if bits&(1<<i) == 0 || flags&want != want {
			continue
		}
		if flags&avoid == 0 {
			return i, true
		}
		if !found {
			best, found = i, true
		}
	}
	return best, found
}

// ensureBuffer gives a slot a buffer the processor can read of at least n bytes, mapped for good.
func (v *vkOut) ensureBuffer(s *vkSlot, n uint64) error {
	if s.buf != 0 && s.bufSize >= n {
		return nil
	}
	v.freeBuffer(s)
	bi := vkBufferCreateInfo{sType: vkStructBufferCreateInfo, size: n, usage: vkBufferUsageTransferDst, sharingMode: vkSharingExclusive}
	if r := call(v.f.createBuffer, v.device, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&s.buf))); int32(r) != vkSuccess {
		return fmt.Errorf("vkCreateBuffer failed (%d)", int32(r))
	}
	var req vkMemoryRequirements
	call(v.f.getBufferMemoryRequirements, v.device, uintptr(s.buf), uintptr(unsafe.Pointer(&req)))
	// Cached memory reads fast; uncached memory, read a byte at a time by the processor, is slow.
	t, ok := v.memoryType(req.memoryTypeBits, vkMemoryHostVisible|vkMemoryHostCoherent|vkMemoryHostCached, 0)
	if !ok {
		if t, ok = v.memoryType(req.memoryTypeBits, vkMemoryHostVisible|vkMemoryHostCoherent, 0); !ok {
			return errors.New("no memory the processor can read")
		}
	}
	ai := vkMemoryAllocateInfo{sType: vkStructMemoryAllocateInfo, allocationSize: req.size, memoryTypeIndex: t}
	if r := call(v.f.allocateMemory, v.device, uintptr(unsafe.Pointer(&ai)), 0, uintptr(unsafe.Pointer(&s.bufMem))); int32(r) != vkSuccess {
		return fmt.Errorf("vkAllocateMemory failed (%d)", int32(r))
	}
	call(v.f.bindBufferMemory, v.device, uintptr(s.buf), uintptr(s.bufMem), 0)
	if r := call(v.f.mapMemory, v.device, uintptr(s.bufMem), 0, vkMaxArg, 0, uintptr(unsafe.Pointer(&s.bufMapped))); int32(r) != vkSuccess {
		return fmt.Errorf("vkMapMemory failed (%d)", int32(r))
	}
	s.bufSize = n
	return nil
}

func (v *vkOut) freeBuffer(s *vkSlot) {
	if s.bufMapped != 0 {
		call(v.f.unmapMemory, v.device, uintptr(s.bufMem))
		s.bufMapped = 0
	}
	if s.buf != 0 {
		call(v.f.destroyBuffer, v.device, uintptr(s.buf), 0)
		s.buf = 0
	}
	if s.bufMem != 0 {
		call(v.f.freeMemory, v.device, uintptr(s.bufMem), 0)
		s.bufMem = 0
	}
	s.bufSize = 0
}

// ensureBlit gives a slot an image in B8G8R8A8 to blit other formats into: the graphics card
// converts, rather than the processor afterwards.
func (v *vkOut) ensureBlit(s *vkSlot, w, h, format uint32) error {
	if s.blit != 0 && s.blitW == w && s.blitH == h && s.blitFormat == format {
		return nil
	}
	v.freeBlit(s)
	ii := vkImageCreateInfo{sType: vkStructImageCreateInfo, imageType: vkImageType2D, format: format, extent: vkExtent3D{w, h, 1}, mipLevels: 1, arrayLayers: 1,
		samples: vkSampleCount1, tiling: vkTilingOptimal, usage: vkImageUsageTransferSrc | vkImageUsageTransferDst, sharingMode: vkSharingExclusive, initialLayout: vkLayoutUndefined}
	if r := call(v.f.createImage, v.device, uintptr(unsafe.Pointer(&ii)), 0, uintptr(unsafe.Pointer(&s.blit))); int32(r) != vkSuccess {
		return fmt.Errorf("vkCreateImage failed (%d)", int32(r))
	}
	var req vkMemoryRequirements
	call(v.f.getImageMemoryRequirements, v.device, uintptr(s.blit), uintptr(unsafe.Pointer(&req)))
	t, ok := v.memoryType(req.memoryTypeBits, 0, vkMemoryHostVisible)
	if !ok {
		return errors.New("no memory for an image")
	}
	ai := vkMemoryAllocateInfo{sType: vkStructMemoryAllocateInfo, allocationSize: req.size, memoryTypeIndex: t}
	if r := call(v.f.allocateMemory, v.device, uintptr(unsafe.Pointer(&ai)), 0, uintptr(unsafe.Pointer(&s.blitMem))); int32(r) != vkSuccess {
		return fmt.Errorf("vkAllocateMemory failed (%d)", int32(r))
	}
	call(v.f.bindImageMemory, v.device, uintptr(s.blit), uintptr(s.blitMem), 0)
	s.blitW, s.blitH, s.blitFormat = w, h, format
	return nil
}

func (v *vkOut) freeBlit(s *vkSlot) {
	if s.blit != 0 {
		call(v.f.destroyImage, v.device, uintptr(s.blit), 0)
		s.blit = 0
	}
	if s.blitMem != 0 {
		call(v.f.freeMemory, v.device, uintptr(s.blitMem), 0)
		s.blitMem = 0
	}
}

func barrier(img uint64, rng vkImageSubresourceRange, oldL, newL, srcAccess, dstAccess uint32) vkImageMemoryBarrier {
	return vkImageMemoryBarrier{sType: vkStructImageMemoryBarrier, srcAccessMask: srcAccess, dstAccessMask: dstAccess, oldLayout: oldL, newLayout: newL,
		srcQueueFamilyIndex: vkQueueFamilyIgnored, dstQueueFamilyIndex: vkQueueFamilyIgnored, image: img, subresourceRange: rng}
}

func (v *vkOut) cmdBarrier(cmd uintptr, src, dst uint32, b *vkImageMemoryBarrier) {
	call(v.f.cmdPipelineBarrier, cmd, uintptr(src), uintptr(dst), 0, 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(b)))
}

// startRead copies the frame the core presented with set_image into the current sync index's
// buffer, after whatever the core has submitted, and moves on to the next index. It waits for
// nothing: the frame is handed over by collect. It can be called from whichever thread the core
// presents from.
func (v *vkOut) startRead(w, h int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.gone || v.image == nil || w <= 0 || h <= 0 {
		return
	}
	img := v.image.createInfo.image
	layout := v.image.layout
	format := v.image.createInfo.format
	sr := v.image.createInfo.subresourceRange
	if sr.levelCount == 0 {
		sr.levelCount = 1
	}
	if sr.layerCount == 0 {
		sr.layerCount = 1
	}
	s := &v.slots[v.index]
	// The slot's last frame went two frames ago; it has to be done before its buffer is reused.
	if s.submitted {
		call(v.f.waitForFences, v.device, 1, uintptr(unsafe.Pointer(&s.fence)), 1, vkMaxArg)
	}
	n := uint64(w) * uint64(h) * 4
	if err := v.ensureBuffer(s, n); err != nil {
		return
	}
	cmd := s.cmd
	call(v.f.resetCommandBuffer, cmd, 0)
	bi := vkCommandBufferBeginInfo{sType: vkStructCommandBufferBeginInfo, flags: vkCommandBufferOneTimeSubmit}
	call(v.f.beginCommandBuffer, cmd, uintptr(unsafe.Pointer(&bi)))
	src := img
	if layout != vkLayoutGeneral {
		b := barrier(img, sr, layout, vkLayoutTransferSrc, vkAccessColorAttachmentWrite|vkAccessMemoryWrite, vkAccessTransferRead)
		v.cmdBarrier(cmd, vkStageAllCommands, vkStageTransfer, &b)
	}
	srcLayout := uint32(vkLayoutTransferSrc)
	if layout == vkLayoutGeneral {
		srcLayout = vkLayoutGeneral
	}
	layers := vkImageSubresourceLayers{vkAspectColor, sr.baseMipLevel, sr.baseArrayLayer, 1}
	if format != vkFormatB8G8R8A8Unorm && format != vkFormatB8G8R8A8Srgb {
		// The card converts to B8G8R8A8 by blitting into an image of that format: an sRGB frame into
		// an sRGB one, so nothing changes but the order of the channels.
		dstFormat := uint32(vkFormatB8G8R8A8Unorm)
		if format == vkFormatR8G8B8A8Srgb {
			dstFormat = vkFormatB8G8R8A8Srgb
		}
		if err := v.ensureBlit(s, uint32(w), uint32(h), dstFormat); err != nil {
			call(v.f.endCommandBuffer, cmd)
			return
		}
		one := vkImageSubresourceRange{vkAspectColor, 0, 1, 0, 1}
		b := barrier(s.blit, one, vkLayoutUndefined, vkLayoutTransferDst, 0, vkAccessTransferWrite)
		v.cmdBarrier(cmd, vkStageTopOfPipe, vkStageTransfer, &b)
		region := vkImageBlit{srcSubresource: layers, srcOffsets: [2]vkOffset3D{{0, 0, 0}, {int32(w), int32(h), 1}},
			dstSubresource: vkImageSubresourceLayers{vkAspectColor, 0, 0, 1}, dstOffsets: [2]vkOffset3D{{0, 0, 0}, {int32(w), int32(h), 1}}}
		call(v.f.cmdBlitImage, cmd, uintptr(img), uintptr(srcLayout), uintptr(s.blit), vkLayoutTransferDst, 1, uintptr(unsafe.Pointer(&region)), vkFilterNearest)
		b = barrier(s.blit, one, vkLayoutTransferDst, vkLayoutTransferSrc, vkAccessTransferWrite, vkAccessTransferRead)
		v.cmdBarrier(cmd, vkStageTransfer, vkStageTransfer, &b)
		src, srcLayout, layers = s.blit, vkLayoutTransferSrc, vkImageSubresourceLayers{vkAspectColor, 0, 0, 1}
	}
	region := vkBufferImageCopy{imageSubresource: layers, imageExtent: vkExtent3D{uint32(w), uint32(h), 1}}
	call(v.f.cmdCopyImageToBuffer, cmd, uintptr(src), uintptr(srcLayout), uintptr(s.buf), 1, uintptr(unsafe.Pointer(&region)))
	if layout != vkLayoutGeneral {
		b := barrier(img, sr, vkLayoutTransferSrc, layout, vkAccessTransferRead, vkAccessShaderRead|vkAccessColorAttachmentWrite)
		v.cmdBarrier(cmd, vkStageTransfer, vkStageAllCommands, &b)
	}
	call(v.f.endCommandBuffer, cmd)

	cmds := append(append([]uintptr(nil), v.cmds...), cmd)
	si := vkSubmitInfo{sType: vkStructSubmitInfo, commandBufferCount: uint32(len(cmds)), pCommandBuffers: uintptr(unsafe.Pointer(&cmds[0]))}
	var stages []uint32
	if len(v.cmds) == 0 && len(v.waitSems) > 0 {
		stages = make([]uint32, len(v.waitSems))
		for i := range stages {
			stages[i] = vkStageTransfer
		}
		si.waitSemaphoreCount, si.pWaitSemaphores, si.pWaitDstStageMask = uint32(len(v.waitSems)), uintptr(unsafe.Pointer(&v.waitSems[0])), uintptr(unsafe.Pointer(&stages[0]))
	}
	signal := v.signal
	if signal != 0 {
		si.signalSemaphoreCount, si.pSignalSemaphores = 1, uintptr(unsafe.Pointer(&signal))
	}
	call(v.f.resetFences, v.device, 1, uintptr(unsafe.Pointer(&s.fence)))
	v.queueMu.Lock()
	r := call(v.f.queueSubmit, v.queue, 1, uintptr(unsafe.Pointer(&si)), uintptr(s.fence))
	v.queueMu.Unlock()
	// A semaphore is waited on once; the command buffers are submitted once.
	v.waitSems, v.cmds, v.signal = v.waitSems[:0], v.cmds[:0], 0
	if int32(r) != vkSuccess {
		s.submitted, s.pending = false, false
		return
	}
	v.seq++
	s.submitted, s.pending, s.seq, s.w, s.h = true, true, v.seq, w, h
	v.index = (v.index + 1) % vkSyncSlots
}

// collect hands over the newest frame the card has finished, and says whether one is still on its
// way. Frames older than the one handed over are left out: the newest is what a player should see.
// A core that presents from a thread of its own (LRPS2) has always just submitted another by the
// time this looks, so it's the newest finished frame that goes, not the newest asked for. When
// none has finished, it waits at most wait for the oldest. It runs on the core's thread.
func (v *vkOut) collect(h engine.Handlers, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		v.mu.Lock()
		done, anyPending := -1, false
		for i := range v.slots {
			s := &v.slots[i]
			if !s.pending {
				continue
			}
			anyPending = true
			if done >= 0 && s.seq < v.slots[done].seq {
				continue
			}
			if r := call(v.f.getFenceStatus, v.device, uintptr(s.fence)); int32(r) == vkSuccess {
				done = i
			}
		}
		if done < 0 {
			v.mu.Unlock()
			if !anyPending || !time.Now().Before(deadline) {
				return anyPending
			}
			time.Sleep(glPollStep)
			continue
		}
		s := &v.slots[done]
		for i := range v.slots {
			if o := &v.slots[i]; o.pending && o.seq <= s.seq {
				o.pending = false
			}
		}
		n := s.w * s.h * 4
		if cap(v.copied) < n {
			v.copied = make([]byte, n)
		}
		// Out of the mapped memory now, so the slot can be reused as soon as the lock is let go.
		data := v.copied[:n]
		copy(data, unsafe.Slice((*byte)(ptr(s.bufMapped)), n))
		w, ht := s.w, s.h
		v.mu.Unlock()
		if h.Video != nil {
			h.Video(data, w, ht, w*4, engine.FormatXRGB8888)
		}
		return false
	}
}

func (v *vkOut) destroy() {
	if v == nil {
		return
	}
	v.mu.Lock()
	v.gone = true
	v.mu.Unlock()
	if v.device != 0 && v.f.deviceWaitIdle != 0 {
		v.queueMu.Lock()
		call(v.f.deviceWaitIdle, v.device)
		v.queueMu.Unlock()
	}
	if v.device != 0 && v.f.destroyFence != 0 {
		for i := range v.slots {
			s := &v.slots[i]
			v.freeBuffer(s)
			v.freeBlit(s)
			if s.fence != 0 {
				call(v.f.destroyFence, v.device, uintptr(s.fence), 0)
				s.fence = 0
			}
		}
		if v.pool != 0 {
			call(v.f.destroyCommandPool, v.device, uintptr(v.pool), 0)
			v.pool = 0
		}
	}
	if v.device != 0 {
		switch {
		case v.ownDev && v.f.destroyDevice != 0:
			call(v.f.destroyDevice, v.device, 0)
		case v.neg != nil && v.neg.destroyDevice != 0:
			call(v.neg.destroyDevice)
		case v.f.destroyDevice != 0:
			call(v.f.destroyDevice, v.device, 0)
		}
		v.device = 0
	}
	if v.surface != 0 {
		if destroy := v.instanceProc(v.instance, "vkDestroySurfaceKHR"); destroy != 0 {
			call(destroy, v.instance, uintptr(v.surface), 0)
		}
		v.surface = 0
	}
	v.destroyWindow()
	if v.instance != 0 && v.f.destroyInstance != 0 {
		call(v.f.destroyInstance, v.instance, 0)
		v.instance = 0
	}
	vkCurrentMu.Lock()
	if vkCurrent == v {
		vkCurrent = nil
	}
	vkCurrentMu.Unlock()
}

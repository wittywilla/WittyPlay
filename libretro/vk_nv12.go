// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	_ "embed"
	"fmt"
	"unsafe"
)

// The frame a core presents, turned into NV12 on the graphics card by a compute shader
// (shaders/nv12.comp) and written straight into the buffer the processor reads: a third of the
// bytes of B8G8R8A8, in the form the encoder takes, so nothing converts it on the processor.

//go:embed shaders/nv12.spv
var nv12SPIRV []byte

// nv12Wanted turns the conversion on; the runner's -nv12 sets it (on unless an administrator
// turned "Convert frames on the graphics card" off, or a viewer needs the frame stamps drawn in
// colour).
var nv12Wanted bool

// WantNV12 asks for frames as NV12 where the card can convert them.
func WantNV12(on bool) { nv12Wanted = on }

const (
	vkStructShaderModuleCreateInfo        = 16
	vkStructPipelineShaderStageCreateInfo = 18
	vkStructComputePipelineCreateInfo     = 29
	vkStructPipelineLayoutCreateInfo      = 30
	vkStructSamplerCreateInfo             = 31
	vkStructDescriptorSetLayoutCreateInfo = 32
	vkStructDescriptorPoolCreateInfo      = 33
	vkStructDescriptorSetAllocateInfo     = 34
	vkStructWriteDescriptorSet            = 35
	vkStructBufferMemoryBarrier           = 44
	vkDescriptorCombinedImageSampler      = 1
	vkDescriptorStorageBuffer             = 7
	vkShaderStageCompute                  = 0x20
	vkStageComputeShader                  = 0x800
	vkAccessShaderWrite                   = 0x40
	vkBindPointCompute                    = 1
	vkBufferUsageStorage                  = 0x20
	vkAddressClampToEdge                  = 2
	nv12PushSize                          = 20
)

type vkShaderModuleCreateInfo struct {
	sType    uint32
	pNext    uintptr
	flags    uint32
	codeSize uintptr
	pCode    uintptr
}

type vkDescriptorSetLayoutBinding struct {
	binding            uint32
	descriptorType     uint32
	descriptorCount    uint32
	stageFlags         uint32
	pImmutableSamplers uintptr
}

type vkDescriptorSetLayoutCreateInfo struct {
	sType        uint32
	pNext        uintptr
	flags        uint32
	bindingCount uint32
	pBindings    uintptr
}

type vkPushConstantRange struct{ stageFlags, offset, size uint32 }

type vkPipelineLayoutCreateInfo struct {
	sType                  uint32
	pNext                  uintptr
	flags                  uint32
	setLayoutCount         uint32
	pSetLayouts            uintptr
	pushConstantRangeCount uint32
	pPushConstantRanges    uintptr
}

type vkPipelineShaderStageCreateInfo struct {
	sType               uint32
	pNext               uintptr
	flags               uint32
	stage               uint32
	module              uint64
	pName               uintptr
	pSpecializationInfo uintptr
}

type vkComputePipelineCreateInfo struct {
	sType              uint32
	pNext              uintptr
	flags              uint32
	stage              vkPipelineShaderStageCreateInfo
	layout             uint64
	basePipelineHandle uint64
	basePipelineIndex  int32
}

type vkDescriptorPoolSize struct{ kind, count uint32 }

type vkDescriptorPoolCreateInfo struct {
	sType         uint32
	pNext         uintptr
	flags         uint32
	maxSets       uint32
	poolSizeCount uint32
	pPoolSizes    uintptr
}

type vkDescriptorSetAllocateInfo struct {
	sType              uint32
	pNext              uintptr
	descriptorPool     uint64
	descriptorSetCount uint32
	pSetLayouts        uintptr
}

type vkDescriptorImageInfo struct {
	sampler, imageView uint64
	imageLayout        uint32
}

type vkDescriptorBufferInfo struct{ buffer, offset, rng uint64 }

type vkWriteDescriptorSet struct {
	sType            uint32
	pNext            uintptr
	dstSet           uint64
	dstBinding       uint32
	dstArrayElement  uint32
	descriptorCount  uint32
	descriptorType   uint32
	pImageInfo       uintptr
	pBufferInfo      uintptr
	pTexelBufferView uintptr
}

type vkSamplerCreateInfo struct {
	sType                   uint32
	pNext                   uintptr
	flags                   uint32
	magFilter, minFilter    uint32
	mipmapMode              uint32
	addressU, addressV      uint32
	addressW                uint32
	mipLodBias              float32
	anisotropyEnable        uint32
	maxAnisotropy           float32
	compareEnable           uint32
	compareOp               uint32
	minLod, maxLod          float32
	borderColor             uint32
	unnormalizedCoordinates uint32
}

type vkBufferMemoryBarrier struct {
	sType               uint32
	pNext               uintptr
	srcAccessMask       uint32
	dstAccessMask       uint32
	srcQueueFamilyIndex uint32
	dstQueueFamilyIndex uint32
	buffer              uint64
	offset              uint64
	size                uint64
}

// The functions the conversion uses.
type vkNV12Funcs struct {
	createShaderModule, destroyShaderModule, createDescriptorSetLayout, destroyDescriptorSetLayout uintptr
	createPipelineLayout, destroyPipelineLayout, createComputePipelines, destroyPipeline           uintptr
	createDescriptorPool, destroyDescriptorPool, allocateDescriptorSets, updateDescriptorSets      uintptr
	createSampler, destroySampler, cmdBindPipeline, cmdBindDescriptorSets, cmdPushConstants        uintptr
	cmdDispatch                                                                                    uintptr
}

// vkNV12 is the compute pipeline, and a descriptor set for each sync index.
type vkNV12 struct {
	f                                   vkNV12Funcs
	module, setLayout, layout, pipeline uint64
	pool, sampler                       uint64
	sets                                [vkSyncSlots]uint64
}

func newVKNV12(v *vkOut) (*vkNV12, error) {
	n := &vkNV12{}
	for name, slot := range map[string]*uintptr{
		"vkCreateShaderModule": &n.f.createShaderModule, "vkDestroyShaderModule": &n.f.destroyShaderModule,
		"vkCreateDescriptorSetLayout": &n.f.createDescriptorSetLayout, "vkDestroyDescriptorSetLayout": &n.f.destroyDescriptorSetLayout,
		"vkCreatePipelineLayout": &n.f.createPipelineLayout, "vkDestroyPipelineLayout": &n.f.destroyPipelineLayout,
		"vkCreateComputePipelines": &n.f.createComputePipelines, "vkDestroyPipeline": &n.f.destroyPipeline,
		"vkCreateDescriptorPool": &n.f.createDescriptorPool, "vkDestroyDescriptorPool": &n.f.destroyDescriptorPool,
		"vkAllocateDescriptorSets": &n.f.allocateDescriptorSets, "vkUpdateDescriptorSets": &n.f.updateDescriptorSets,
		"vkCreateSampler": &n.f.createSampler, "vkDestroySampler": &n.f.destroySampler, "vkCmdBindPipeline": &n.f.cmdBindPipeline,
		"vkCmdBindDescriptorSets": &n.f.cmdBindDescriptorSets, "vkCmdPushConstants": &n.f.cmdPushConstants, "vkCmdDispatch": &n.f.cmdDispatch,
	} {
		if *slot = call(v.f.getDeviceProcAddr, v.device, uintptr(unsafe.Pointer(vkStr(name)))); *slot == 0 {
			return nil, fmt.Errorf("the device has no %s", name)
		}
	}
	fail := func(what string, r uintptr) (*vkNV12, error) {
		n.destroy(v)
		return nil, fmt.Errorf("%s failed (%d)", what, int32(r))
	}
	code := make([]uint32, len(nv12SPIRV)/4) // aligned for the driver
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&code[0])), len(nv12SPIRV)), nv12SPIRV)
	mi := vkShaderModuleCreateInfo{sType: vkStructShaderModuleCreateInfo, codeSize: uintptr(len(nv12SPIRV)), pCode: uintptr(unsafe.Pointer(&code[0]))}
	if r := call(n.f.createShaderModule, v.device, uintptr(unsafe.Pointer(&mi)), 0, uintptr(unsafe.Pointer(&n.module))); int32(r) != vkSuccess {
		return fail("vkCreateShaderModule", r)
	}
	si := vkSamplerCreateInfo{sType: vkStructSamplerCreateInfo, magFilter: vkFilterNearest, minFilter: vkFilterNearest,
		addressU: vkAddressClampToEdge, addressV: vkAddressClampToEdge, addressW: vkAddressClampToEdge, maxAnisotropy: 1}
	if r := call(n.f.createSampler, v.device, uintptr(unsafe.Pointer(&si)), 0, uintptr(unsafe.Pointer(&n.sampler))); int32(r) != vkSuccess {
		return fail("vkCreateSampler", r)
	}
	bindings := [2]vkDescriptorSetLayoutBinding{
		{binding: 0, descriptorType: vkDescriptorCombinedImageSampler, descriptorCount: 1, stageFlags: vkShaderStageCompute},
		{binding: 1, descriptorType: vkDescriptorStorageBuffer, descriptorCount: 1, stageFlags: vkShaderStageCompute},
	}
	li := vkDescriptorSetLayoutCreateInfo{sType: vkStructDescriptorSetLayoutCreateInfo, bindingCount: 2, pBindings: uintptr(unsafe.Pointer(&bindings[0]))}
	if r := call(n.f.createDescriptorSetLayout, v.device, uintptr(unsafe.Pointer(&li)), 0, uintptr(unsafe.Pointer(&n.setLayout))); int32(r) != vkSuccess {
		return fail("vkCreateDescriptorSetLayout", r)
	}
	pc := vkPushConstantRange{stageFlags: vkShaderStageCompute, size: nv12PushSize}
	pli := vkPipelineLayoutCreateInfo{sType: vkStructPipelineLayoutCreateInfo, setLayoutCount: 1, pSetLayouts: uintptr(unsafe.Pointer(&n.setLayout)),
		pushConstantRangeCount: 1, pPushConstantRanges: uintptr(unsafe.Pointer(&pc))}
	if r := call(n.f.createPipelineLayout, v.device, uintptr(unsafe.Pointer(&pli)), 0, uintptr(unsafe.Pointer(&n.layout))); int32(r) != vkSuccess {
		return fail("vkCreatePipelineLayout", r)
	}
	ci := vkComputePipelineCreateInfo{sType: vkStructComputePipelineCreateInfo, layout: n.layout, basePipelineIndex: -1,
		stage: vkPipelineShaderStageCreateInfo{sType: vkStructPipelineShaderStageCreateInfo, stage: vkShaderStageCompute, module: n.module, pName: uintptr(unsafe.Pointer(vkStr("main")))}}
	if r := call(n.f.createComputePipelines, v.device, 0, 1, uintptr(unsafe.Pointer(&ci)), 0, uintptr(unsafe.Pointer(&n.pipeline))); int32(r) != vkSuccess {
		return fail("vkCreateComputePipelines", r)
	}
	sizes := [2]vkDescriptorPoolSize{{vkDescriptorCombinedImageSampler, vkSyncSlots}, {vkDescriptorStorageBuffer, vkSyncSlots}}
	pi := vkDescriptorPoolCreateInfo{sType: vkStructDescriptorPoolCreateInfo, maxSets: vkSyncSlots, poolSizeCount: 2, pPoolSizes: uintptr(unsafe.Pointer(&sizes[0]))}
	if r := call(n.f.createDescriptorPool, v.device, uintptr(unsafe.Pointer(&pi)), 0, uintptr(unsafe.Pointer(&n.pool))); int32(r) != vkSuccess {
		return fail("vkCreateDescriptorPool", r)
	}
	layouts := [vkSyncSlots]uint64{n.setLayout, n.setLayout, n.setLayout}
	ai := vkDescriptorSetAllocateInfo{sType: vkStructDescriptorSetAllocateInfo, descriptorPool: n.pool, descriptorSetCount: vkSyncSlots, pSetLayouts: uintptr(unsafe.Pointer(&layouts[0]))}
	if r := call(n.f.allocateDescriptorSets, v.device, uintptr(unsafe.Pointer(&ai)), uintptr(unsafe.Pointer(&n.sets[0]))); int32(r) != vkSuccess {
		return fail("vkAllocateDescriptorSets", r)
	}
	return n, nil
}

// record converts the image the core presented, w by h, into the slot's buffer as NV12 of
// outW by outH (padded to a multiple of 4 by an even number).
func (n *vkNV12) record(v *vkOut, cmd uintptr, s *vkSlot, img *vkRetroImage, w, h, outW, outH int) {
	set := n.sets[v.index]
	layout := img.layout
	ii := vkDescriptorImageInfo{sampler: n.sampler, imageView: img.imageView, imageLayout: layout}
	bi := vkDescriptorBufferInfo{buffer: s.buf, rng: vkWholeSize}
	writes := [2]vkWriteDescriptorSet{
		{sType: vkStructWriteDescriptorSet, dstSet: set, dstBinding: 0, descriptorCount: 1, descriptorType: vkDescriptorCombinedImageSampler, pImageInfo: uintptr(unsafe.Pointer(&ii))},
		{sType: vkStructWriteDescriptorSet, dstSet: set, dstBinding: 1, descriptorCount: 1, descriptorType: vkDescriptorStorageBuffer, pBufferInfo: uintptr(unsafe.Pointer(&bi))},
	}
	call(n.f.updateDescriptorSets, v.device, 2, uintptr(unsafe.Pointer(&writes[0])), 0, 0)
	// What the core drew has to be finished, and visible to the shader.
	sr := img.createInfo.subresourceRange
	if sr.levelCount == 0 {
		sr.levelCount = 1
	}
	if sr.layerCount == 0 {
		sr.layerCount = 1
	}
	b := barrier(img.createInfo.image, sr, layout, layout, vkAccessColorAttachmentWrite|vkAccessMemoryWrite, vkAccessShaderRead)
	v.cmdBarrier(cmd, vkStageAllCommands, vkStageComputeShader, &b)
	call(n.f.cmdBindPipeline, cmd, vkBindPointCompute, uintptr(n.pipeline))
	call(n.f.cmdBindDescriptorSets, cmd, vkBindPointCompute, uintptr(n.layout), 0, 1, uintptr(unsafe.Pointer(&set)), 0, 0)
	srgb := uint32(0)
	if f := img.createInfo.format; f == vkFormatB8G8R8A8Srgb || f == vkFormatR8G8B8A8Srgb {
		srgb = 1
	}
	push := [5]uint32{uint32(w), uint32(h), uint32(outW), uint32(outH), srgb}
	call(n.f.cmdPushConstants, cmd, uintptr(n.layout), vkShaderStageCompute, 0, nv12PushSize, uintptr(unsafe.Pointer(&push[0])))
	// Each invocation does 4 by 2 pixels, in groups of 8 by 8.
	call(n.f.cmdDispatch, cmd, uintptr((outW/4+7)/8), uintptr((outH/2+7)/8), 1)
	bb := vkBufferMemoryBarrier{sType: vkStructBufferMemoryBarrier, srcAccessMask: vkAccessShaderWrite, dstAccessMask: vkAccessHostRead,
		srcQueueFamilyIndex: vkQueueFamilyIgnored, dstQueueFamilyIndex: vkQueueFamilyIgnored, buffer: s.buf, size: vkWholeSize}
	call(v.f.cmdPipelineBarrier, cmd, vkStageComputeShader, vkStageHost, 0, 0, 0, 1, uintptr(unsafe.Pointer(&bb)), 0, 0)
}

func (n *vkNV12) destroy(v *vkOut) {
	d := v.device
	if n.pool != 0 {
		call(n.f.destroyDescriptorPool, d, uintptr(n.pool), 0)
	}
	if n.pipeline != 0 {
		call(n.f.destroyPipeline, d, uintptr(n.pipeline), 0)
	}
	if n.layout != 0 {
		call(n.f.destroyPipelineLayout, d, uintptr(n.layout), 0)
	}
	if n.setLayout != 0 {
		call(n.f.destroyDescriptorSetLayout, d, uintptr(n.setLayout), 0)
	}
	if n.sampler != 0 {
		call(n.f.destroySampler, d, uintptr(n.sampler), 0)
	}
	if n.module != 0 {
		call(n.f.destroyShaderModule, d, uintptr(n.module), 0)
	}
}

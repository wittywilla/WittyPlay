package libretro

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

// OpenGL on a Mac: a CGL context with no window, drawn by the Mac's own graphics processor
// (on Apple silicon, OpenGL is carried out through Metal). macOS stops at OpenGL 4.1 core, and
// has no OpenGL ES, so a core asking for more says so instead of failing later.

const openGLFramework = "/System/Library/Frameworks/OpenGL.framework/OpenGL"

// CGL's pixel format attributes, from CGLTypes.h.
const (
	cglPFAColorSize          = 8
	cglPFAAlphaSize          = 11
	cglPFADepthSize          = 12
	cglPFAStencilSize        = 13
	cglPFANoRecovery         = 72
	cglPFAAccelerated        = 73
	cglPFAAllowOffline       = 96 // a second card that isn't driving a display may be used
	cglPFAOpenGLProfile      = 99
	cglProfileLegacy         = 0x1000 // OpenGL 2.1
	cglProfileGL4Core        = 0x4100 // the newest core profile: 4.1 on every Mac that has one
	macMaxMajor, macMaxMinor = 4, 1
)

type cglPlatform struct {
	lib uintptr
	ctx uintptr
	f   struct{ choose, create, destroyPix, setCurrent, destroyCtx uintptr }
}

// glUnavailable is nil: whether a context can be made is found out when one is.
func glUnavailable() error { return nil }

// cglProfile is the profile for what a core asked for, or why it can't have one here.
func cglProfile(req glRequest) (int32, error) {
	switch {
	case req.es:
		return 0, fmt.Errorf("%w: this core asks for OpenGL ES, which macOS doesn't have", ErrNoGPU)
	case req.major > macMaxMajor || (req.major == macMaxMajor && req.minor > macMaxMinor):
		return 0, fmt.Errorf("%w: this core asks for OpenGL %d.%d, and macOS stops at %d.%d", ErrNoGPU, req.major, req.minor, macMaxMajor, macMaxMinor)
	case req.core || req.major >= 3:
		// A compatibility profile above 2.1 doesn't exist on a Mac; the core one is the nearest.
		return cglProfileGL4Core, nil
	}
	return cglProfileLegacy, nil
}

func newGLPlatform(req glRequest) (glPlatform, error) {
	profile, err := cglProfile(req)
	if err != nil {
		return nil, err
	}
	lib, err := purego.Dlopen(openGLFramework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("%w: OpenGL can't be opened here: %v", ErrNoGPU, err)
	}
	p := &cglPlatform{lib: lib}
	for name, slot := range map[string]*uintptr{
		"CGLChoosePixelFormat": &p.f.choose, "CGLCreateContext": &p.f.create, "CGLDestroyPixelFormat": &p.f.destroyPix,
		"CGLSetCurrentContext": &p.f.setCurrent, "CGLDestroyContext": &p.f.destroyCtx,
	} {
		a, err := purego.Dlsym(lib, name)
		if err != nil {
			return nil, fmt.Errorf("%w: OpenGL here has no %s", ErrNoGPU, name)
		}
		*slot = a
	}
	// A graphics processor, never Apple's software renderer: that's far too slow to play on.
	attribs := []int32{cglPFAOpenGLProfile, profile, cglPFAAccelerated, cglPFANoRecovery, cglPFAAllowOffline,
		cglPFAColorSize, 24, cglPFAAlphaSize, 8, cglPFADepthSize, 24, cglPFAStencilSize, 8, 0}
	var pix uintptr
	var n int32
	if e := call(p.f.choose, uintptr(unsafe.Pointer(&attribs[0])), uintptr(unsafe.Pointer(&pix)), uintptr(unsafe.Pointer(&n))); int32(e) != 0 || pix == 0 {
		return nil, fmt.Errorf("%w: no accelerated OpenGL pixel format (CGL error %d)", ErrNoGPU, int32(e))
	}
	e := call(p.f.create, pix, 0, uintptr(unsafe.Pointer(&p.ctx)))
	call(p.f.destroyPix, pix)
	if int32(e) != 0 || p.ctx == 0 {
		return nil, fmt.Errorf("%w: OpenGL wouldn't make a context (CGL error %d)", ErrNoGPU, int32(e))
	}
	if !p.makeCurrent() {
		p.destroy()
		return nil, errors.New("the OpenGL context couldn't be made current")
	}
	return p, nil
}

func prepareGPU() {}

func (p *cglPlatform) proc(name string) uintptr {
	a, err := purego.Dlsym(p.lib, name)
	if err != nil {
		return 0
	}
	return a
}

func (p *cglPlatform) makeCurrent() bool { return int32(call(p.f.setCurrent, p.ctx)) == 0 }

func (p *cglPlatform) destroy() {
	if p.ctx != 0 {
		call(p.f.setCurrent, 0)
		call(p.f.destroyCtx, p.ctx)
		p.ctx = 0
	}
}

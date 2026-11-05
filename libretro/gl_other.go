//go:build !windows && !linux

package libretro

import "fmt"

// errNoGLHere: this runner makes OpenGL contexts on Windows and Linux only.
var errNoGLHere = fmt.Errorf("%w: hardware-rendered games run on Windows and Linux servers", ErrNoGPU)

func newGLPlatform(req glRequest) (glPlatform, error) { return nil, errNoGLHere }

// glUnavailable says why no core can have a context here, whatever it asks for.
func glUnavailable() error { return errNoGLHere }

func prepareGPU() {}

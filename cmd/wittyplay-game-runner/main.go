// SPDX-License-Identifier: AGPL-3.0-or-later
//
// WittyPlay - Console games streamed to a browser or an app, with every controller and a touch layout editor.
// Copyright (C) 2026 WittyWilla and contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

// Command wittyplay-game-runner runs one game for WittyMediaServer: it loads a libretro
// core, or the built-in test core, and speaks the ipc protocol on stdin and
// stdout. It has no options a person needs. docs/GAMING.md explains why it is
// a process of its own.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/wittywilla/wittyplay/engine"
	"github.com/wittywilla/wittyplay/host"
	"github.com/wittywilla/wittyplay/hostsandbox"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/libretro"
	"github.com/wittywilla/wittyplay/testcore"
)

// boundedClose stops a core from holding the process open: everything has been
// saved by then, and some cores hang waiting for their own threads.
type boundedClose struct{ *libretro.Core }

func (b boundedClose) Close() {
	time.AfterFunc(5*time.Second, func() { os.Exit(0) })
	b.Core.Close()
}

func main() {
	saveDir := flag.String("save", "", "the only folder this process may write to")
	memory := flag.Int64("memory-mb", 0, "address space limit in MiB; 0 for none")
	gpu := flag.Bool("gpu", false, "this game draws with a graphics card, so allow what a driver needs")
	normal := flag.Bool("normal-priority", false, "this game can't play at low priority")
	sharedMemory := flag.Bool("shared-memory", false, "this core maps its code through /dev/shm, so allow files there")
	largeAddresses := flag.Bool("large-address-space", false, "this core reserves far more address space than it uses, so allow that")
	_ = flag.Bool("own-home", false, "this core has its save folder as a home folder, which whoever starts this sets up")
	probe := flag.Bool("probe-gpu", false, "say which graphics card games would draw with, and stop")
	nv12 := flag.Bool("nv12", false, "convert frames drawn on the graphics card to NV12 there, for the encoder")
	vkTrace := flag.Bool("vk-trace", false, "log what the core asks of Vulkan")
	coreFixes := flag.Bool("core-fixes", false, "make the runner's fixes to what cores get (libretro/vk_fixes.go)")
	vulkan := flag.Bool("vulkan", false, "this core may draw with Vulkan, where the graphics card can make a device")
	processorGL := flag.Bool("processor-gl", false, "this server has no graphics card, and this core can draw with OpenGL the processor runs")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "wittyplay-game-runner is started by WittyMediaServer")
		os.Exit(2)
	}
	// OpenGL the processor runs is still a graphics driver, with a driver's appetite for address
	// space, so it gets the limits a card's driver gets (hostsandbox.Limits.GPU): without them
	// Dolphin, its fast memory and llvmpipe couldn't fit, and the game shut itself down.
	if *processorGL {
		libretro.AllowProcessorGL()
	}
	libretro.TraceVulkan(*vkTrace)
	libretro.FixCores(*coreFixes)
	if *vulkan {
		libretro.AllowVulkan()
	}
	libretro.WantNV12(*nv12)
	limits := hostsandbox.Limits{SaveDir: *saveDir, MemoryMB: *memory, GPU: *gpu || *probe || *processorGL, Normal: *normal, SharedMemory: *sharedMemory, LargeAddressSpace: *largeAddresses}
	// This starts the program again, so it comes before anything that can't happen twice.
	if err := hostsandbox.Confine(limits); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
	}
	// Cores print to stdout, so the protocol goes elsewhere before one is loaded.
	out, err := hostsandbox.ClaimStdout()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wittyplay-game-runner:", err)
		os.Exit(1)
	}
	// The sandbox goes on from the thread that will run the core, as some of its
	// layers apply to one thread and whatever that thread starts.
	var runErr error
	err = libretro.OnCoreThread(func() {
		if err := hostsandbox.Apply(limits); err != nil {
			// Each layer is best effort, because kernels and containers differ. The
			// server's log shows which ones this machine lacks.
			fmt.Fprintln(os.Stderr, "sandbox:", err)
		}
		if *probe {
			info, err := libretro.ProbeGPU()
			if err != nil {
				fmt.Fprintln(out, "none:", err)
				return
			}
			// The card's name first, on a line of its own, as older servers read only that.
			fmt.Fprintln(out, "gpu:", info.Renderer)
			fmt.Fprintln(out, "vendor:", info.Vendor)
			fmt.Fprintln(out, "version:", info.Version)
			if info.VRAMMB > 0 {
				fmt.Fprintln(out, "vram-mb:", info.VRAMMB)
			}
			fmt.Fprintln(out, "vulkan:", info.Vulkan)
			// Straight out, with the graphics driver as it is: nothing is left to tidy, and the
			// driver's threads are stopped with the process rather than racing a teardown.
			_ = out.Sync()
			os.Exit(0)
		}
		runErr = host.Run(os.Stdin, out, func(l ipc.Load, h engine.Handlers) (engine.Engine, error) {
			if l.Core == ipc.TestCore {
				c := testcore.New(h)
				c.ApplyOptions(l.Options)
				return c, nil
			}
			c, err := libretro.Load(l.Core, libretro.Paths{System: l.System, Save: l.Save, Assets: l.Assets}, l.Options, h)
			if err != nil {
				return nil, err
			}
			return boundedClose{c}, nil
		})
	})
	if err == nil {
		err = runErr
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wittyplay-game-runner:", err)
		os.Exit(1)
	}
}

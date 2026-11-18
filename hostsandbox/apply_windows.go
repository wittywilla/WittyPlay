// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package hostsandbox

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Confine has nothing to do here: the server sets the limits from outside.
func Confine(l Limits) error { return nil }

// Apply does little on Windows: the server starts the host in a job object, with a
// low-integrity token, at below-normal priority. A demanding game asks for above-normal
// priority, which a process may always do for itself.
//
// Normal wasn't enough. A game is someone playing, now, and any other program at normal
// priority or above shared the processor with it equally: on a busy machine (browsers, which
// raise themselves to above normal, a transcode, a library scan) a Wii game ran at 2 to 13
// frames a second of its 60 while its own threads used a third of one processor thread,
// waiting for a turn. Above normal still gives way to anything the system holds to be more
// urgent, and a game uses a few threads at most, so the rest of the machine keeps going.
//
// Every game also opts out of Windows' power throttling for itself. The runner has no window of
// its own, so Windows 11 counts it as background work: it may put it on a laptop's efficiency
// cores (EcoQoS), and it ignores its requests for a fine timer, so an emulator that paces its
// frames and sound with short sleeps wakes late. Both made a Wii game's videos and sound choppy
// on a machine with plenty to spare. A process may always ask this of itself.
func Apply(l Limits) error {
	keepPace()
	if l.Normal {
		return windows.SetPriorityClass(windows.CurrentProcess(), windows.ABOVE_NORMAL_PRIORITY_CLASS)
	}
	return nil
}

var procSetProcessInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetProcessInformation")

// keepPace turns power throttling off for this process: execution speed (no EcoQoS) and timer
// resolution (its timer requests are honoured though it has no window). Older Windows without
// the call, or without the timer flag, keep what they did; nothing depends on it.
func keepPace() {
	if procSetProcessInformation.Find() != nil {
		return
	}
	const processPowerThrottling = 4 // PROCESS_INFORMATION_CLASS ProcessPowerThrottling
	const executionSpeed, ignoreTimerResolution = 0x1, 0x4
	for _, mask := range []uint32{executionSpeed | ignoreTimerResolution, executionSpeed} {
		// PROCESS_POWER_THROTTLING_STATE: Version 1, the controls taken over, and their state:
		// 0, throttling off.
		st := struct{ Version, ControlMask, StateMask uint32 }{1, mask, 0}
		if r, _, _ := procSetProcessInformation.Call(uintptr(windows.CurrentProcess()), processPowerThrottling,
			uintptr(unsafe.Pointer(&st)), unsafe.Sizeof(st)); r != 0 {
			return
		}
	}
}

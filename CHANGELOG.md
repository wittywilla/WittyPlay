# Changelog

What changed in WittyPlay, newest first. What changed before it had a repository of its own is in the
[WittyMediaServer changelog](https://github.com/wittywilla/WittyMediaServer/blob/master/CHANGELOG.md).

## Unreleased

- **WittyPlay 0.3.5:** steadier streaming on a busy machine, and `wittyplay-probe` builds on macOS and FreeBSD, so CI is green again. There it reads a process's memory from `ps`, and has no count of open files.
- **"The connection dropped" no longer comes up for a picture that's only late.** The player started a new connection whenever the picture stopped for 4 s, which on a busy machine was every few minutes, though the connection was fine. It now waits while the server's own watchdogs bring the picture back, says it's waiting, and starts again only when the connection has really gone. A reconnection that takes under 1.5 s says nothing. Over 19 minutes on a busy machine: 7 drops before, none after ([#16](https://github.com/wittywilla/WittyPlay/issues/16)).
- **A busy machine no longer makes the picture worse, and stop.** The server took a round trip that was slow for a moment for a full network, cut the bitrate, and started the encoder again, which stops the picture briefly. Round trips now have to stay slow for four reports in a row, and one over a second doesn't count; loss counts as before. Over 19 minutes on a busy machine: 22 bitrate changes and 45 encoder starts before, 6 and 8 after ([#21](https://github.com/wittywilla/WittyPlay/issues/21)).
- **No black flash on a reconnection.** The new picture goes in underneath, and the old one stays up until the new one is on the screen. Reconnecting every 10 s for 4 minutes: 12 black flashes before, none after ([#17](https://github.com/wittywilla/WittyPlay/issues/17)).
- **A paused game costs less.** With a menu open, its still picture is sent again four times a second rather than twenty, each one encoded again ([#22](https://github.com/wittywilla/WittyPlay/issues/22)).
- The web bundle is built with vite 8, as the server's app is, and esbuild is a development dependency of its own for the tests and the probe page ([#29](https://github.com/wittywilla/WittyPlay/issues/29)).
- **A disc kept as a lone .bin plays.** One of raw CD sectors with no sheet beside it is given a sheet of its own, as some cores read it wrongly without one, and Play! crashed on it ([#31](https://github.com/wittywilla/WittyPlay/issues/31)).

- **WittyPlay 0.3.4:** 0.3.3 was tagged and never released: a sandbox test failed on GitHub's runners, which don't allow a private mount. Its fallback was always there.

- **WittyPlay 0.3.3:** Homebrew built with libdragon plays on the Nintendo 64: it goes to ParaLLEl N64 with the settings it needs, and without that core the message says so.

- **Cores install from the store again on Linux.** A module's sandbox refuses chmod, and installing a core used it, so every core stopped with "operation not permitted".
- **melonDS starts on a current Linux.** glibc 2.41 and later refuse a library asking for an executable stack, as melonDS's does by an accident of how it's built. It now loads from a private copy without that request, and no core ever gets an executable stack. It also needed room for the address space it reserves, and shared memory, or it crashed while loading.
- **Play! starts on Linux.** It stopped at once for want of a home folder. It has its save folder as one, and its memory cards are kept there.
- **SwanStation and PPSSPP start on Linux under the sandbox.** They need shared memory, as melonDS and LRPS2 do. The PSP had no way to start under the sandbox at all.
- **Shared memory is private.** A core that needs `/dev/shm` now gets an empty one of its own: nothing else can reach it and nothing is left behind. Where the system doesn't allow that, it has the system's, as LRPS2 did before.
- **PlayStation and Dreamcast discs on Linux:** a `.cue`, `.gdi` or `.m3u` naming `GAME.BIN` beside `Game.bin` didn't open on most cores, as Linux is particular about capitals. Such a game gets a folder of links under the names its sheet uses, and the library is left alone.
- **Dreamcast memory cards now last.** Flycast kept them in the system folder, which a running game can't write to. They're in the game's save folder.
- `wittyplay-probe` has `sweep`, which plays a game through the real stream and says whether time passed; `test-cia` and `test-rom`, which write small programs from nothing but hand-assembled instructions, for consoles with no game to hand; and `cia-info`, which prints what a 3DS package's own bytes say about whether it's encrypted.
- The patches panel links to the guide in Help. The message about an encrypted 3DS file no longer suggests where else a game might come from.
- **PlayStation 2 runs under the sandbox on Linux.** Its core crashed on loading, as it needs `/dev/shm` to map its recompiled code. It's asked for by name, for that core only. It keeps its shader cache beside the save between games, where it used to be rebuilt every time.
- **On a Mac, the sandbox let no game start.** It refused every program, including the runner it was about to start, and its rules named paths through links, which match nothing. Found by reading: this hasn't been run on a Mac.
- **FreeBSD:** a game with no graphics card can't start programs or open a socket. That isn't a sandbox, and custom cores stay refused.
- **Security:** a core fetched by address can't reach shared address space, or a private address wrapped in IPv6 for a gateway to unwrap.

- **Latency:** from a finished frame to the screen went from 65 to 235 ms to 9 to 22 ms, and from a press to the screen from 75 to 270 ms to
  11 to 31 ms, or about 50 on PlayStation 2, which takes most of a frame to run. Those are medians with the server and browser on one machine;
  a wired network adds about 1 ms, Wi-Fi more. The browser no longer holds frames back, sound can't hold the picture back, FFmpeg's queues can't fill,
  and a new press starts the next frame at once. Pads are read every 4 ms. Frames land just before the display wants them where a browser shows it helps.
- **Run-ahead** is on by default for one frame on the older consoles, which takes a whole frame off a game's own reaction. It's a setting.
- **The picture:** PlayStation 2 is native size by default, which ends the bands and grid in Gran Turismo 4. Colours are right on every console:
  red was coming out orange. Pixel art is sharp from the first frame, and pictures of 640 wide or more are scaled smoothly.
- **Speed:** PlayStation 2 goes from about 40 frames a second to 45 to 60 in a race.
- **Poor networks:** bitrate follows the connection, keyframes can't flood a slow link, and a browser's request for a keyframe is answered.
- **Reliability:** a stuck or dead encoder, a dropped network and a stalled picture all come back by themselves, with the last picture up
  until there's a new one. A dropped connection used to step down to a worse way of connecting for the rest of the game.
- A game that's gone from the server, because the server or the module restarted or this device was away too long, starts again from its last save,
  in place of a frozen picture for a minute and then a message about the network. If that happens twice in two minutes, it says so instead.
- Battery saves reach the disk within 5 seconds of changing, from 30, so a machine that dies loses less.
- SameBoy starts: it gives its sound at the Game Boy chip's own rate, over 2 MHz, which is now averaged down.
- **On-screen numbers** say where the delay goes, stage by stage, from a press to the screen, and what's holding things back when something is.
- **Speed or steadiness**, under Speed and rewind, with a good automatic choice.
- **WebRTC, drawn directly**, under Connection: for those who want to try it. It can tear.
- `wittyplay-probe` measures all of it on real frames, in a real browser, and can run through an impaired network, for hours, or killing each process in turn.
- WittyPlay is now a service of its own, with nothing compiled into a server: `wittyplay-module` holds the games being played and answers a server's calls, and `wittyplay-host` is now called `wittyplay-game-runner`.
- The display preview needs no inline style, so the page policy can leave it out.
- **Security:** a core can't remove a file's access list or other extended attributes, or use `userfaultfd`.
- **Security:** one game takes at most 8 viewers and 4 connecting at once. Past that the oldest goes, so someone allowed to play can't run the machine out of sockets and memory.
- The controller screen and the game settings screen are redrawn to match the host app: tabs along a line, chips to choose from,
  cards, switches and sliders in the app's own shapes, button prompts along the bottom, and 44px targets on a phone. Choosing a
  console on the settings screen is a row of chips (`ConsoleChips`) rather than a drop-down list.
- A short press on a pad isn't lost if the browser holds its frames back: pads are also read on a timer whenever a frame is late.
- Whether a 3DS file is encrypted now goes by its bytes as well as its package's mark: `NCCH` where it belongs, and the NoCrypto flag. A package marked encrypted whose program is readable plays.
- A game made only for the New 3DS, as its header says, tells Azahar to be a New 3DS (`citra_is_new_3ds`).
- `.bcci` and `.bcxi`, Azahar's newer compressed forms, count as 3DS files.
- A new game menu, with large tiles for save, load, rewind, fast forward, screenshots, controllers, settings and quit. It works the
  same by finger, keyboard, remote or controller, and each screen in it goes back one level.
- A loading screen that shows what's happening, and error screens that say what to do about it.
- Touch controls redrawn for each console, with shapes drawn rather than typed. One tap hides them, and they step aside when a
  controller is connected. The editor can resize every control at once, and tuck its options away to see the layout.
- Button prompts that match the controller in use (`web/prompts.tsx`).
- A save state can be chosen when a game starts (`startSlot`).
- WittyPlay is now a project of its own, developed and released from this repository.

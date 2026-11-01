# Changelog

What changed in WittyPlay, newest first. What changed before it had a repository of its own is in the
[WittyMediaServer changelog](https://github.com/wittywilla/WittyMediaServer/blob/master/CHANGELOG.md).

## Unreleased

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

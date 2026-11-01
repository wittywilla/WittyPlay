# Changelog

What changed in WittyPlay, newest first. What changed before it had a repository of its own is in the
[WittyMediaServer changelog](https://github.com/wittywilla/WittyMediaServer/blob/master/CHANGELOG.md).

## Unreleased

- WittyPlay is now a service of its own, with nothing compiled into a server: `wittyplay-module` holds the games being played and answers a server's calls, and `wittyplay-host` is now called `wittyplay-game-runner`.
- Whether a 3DS file is encrypted now goes by its bytes as well as its package's mark: `NCCH` where it belongs, and the NoCrypto flag. A package marked encrypted whose program is readable plays.
- A game made only for the New 3DS, as its header says, tells Azahar to be a New 3DS (`citra_is_new_3ds`).
- `.bcci` and `.bcxi`, Azahar's newer compressed forms, count as 3DS files.
- WittyPlay is now a project of its own, developed and released from this repository.
- A new game menu, with large tiles for save, load, rewind, fast forward, screenshots, controllers, settings and quit. It works the
  same by finger, keyboard, remote or controller, and each screen in it goes back one level.
- A loading screen that shows what's happening, and error screens that say what to do about it.
- Touch controls redrawn for each console, with shapes drawn rather than typed. One tap hides them, and they step aside when a
  controller is connected. The editor can resize every control at once, and tuck its options away to see the layout.
- Button prompts that match the controller in use (`web/prompts.tsx`).
- A save state can be chosen when a game starts (`startSlot`).
- WittyPlay is now a project of its own, developed and released from this repository.

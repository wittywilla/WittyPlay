# Changelog

What changed in WittyPlay, newest first. What changed before it had a repository of its own is in the
[WittyMediaServer changelog](https://github.com/wittywilla/WittyMediaServer/blob/master/CHANGELOG.md).

## Unreleased

- WittyPlay is now a project of its own, developed and released from this repository.
- A new game menu, with large tiles for save, load, rewind, fast forward, screenshots, controllers, settings and quit. It works the
  same by finger, keyboard, remote or controller, and each screen in it goes back one level.
- A loading screen that shows what's happening, and error screens that say what to do about it.
- Touch controls redrawn for each console, with shapes drawn rather than typed. One tap hides them, and they step aside when a
  controller is connected. The editor can resize every control at once, and tuck its options away to see the layout.
- Button prompts that match the controller in use (`web/prompts.tsx`).
- A save state can be chosen when a game starts (`startSlot`).

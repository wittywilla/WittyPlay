# Changelog

What changed in WittyPlay, newest first. What changed before it had a repository of its own is in the
[WittyMediaServer changelog](https://github.com/wittywilla/WittyMediaServer/blob/master/CHANGELOG.md).

## Unreleased

- Whether a 3DS file is encrypted now goes by its bytes as well as its package's mark: `NCCH` where it belongs, and the NoCrypto flag. A package marked encrypted whose program is readable plays.
- A game made only for the New 3DS, as its header says, tells Azahar to be a New 3DS (`citra_is_new_3ds`).
- `.bcci` and `.bcxi`, Azahar's newer compressed forms, count as 3DS files.
- WittyPlay is now a project of its own, developed and released from this repository.

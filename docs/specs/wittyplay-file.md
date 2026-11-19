<!-- SPDX-License-Identifier: CC0-1.0 -->
# The .wittyplay file format, version 2

A `.wittyplay` file says how a games front end should picture games: the box art fitted into its tile, the box art
filling its tile, or the game drawn inside a cartridge or disc case like the ones its console used. It can say this for
every game, for each console, and for single games. It is small, plain JSON, and meant to be written by people as
easily as by programs.

Since version 2 it also says how games are played (controls), and it can be **a game's own file**: an easy-to-share,
self-contained archive holding everything known about one game. That covers its details, artwork, the dump it was made
for, how it is played, its controls and cheats, notes on its saves, and who made the file (see *Version 2*).

The WittyPlay module for WittyMediaServer reads and writes it (WittyPlay Settings › Game pictures: Export and Import),
and reads one placed beside a game file. Anyone may read or write it in any program.

- **File extension:** `.wittyplay`
- **Media type:** `application/vnd.wittyplay+json` (a JSON document; `application/json` readers can read it)
- **Archive form:** `application/vnd.wittyplay+zip`, a ZIP archive with `manifest.json` and pictures (version 2)
- **Encoding:** UTF-8, optionally with a byte-order mark. At most 1 MiB bare (64 KiB for a version 1 file), and at
  most 64 MiB as an archive.
- **JSON Schema:** [wittyplay.schema.json](wittyplay.schema.json) (JSON Schema 2020-12)

## Licence

This specification and its JSON Schema are dedicated to the public domain under
[CC0 1.0 Universal](https://creativecommons.org/publicdomain/zero/1.0/). You may implement, copy, change and
redistribute them for any purpose, without asking and without credit. (WittyMediaServer's own code that reads them is
under its own licence; the format is not.)

## Purpose

- **A portable presentation profile.** Someone exports how they like their games shown and imports it on another
  server, or shares it with a friend.
- **A per-game or per-collection note.** A collection of games can carry a file saying "draw these in a cartridge,
  this one in a plain box".
- **A sidecar beside a game file.** A file named after a game file, in the same folder, says how that one game is
  pictured. For `Made Up Quest (USA).sfc` it is `Made Up Quest (USA).wittyplay` (or, also accepted,
  `Made Up Quest (USA).sfc.wittyplay`). Games folders are often read-only and belong to their owner. A program never
  writes a sidecar there on its own: it writes one only when the person asks it to (converting a library, or saving
  from an editor), and only where it may write.
- **A game to share.** A game's own file carries its details, pictures and controls to another library.

A bare `.wittyplay` file never holds pictures or other media. An archive holds pictures only, under `images/`. Neither
form ever holds anything that identifies a person, saves, or the game itself.

## Example

```json
{
  "wittyplay": 1,
  "name": "Living room",
  "generator": "WittyMediaServer 0.8.0",
  "default": { "picture": "case", "label": "boxart", "cases": { "cd": "disc-bare", "cart-16bit": "cart-super-angled" } },
  "consoles": {
    "ps1": { "picture": "fitted" },
    "snes": { "case": "cart-super" }
  },
  "games": [
    { "match": { "file": "Made Up Quest (USA).sfc" }, "case": "generic-box", "label": "title" },
    { "match": { "title": "Another Made Up Game", "console": "ps1" }, "picture": "filled" }
  ]
}
```

## Versions

Every file says which version of the format it follows, as a whole number in the top-level `"wittyplay"` field. The
field is required, and writers put it first:

```json
{ "wittyplay": 2 }
```

| Version | What it is |
|---|---|
| `1` | How games are pictured: `default`, `consoles` and `games` looks (pictures, labels, cases). First released with WittyMediaServer 0.8.0-alpha. |
| `2` | Adds `controls` and a game's own file: the `game` part (details, identity, artwork with a list of pictures for each role, screenshots, play choices, cheats, saves notes), `authorship`, `language`, `translations`, `extensions`, and the archive form. First released with WittyPlay 0.7.8 (WittyMediaServer 0.8.1-alpha). |

How readers and writers treat it:

- A file without `wittyplay`, or with `0` or less, isn't read.
- A reader takes a file of its own version or an older one in full.
- A reader takes a **newer** file for what it knows. It keeps everything else as it is when it writes the file again,
  so nothing is lost by opening a file in an older program. Values it doesn't know in a field it does know (a new
  artwork role, button name, hotkey action or device) are kept, not refused. It says the file is newer: WittyPlay's
  editor shows a note, and its answers carry `"newer": true`.
- A writer writes the lowest version that holds what it writes, but **never lowers** the version a file already
  says. A version 2 file that now holds only looks is still written as version 2.

Changing the format:

- New optional fields, and new values of `case`, may be added without changing the version. A reader ignores what it
  does not know (see *Unknown fields*).
- A change an older reader would get wrong, such as a field whose meaning changes, raises the version. So do new
  artwork roles, button names, hotkey actions and devices, because a reader of the same version refuses values it
  doesn't know.

## Fields

### Top level

| Field | Type | Required | Meaning |
|---|---|---|---|
| `wittyplay` | integer | yes | The format's version: `1`. Writers put it first so a person opening the file sees what it is. |
| `name` | string, ≤ 200 characters | no | A name for people: "Living room", "Arcade night". |
| `description` | string, ≤ 1000 characters | no | Anything else for people. |
| `generator` | string, ≤ 200 characters | no | The program that wrote it. For people; never acted on. |
| `default` | look | no | The look for every game the file covers. In a sidecar, the look of the game beside it. |
| `consoles` | object of looks, ≤ 200 | no | A look per console, keyed by console id. |
| `games` | array of games, ≤ 2000 | no | A look per game. |

### Look

A look says how pictures are shown at one level. Every field is optional: one left out is decided by the level above
(see *Precedence*).

| Field | Values | Meaning |
|---|---|---|
| `picture` | `fitted`, `filled`, `case` | `fitted`: the whole box art, nothing cut off, over a blurred and dimmed copy of itself so the tile has no empty bars. `filled`: the box art fills the tile and is cropped to it. `case`: the game is drawn in a cartridge, disc case or similar, with its art on the label. |
| `case` | a case style id, or `auto` | Which case to draw when `picture` is `case`. `auto` (or leaving it out) lets the medium decide. It counts before `cases`. |
| `cases` | an object: medium id → case style id | A case style per kind of medium (see *Media*), for every game of that medium the look covers: for example all CDs as the bare disc. At most 64. |
| `label` | `boxart`, `logo`, `title` | What goes on the case's label: the box art, the game's own logo picture (the box art when it has none), or the game's title set in type. |

### Game

A game is a look (the fields above) with a `match`:

| Field | Type | Meaning |
|---|---|---|
| `match.item` | integer ≥ 1 | One server's own id for the game. Meaningful only on the server that wrote it; another program ignores entries it cannot match. |
| `match.file` | string, a file name without any folder, ≤ 200 characters | The game file's name, compared without regard to case. |
| `match.title` | string, ≤ 200 characters | The game's title, compared without regard to case or surrounding spaces. |
| `match.console` | console id | The console the game is on. |

A match gives at least one of `item`, `file` or `title`; every field it gives must agree for the entry to apply. When
several entries match one game, a later entry's fields win over an earlier one's.

### Console ids

Lower-case letters, digits, `-` and `_`, at most 32 characters (`^[a-z0-9][a-z0-9_-]{0,31}$`). The ids WittyMediaServer
uses include `nes`, `snes`, `n64`, `gc`, `wii`, `wiiu`, `switch`, `gb`, `gba`, `nds`, `3ds`, `vb`, `md`, `sms`, `gg`,
`segacd`, `32x`, `saturn`, `dc`, `ps1`, `ps2`, `ps3`, `psp`, `xbox`, `pce`, `a2600`, `a5200`, `a7800`, `lynx`, `jaguar`,
`ws`, `ngp`, `neogeo` and `arcade`. Another program may map its own names onto these.

### Media

Every kind of medium games came on has an id and the case styles it can be shown in, the first being its usual one. The table
lists its named ways (front, at an angle, in its box or case, bare, and as a download: every medium can be shown as a download,
with `digital-tile`); a program may offer more of the styles below for it, such as other finishes, boxes and cases. A look's
`cases` chooses a style per medium; a program decides each console's medium (WittyMediaServer's are below).

| Medium | What it is | Styles (usual first) | WittyMediaServer's consoles |
|---|---|---|---|
| `cart-8bit` | 8-bit cartridges | `cart-nes`, `cart-nes-angled`, `box-cart`, `digital-tile` | `nes` |
| `cart-early` | First-generation cartridges | `cart-atari`, `cart-atari-angled`, `box-cart`, `digital-tile` | `a2600`, `a5200`, `a7800` |
| `cart-16bit` | 16-bit cartridges | `cart-super`, `cart-super-angled`, `box-cart-wide`, `digital-tile` | `snes` |
| `cart-mega` | Black 16-bit cartridges | `cart-mega`, `cart-mega-angled`, `case-clamshell`, `digital-tile` | `md`, `32x`, `sms`, `neogeo` |
| `cart-64` | 64-bit cartridges | `cart-64`, `cart-64-angled`, `box-cart-wide`, `digital-tile` | `n64`, `jaguar` |
| `cart-pocket` | Handheld cartridges | `cart-pocket`, `cart-pocket-angled`, `box-small`, `digital-tile` | `gb`, `gbc`, `gg`, `ngp`, `ws`, `vb` |
| `cart-advance` | Wide handheld cartridges | `cart-advance`, `cart-advance-angled`, `box-small`, `digital-tile` | `gba` |
| `card` | Small game cards | `card-slim`, `card-slim-angled`, `case-card`, `digital-tile` | `nds`, `3ds`, `switch`, `switch2` |
| `hucard` | Thin, credit-card-sized cards | `card-hu`, `case-jewel`, `digital-tile` | `pce`, `lynx` |
| `cd` | CDs | `case-jewel`, `disc-bare`, `case-keep`, `digital-tile` | `ps1`, `saturn`, `segacd`, `3do` |
| `gd` | High-density discs with a bright inner ring | `case-jewel`, `disc-gd`, `case-keep`, `digital-tile` | `dc` |
| `dvd` | DVDs | `case-keep`, `disc-bare`, `case-keep-angled`, `digital-tile` | `ps2`, `xbox`, `wii` |
| `bluray` | High-definition discs | `case-blu`, `disc-bare`, `case-blu-angled`, `digital-tile` | `ps3`, `wiiu` |
| `minidisc` | Small 8 cm discs | `case-mini`, `disc-mini`, `digital-tile` | `gc` |
| `umd` | Small discs in a shell | `umd-bare`, `case-umd`, `digital-tile` | `psp` |
| `floppy35` | 3.5-inch floppy disks | `disk-floppy`, `box-big`, `digital-tile` | `amiga`, `atarist`, `dos`, `pc98`, `x68000` |
| `floppy525` | 5.25-inch floppy disks | `disk-525`, `box-big`, `digital-tile` | `apple2`, `bbc` |
| `tape` | Cassette tapes | `tape-cassette`, `case-tape`, `digital-tile` | `c64`, `zx`, `cpc`, `msx` |
| `arcade` | Arcade machines | `arcade-marquee`, `arcade-pcb`, `digital-tile` | `arcade`, `mame`, `fbneo` |
| `digital` | Download only | `digital-tile`, `generic-box`, `digital-poster` | `pc` |
| `other` | Anything else | `generic-box`, `digital-tile`, `digital-poster` | any other |

### Case styles

Original drawings that evoke how each kind of medium was delivered. None reproduces a product, a brand name or a logo.
A program may draw them however it likes; these are what the ids mean. Any style may be chosen for any game.

| Id | Drawing |
|---|---|
| `cart-nes` | An 8-bit front-loading grey cartridge: ridged sides and a label window |
| `cart-atari` | A short first-generation cartridge with a wide label |
| `cart-super` | A 16-bit cartridge, rounded at the top, with grip ridges |
| `cart-64` | A tall 64-bit cartridge with a ridged, sloping top |
| `cart-mega` | A black 16-bit cartridge with a slanted label top |
| `cart-pocket` | A small handheld cartridge with a notched corner |
| `cart-advance` | A wide, short handheld cartridge |
| `card-slim` | A small, thin game card |
| `card-hu` | A card no thicker than a credit card, contacts along one edge |
| `…-angled` | A cartridge, card, case, cassette, small disc shell or arcade cabinet turned to show its side (for example `cart-nes-angled`, `case-jewel-angled`) |
| `<style>-<finish>` | The same drawing in a finish: `gold`, `dark` (black), `clear` (see-through), `red`, `blue`, `green`, `white` or `wood` (wood-grain), for example `cart-nes-gold`, `case-keep-green`, `box-cart-dark`; finishes of an angled-capable drawing also exist at an angle (`cart-nes-gold-angled`) |
| `box-tall`, `box-flip` | A narrow, tall cardboard box; a cardboard box with a lid along the top |
| `disc-bd`, `disc-dark`, `disc-mini-dark` | A disc alone with a blue-violet or a black inner ring |
| `disc-sleeve`, `disc-sleeve-gd` | A disc half out of a paper sleeve |
| `arcade-cabinet` | A whole upright arcade cabinet with the game on its screen |
| `digital-poster`, `digital-banner`, `digital-window` | Download-only styles: a tall store poster with a progress bar, a wide banner with a play button, an app window with a status bar |
| `digital-tile-<finish>` | The download tile in a colour |
| `box-cart`, `box-cart-wide`, `box-small`, `box-big` | A cardboard box: tall, wide, small, or large as computer games came in |
| `case-clamshell` | A hinged plastic case with ribs |
| `case-card` | A small case for a game card |
| `case-jewel` | A square CD jewel case with hinge |
| `case-keep` | A tall DVD keep case |
| `case-blu` | A shorter, slimmer disc case with a band along the top |
| `case-mini` | A small, squarer disc case |
| `disc-bare` | The disc alone, label side up |
| `disc-gd` | The disc alone, with a bright inner ring |
| `disc-mini` | A small disc alone |
| `umd-bare` | A small disc in its shell, with a window |
| `case-umd` | The tall, narrow case a small disc shell came in |
| `disk-floppy` | A 3.5-inch floppy disk with shutter and label |
| `disk-525` | A 5.25-inch floppy disk in its square sleeve |
| `tape-cassette` | An audio cassette with two reels |
| `case-tape` | A cassette in its clear box |
| `arcade-marquee` | An arcade cabinet's lit marquee |
| `arcade-pcb` | An arcade circuit board with a label |
| `digital-tile` | A rounded tile with a download mark, for a game that came as a download |
| `generic-box` | A plain box, for anything else |

A reader that meets a case style id it does not know passes it over (as for `auto`), and keeps the id.

## Version 2: a game's own file

Version 2 keeps everything above and adds three things:

- **Controls**, at every level a look has: per-player button maps for gamepads, keyboards and mice, hotkeys, turbo, dead
  zones and sensitivity (see *Controls*).
- **A game's own file.** A `game` part holds everything about one game: its details, artwork, the dump it was made for,
  how it is played, cheats, and notes on its saves (see *Game part*). It lives beside the game
  (`Made Up Quest (USA).wittyplay`) or travels on its own to be shared.
- **Authorship and languages:** who made the file, the language its text is in, and that text in other languages
  (see *Authorship* and *Languages*).

A file that uses none of these is still written as version 1, so older readers keep reading it.

### Two forms

A `.wittyplay` is one of these:

1. **Bare JSON:** the document itself, as in version 1, up to 1 MiB. It can hold everything except pictures. People can
   write it by hand.
2. **Archive:** a ZIP archive holding `manifest.json` (the same document) and the pictures it names under `images/`, up
   to 64 MiB. Its media type is `application/vnd.wittyplay+zip`.

A reader tells the two forms apart by the first four bytes: `PK\x03\x04` is an archive. A writer writes an archive only
when there are pictures to carry, and writes `manifest.json` as the first member, stored rather than compressed, so it
can be read without unpacking.

An archive is refused whole if any of these is true:

| Rule | Limit |
|---|---|
| Whole file | at most 64 MiB |
| Members | at most 256 |
| Unpacked total | at most 128 MiB |
| `manifest.json` | at most 1 MiB unpacked; it must be present |
| One picture | at most 16 MiB unpacked, and at most 8192 pixels across |
| Any other member | at most 4 MiB unpacked; kept, never used |
| Packing ratio | a member over 1 MiB may not unpack to more than 100 times its packed size |
| Member names | letters, digits, `.`, `_`, `-` and space, in at most three folders. No `..`, no absolute paths, no backslashes, and no name twice (compared without regard to case) |
| Member kinds | ordinary files only: no links, no encryption, only stored or deflated |
| Pictures | `.jpg`, `.png` or `.webp`, and the bytes must be the type the name says |

### Game part

Every field is optional.

| Field | Type | Meaning |
|---|---|---|
| `details` | details | The game's facts. These replace what a program's sources found. |
| `identity` | identity | Which dump the file was made for. |
| `artwork` | object: role → member or list | The pictures for each role: `boxfront`, `boxback`, `boxspine`, `boxfull`, `media` (the disc or cartridge), `logo`, `marquee`, `banner`, `titlescreen`, `fanart`, `snapshot` (a still from the game's video), `icon`. A value names a member under `images/`, or is a list of them with the one shown first: box art from several regions, several pieces of fan art. At most 32 a role, each named once. A writer writes a role with one picture as a plain name, so files with one picture a role read as they always have. |
| `screenshots` | array of members, ≤ 50 | Screenshots, in order. |
| `play` | play | How the game is best played. |
| `cheats` | array, ≤ 500 | Cheat codes: `id`, `label` (≤ 120), `code` (≤ 2048: letters, digits, space and `+ - : ; , . = ? _` only), `enabled`. |
| `saves` | saves | Notes about the game's saves: `kind` (`battery`, `memorycard`, `password`, `none`), `memoryCard` (`shared` or `game`), `blocks` (0–1000), `notes`. The saves themselves are never carried: they belong to a person. |
| `notes` | string, ≤ 20000 | Anything else, for people. |

**Details:**

| Field | Meaning |
|---|---|
| `title`, `sortTitle`, `originalTitle` | Names, each ≤ 200 characters. `originalTitle` is the title in the game's own script. |
| `console` | A console id. |
| `year` | 1950–3000. |
| `releaseDate` | `YYYY-MM-DD`, `YYYY-MM` or `YYYY`. |
| `overview` | ≤ 20000 characters. |
| `genres`, `tags`, `regions` | Up to 50 each. `regions` are written the way dump names write them: `USA`, `Europe`, `Japan`. |
| `developer`, `publisher`, `series` | Each ≤ 200 characters. |
| `players` | `min` and `max` (0–64), and `coop`, `versus`, `online` as true or false. |
| `rating` | A score from 0 to 10. |
| `ageRatings` | A rating per board, such as `{"ESRB": "E", "PEGI": "3"}`. |
| `languages` | Language tags for the languages the game itself can be played in. |
| `externalIds` | The game's id in other catalogues, by a plain name of each catalogue. |

**Identity** gives `fileName`, `size`, `crc32` (8 hex digits), `md5` (32), `sha1` (40) and `serial` (a disc's or
cartridge's product code). For a game of several files, `files` lists each one's `name`, `size` and checksums. Before a
program uses the file for a game, it compares this with the game's own file:

- If the size or any checksum differs, the file is for **another dump** (another region, revision or bad dump). It is
  flagged to the person and not applied.
- If a checksum agrees and nothing differs, it is the **same** dump.
- If nothing can be compared (no checksums given, or the game too large to read through), the file is applied.

A file's name never decides this, because people rename their games. A program may use a CRC32 it already has (from a
scan, or of the file inside a ZIP, or with a header left out) instead of reading the file again.

**Play:**

| Field | Meaning |
|---|---|
| `emulator` | `libretro` or a standalone emulator's id. |
| `core` | A core's id, for libretro. |
| `coreOptions` | A core option's value, by its name. Up to 300. |
| `shader` | A shader preset's name. |
| `aspectRatio` | `4:3`, `8:7`, `core`, `stretch` or `square`. |
| `integerScaling` | true or false. |
| `rotation` | 0, 90, 180 or 270. |
| `videoStandard` | `ntsc` or `pal`. |
| `disc` | Which disc of a set starts, counting from 1. |

A person's own choices for a game always come before a file's.

### Controls

`controls` holds a `default` control set and `consoles`, a control set per console id. These work like `default` and
`consoles` for looks. In a game's own file, `default` is that game's controls. For one game, a console's set goes over
the default part by part, and a player given at both levels takes the console's bindings over the default's, one
binding at a time. A person's own saved layouts come first, then the game's file, then the server's file, then the
program's defaults.

A **control set** has:

| Field | Meaning |
|---|---|
| `players` | Up to 8 player entries (see below). |
| `hotkeys` | `enable`: a hotkey that must be held for any gamepad hotkey to work, so a button can be both the game's and a shortcut's. `actions`: a hotkey per action. The actions are `menu`, `pause`, `reset`, `exit`, `saveState`, `loadState`, `nextSlot`, `previousSlot`, `fastForward`, `fastForwardHold`, `slowMotion`, `rewind`, `frameAdvance`, `screenshot`, `recordVideo`, `fullscreen`, `mute`, `volumeUp`, `volumeDown`, `nextDisc`, `ejectDisc`, `nextShader`, `cheats` and `netplayChat`. A hotkey has a `keyboard` key, with up to three modifiers if wanted (`F2`, `Control+KeyS`), and `gamepad` buttons pressed together (`button:8+button:5`). |
| `turbo` | `rate`: presses per second, 1 to 30. `mode`: `hold` (repeats while held) or `toggle` (repeats until pressed again). |
| `analog` | `deadzone` (0–0.9 of the stick's travel), `sensitivity` (0.1–5), `mouseSensitivity` (0.1–5), `triggerThreshold` (0.05–1: how far an analog trigger must go to count as pressed), and `stickAsDPad` (the left stick also presses the D-pad). |
| `rumble` | true or false. |

A **player** entry has:

| Field | Meaning |
|---|---|
| `player` | 1 to 8. |
| `device` | What is plugged into the port: `gamepad`, `analog`, `mouse`, `lightgun`, `keyboard`, `multitap` or `none`. |
| `gamepad` | A binding per button, in W3C standard gamepad numbering: `button:0`; `axis:1-` (one direction of an axis); `axis:2+@-1` (a trigger read as an axis that rests at -1); `hat:0:up`. |
| `keyboard` | A key per button, by its `KeyboardEvent.code` name: `KeyZ`, `ArrowUp`, `Enter`. |
| `mouse` | A mouse button per game button (`left`, `right`, `middle`, `back`, `forward`, `wheelup`, `wheeldown`). The extra key `aim` says what moving the mouse does: `pointer`, `lstick` or `rstick`. |
| `remap` | What the game sees when a button is pressed: `{"b": "a", "a": "b"}` swaps A and B. Applied after the maps above. |
| `turbo` | Buttons that repeat. |
| `analog` | The player's own analog settings, over the set's. |

Buttons are named after the one controller libretro speaks, and each console's buttons map onto it: `b`, `y`, `select`,
`start`, `up`, `down`, `left`, `right`, `a`, `x`, `l`, `r`, `l2`, `r2`, `l3`, `r3`, and the stick directions
`lstick-left`, `lstick-right`, `lstick-up`, `lstick-down`, `rstick-left`, `rstick-right`, `rstick-up`, `rstick-down`.
Their order is libretro's numbering (`b` is 0, `lstick-left` is 16). On the pad, `b` is the bottom face button, `a`
the right one, `y` the left one and `x` the top one.

### Authorship

`authorship` says who made the file: `author`; `created` and `updated` (RFC 3339 times); `tool` (`name` and `version`
of the program that wrote it last); `license` (an SPDX id such as `CC-BY-4.0`); and `source` (where the details came
from, in words).

### Languages

`language` is the language tag of the file's own text. `translations` holds the same text in other languages, keyed by
language tag. Tags may be ISO 639-1, 639-2 (bibliographic or terminology) or 639-3 codes, or BCP 47 tags with a script
or region. Writers write each tag in its usual form, so `ger` becomes `de` and `pt_br` becomes `pt-BR`. A translation
may give `details` (its text fields, genres and tags), `cheats` (a label per cheat id), `notes`, and `saves` (the saves'
notes). Anything a translation leaves out stays as the file has it. A program shows the translation closest to the
person's language: the same tag first, then the same language. These rules are the same as those of a `.wittyinfo`.

### Extensions

`extensions` holds anything a program wants kept, under a name of its own (`tool.key`). It is kept as it is and never
given a meaning by this specification.

### Example of a game's own file

This is the `manifest.json` of `Made Up Quest (USA).wittyplay`, an archive that also holds `images/boxfront.png` and
`images/screenshots/01.png`:

```json
{
  "wittyplay": 2,
  "language": "en",
  "default": { "picture": "case", "case": "cart-super" },
  "game": {
    "details": {
      "title": "Made Up Quest",
      "console": "snes",
      "year": 1994,
      "genres": ["Adventure"],
      "developer": "Example Studio",
      "players": { "min": 1, "max": 2, "coop": true },
      "regions": ["USA"]
    },
    "identity": { "fileName": "Made Up Quest (USA).sfc", "size": 1048576, "crc32": "1a2b3c4d" },
    "artwork": { "boxfront": "images/boxfront.png" },
    "screenshots": ["images/screenshots/01.png"],
    "play": { "core": "snes9x", "aspectRatio": "8:7" },
    "cheats": [{ "id": "cheat-1", "label": "Infinite lives", "code": "7E0DBE:09", "enabled": false }],
    "saves": { "kind": "battery" }
  },
  "controls": {
    "default": {
      "players": [{ "player": 1, "keyboard": { "b": "KeyZ", "a": "KeyX" }, "turbo": ["y"] }],
      "hotkeys": { "actions": { "saveState": { "keyboard": "F2" }, "loadState": { "keyboard": "F4" } } },
      "turbo": { "rate": 15, "mode": "hold" }
    }
  },
  "translations": { "de": { "details": { "overview": "Ein erfundenes Abenteuer." } } },
  "authorship": { "author": "Example", "updated": "2026-11-18T20:00:00Z", "tool": { "name": "WittyPlay" } }
}
```

### Standard files

Everything in a game's own file can be written out to files other programs read, and gathered back from them. WittyPlay
does both: it gathers what it finds beside a game into a `.wittyplay`, and writes a `.wittyplay` back out to these
files:

| Standard file | What it holds |
|---|---|
| `gamelist.xml` in the game's folder (one `<game>` entry per game, keyed by `<path>`) | `name`, `sortname`, `desc`, `releasedate`, `developer`, `publisher`, `genre`, `players`, `rating` (0–1), `region`, `lang`, `md5`, `crc32`, and the picture paths `image`, `thumbnail`, `marquee` and `fanart` |
| `<game>.nfo` (a `<game>` document) | `title`, `originaltitle`, `sorttitle`, `plot`, `year`, `releasedate`, `genre`, `developer`, `publisher`, `players`, `rating`, `region` |
| `media/<kind>/<game>.<ext>` and `<game>-<kind>.<ext>` | Pictures, by the usual folder names: `box2dfront`, `box2dback`, `box2dside`, `box3d`, `physicalmedia`, `wheel`, `marquee`, `screenshot`, `titlescreen`, `fanart`, `miximage`. Further pictures of a kind are numbered: `media/<kind>/<game>-2.<ext>` (or `-02`), and `<game>-<kind>-2.<ext>` beside the game (`<game>-boxart-2.png`). They are looked for only when the first is there, and come after it. Writing out puts the first at `media/<kind>/<game>.<ext>` and the others at `<game>-02.<ext>`, `<game>-03.<ext>`... |
| `<game>.rmp` | `input_playerN_btn_<button> = "<id>"` lines: the player's `remap` |
| `<game>.cfg` | Per-game input lines: `input_playerN_<button>` keys, `input_playerN_<button>_btn` and `_axis` gamepad bindings, hotkeys (`input_save_state`, `input_load_state`, `input_toggle_fast_forward`, `input_hold_fast_forward`, `input_rewind`, `input_screenshot`, `input_menu_toggle`, `input_pause_toggle`, `input_reset`, `input_enable_hotkey`), `input_analog_deadzone`, `input_analog_sensitivity` and `input_turbo_period` |

Gathering never overwrites a `.wittyplay` that is already there unless the person asks. Writing out never removes a
file, never writes a numbered picture over a file already there, and changes only a `gamelist.xml` entry for the same
game.

## Precedence

For one game, each field of the look (`picture`, `case` or `cases`, `label`) is taken from the first of these that sets
it:

1. the game's entry (or entries) in `games`;
2. its console's entry in `consoles`;
3. `default`;
4. the program's own default.

At each level the case is decided by `case` first, then by `cases` for the game's medium.

A program that combines several files (a person's own choices, a server's choices, a sidecar) decides their order.
WittyMediaServer's is: the person's choice for the game, the server's choice for the game (an administrator's, then a
sidecar's), the person's choice for the console, the server's for the console, the person's default, the server's
default, and finally `case` (the medium's usual case) with the medium's usual case and the box art.

## Unknown fields

A reader ignores fields it does not know, at every level (top level, looks, matches and games), and does not refuse the
file for them. A program that reads a file and writes it again keeps every field it did not know, unchanged, so
nothing is lost passing through an older program. Names beginning with `x-` are never given a meaning by this
specification and are free for a program's own use.

## Validation

A reader refuses a file that:

- is larger than 1 MiB bare (or, as an archive, breaks any of the archive's limits), is not UTF-8, is not a single
  JSON object, or is nested more than 24 deep;
- has no `wittyplay`, or one that is not a whole number of at least 1 (a higher number than it knows is read as
  *newer*: see *Versions*);
- gives a known field the wrong type, or `picture` or `label` a value not listed here;
- gives `case`, or a key or value of `cases`, something that is not an id (`^[a-z0-9][a-z0-9-]{0,39}$`), or `cases`
  more than 64 entries;
- gives a console id that does not fit its pattern;
- has a game with no usable `match`, a `file` containing `/` or `\`, or an `item` below 1;
- has more than 200 consoles or 2000 games, or text longer than its limit;
- in version 2: names a picture that is not in the archive or not under `images/`, names one twice in a role or more
  than 32 in a role, gives a checksum of the wrong
  length, a control binding, key or hotkey action not described here, a cheat code with other characters, or a
  language tag that is not one.

A reader reports every problem it finds in the version 2 parts at once, each with where it is (a path such as
`game.details.year` or an archive member) and what is wrong.

The JSON Schema says the same, apart from the size limit and what a newer version may hold, which a reader checks
itself.

## Security and privacy

A `.wittyplay` file is data, never instructions: it can name nothing to fetch, run or write. Readers enforce the size
limit before parsing. Match file names are names, not paths, so a file can never point outside its folder.

An archive is checked whole before any of it is used: its limits stop archives that unpack to far more than they
seem, every member name is a plain relative name, and every picture is decoded far enough to know that it is the
picture its name says. Names inside the manifest refer only to members of the same archive, never to a path or an
address. Cheat codes are data for an emulator core to read, and allow only the characters codes are written in.
Nothing a `.wittyplay` holds is ever run.

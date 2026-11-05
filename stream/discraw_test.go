package stream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A disc kept as a lone .bin of raw sectors gets a sheet (#31); a cartridge kept as a .bin,
// and a disc that came with a sheet of its own, are left as they are.
func TestALoneRawDiscGetsASheet(t *testing.T) {
	sector := append([]byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0, 0, 2, 0, 2}, make([]byte, 2336)...)
	dir := t.TempDir()
	disc := filepath.Join(dir, "Game.bin")
	if err := os.WriteFile(disc, sector, 0o644); err != nil {
		t.Fatal(err)
	}
	save := t.TempDir()
	got, err := discFolder(disc, save)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(save, discFolderName, "Game.cue") {
		t.Fatalf("gave %s", got)
	}
	b, _ := os.ReadFile(got)
	sheet := string(b)
	if !strings.Contains(sheet, "TRACK 01 MODE2/2352") || !strings.Contains(sheet, "INDEX 01 00:00:00") {
		t.Fatalf("the sheet is %q", sheet)
	}
	// Either a link beside the sheet, or the file named in full.
	if strings.Contains(sheet, `FILE "Game.bin" BINARY`) {
		if b, err := os.ReadFile(filepath.Join(save, discFolderName, "Game.bin")); err != nil || len(b) != len(sector) {
			t.Errorf("reading through the link: %d bytes, %v", len(b), err)
		}
	} else if !strings.Contains(sheet, `FILE "`+disc+`" BINARY`) {
		t.Fatalf("the sheet names neither a link nor the file: %q", sheet)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("the library's folder now has %d files", len(ents))
	}

	for name, files := range map[string]map[string][]byte{
		"a cartridge kept as a .bin": {"Cart.bin": []byte("\x00\x00\x40\x00SEGA GENESIS    ")},
		"a disc with its own sheet":  {"Game.bin": sector, "game.CUE": []byte(`FILE "Game.bin" BINARY`)},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			var rom string
			for n, b := range files {
				if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(n, ".bin") {
					rom = filepath.Join(dir, n)
				}
			}
			if got, err := discFolder(rom, t.TempDir()); err != nil || got != rom {
				t.Errorf("gave %s, %v", got, err)
			}
		})
	}
}

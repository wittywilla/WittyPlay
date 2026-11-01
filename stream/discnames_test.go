//go:build !windows

package stream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDiscFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDiscFolder(t *testing.T) {
	const cue = "FILE \"GAME DISC 1.BIN\" BINARY\r\n  TRACK 01 MODE2/2352\r\n    INDEX 01 00:00:00\r\n"
	t.Run("the sheet's name is in other capitals", func(t *testing.T) {
		dir := writeDiscFiles(t, map[string]string{"Game Disc 1.cue": cue, "Game Disc 1.bin": "data"})
		save := t.TempDir()
		got, err := discFolder(filepath.Join(dir, "Game Disc 1.cue"), save)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(got) != filepath.Join(save, discFolderName) || filepath.Base(got) != "Game Disc 1.cue" {
			t.Fatalf("gave %s", got)
		}
		b, err := os.ReadFile(filepath.Join(save, discFolderName, "GAME DISC 1.BIN"))
		if err != nil || string(b) != "data" {
			t.Errorf("reading through the link: %q, %v", b, err)
		}
		if b, _ := os.ReadFile(got); string(b) != cue {
			t.Errorf("the sheet came out as %q", b)
		}
		// The library is left exactly as it was.
		if ents, _ := os.ReadDir(dir); len(ents) != 2 {
			t.Errorf("the library's folder now has %d files", len(ents))
		}
	})
	t.Run("nothing to do", func(t *testing.T) {
		dir := writeDiscFiles(t, map[string]string{"game.cue": `FILE "game.bin" BINARY`, "game.bin": "data"})
		rom := filepath.Join(dir, "game.cue")
		if got, err := discFolder(rom, t.TempDir()); err != nil || got != rom {
			t.Errorf("gave %s, %v", got, err)
		}
	})
	t.Run("a name that isn't there at all", func(t *testing.T) {
		dir := writeDiscFiles(t, map[string]string{"game.cue": `FILE "other.bin" BINARY`})
		rom := filepath.Join(dir, "game.cue")
		if got, err := discFolder(rom, t.TempDir()); err != nil || got != rom {
			t.Errorf("gave %s, %v", got, err)
		}
	})
	t.Run("a Dreamcast track list", func(t *testing.T) {
		gdi := "3\n1 0 4 2352 TRACK01.BIN 0\n2 756 0 2352 \"Track 02.raw\" 0\n3 45000 4 2352 track03.bin 0\n"
		dir := writeDiscFiles(t, map[string]string{"game.gdi": gdi, "track01.bin": "a", "track 02.raw": "b", "track03.bin": "c"})
		save := t.TempDir()
		got, err := discFolder(filepath.Join(dir, "game.gdi"), save)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"TRACK01.BIN", "Track 02.raw"} {
			if _, err := os.Stat(filepath.Join(filepath.Dir(got), name)); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	t.Run("several discs, one with the wrong capitals", func(t *testing.T) {
		dir := writeDiscFiles(t, map[string]string{
			"game.m3u": "#EXTM3U\nDisc 1.cue\nDisc 2.cue\n", "Disc 1.cue": `FILE "DISC 1.BIN" BINARY`, "Disc 1.bin": "one",
			"Disc 2.cue": `FILE "Disc 2.bin" BINARY`, "Disc 2.bin": "two",
		})
		save := t.TempDir()
		got, err := discFolder(filepath.Join(dir, "game.m3u"), save)
		if err != nil {
			t.Fatal(err)
		}
		folder := filepath.Dir(got)
		if !strings.HasSuffix(folder, discFolderName) {
			t.Fatalf("gave %s", got)
		}
		for _, want := range []string{"game.m3u", "Disc 1.cue", "DISC 1.BIN", "Disc 2.cue"} {
			if _, err := os.Stat(filepath.Join(folder, want)); err != nil {
				t.Errorf("%s: %v", want, err)
			}
		}
	})
	t.Run("a name can't climb out of the folder", func(t *testing.T) {
		dir := writeDiscFiles(t, map[string]string{"game.cue": `FILE "../../etc/passwd" BINARY`})
		rom := filepath.Join(dir, "game.cue")
		if got, err := discFolder(rom, t.TempDir()); err != nil || got != rom {
			t.Errorf("gave %s, %v", got, err)
		}
	})
	t.Run("what an earlier game left goes", func(t *testing.T) {
		dir := writeDiscFiles(t, map[string]string{"game.cue": `FILE "game.bin" BINARY`, "game.bin": "data"})
		save := t.TempDir()
		stale := filepath.Join(save, discFolderName)
		if err := os.MkdirAll(stale, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := discFolder(filepath.Join(dir, "game.cue"), save); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Errorf("it's still there: %v", err)
		}
	})
}

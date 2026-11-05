package wittyplay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrPlayLoneBin is why Play! won't play a lone PS2 .bin file.
	ErrPlayLoneBin = errors.New("Play! cannot open a PlayStation 2 game kept as a single .bin; use LRPS2 or a .cue sheet")
	// ErrPlayBinWithCue is why Play! won't open a .bin directly when a .cue exists.
	ErrPlayBinWithCue = errors.New("Play! cannot open a PlayStation 2 .bin directly; open its .cue sheet or use LRPS2")
)

// isPlayCore reports whether a core ID names the Play! core.
func isPlayCore(coreID string) bool {
	if i := strings.LastIndexAny(coreID, `/\`); i >= 0 {
		coreID = coreID[i+1:]
	}
	id := strings.ToLower(coreID)
	return id == "play" || strings.HasPrefix(id, "play_") || strings.HasPrefix(id, "play-") || strings.HasPrefix(id, "play.")
}

// hasCueSheet reports whether path has an accompanying .cue file in the same folder.
func hasCueSheet(path string) bool {
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	for _, ext := range []string{".cue", ".CUE", ".Cue"} {
		if _, err := os.Stat(stem + ext); err == nil {
			return true
		}
	}
	dir := filepath.Dir(path)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	base := filepath.Base(path)
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".cue") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil && strings.Contains(strings.ToLower(string(b)), strings.ToLower(base)) {
			return true
		}
	}
	return false
}

// RefusalForCore says why a file for this console can't be played by a specific core,
// in words for a person, or "" when nothing is known against it. When coreID is "",
// only console-wide refusals are checked.
func RefusalForCore(path, consoleID, coreID string) string {
	if consoleID == "3ds" {
		info, err := Inspect3DS(path)
		if err == nil && info.Encrypted {
			return ErrEncrypted3DS.Error()
		}
	}
	if consoleID == "ps2" && isPlayCore(coreID) {
		if strings.EqualFold(filepath.Ext(path), ".bin") {
			if _, err := os.Stat(path); err != nil {
				return ""
			}
			if !hasCueSheet(path) {
				return ErrPlayLoneBin.Error()
			}
			return ErrPlayBinWithCue.Error()
		}
	}
	return ""
}

// Refusal says why a file for this console can't be played, in words for a person,
// or "" when nothing is known against it. A file that can't be read is a different
// problem, and whoever opens it next says so. Nothing here decrypts anything or tries
// to: it only reads what a file's headers say in the open (see threeds.go).
func Refusal(path, consoleID string) string {
	return RefusalForCore(path, consoleID, "")
}

package stream

import (
	"maps"
	"slices"

	"github.com/wittywilla/wittyplay"
)

// forThisGame makes what a particular file needs of how its core starts. These can't be changed
// by a setting, as the game can't run without them.
func forThisGame(opt Options) Options {
	if opt.Console != "n64" || opt.CoreID != wittyplay.LibdragonCore || !wittyplay.IsLibdragon(opt.ROM) {
		return opt
	}
	opt.fixedOptions = wittyplay.LibdragonOptions()
	opt.CoreOptions = withFixed(opt.CoreOptions, opt.fixedOptions)
	opt.GPU = false
	opt.RunnerArgs = slices.DeleteFunc(slices.Clone(opt.RunnerArgs), func(a string) bool { return a == "-gpu" })
	opt.Notices = append(opt.Notices[:len(opt.Notices):len(opt.Notices)], wittyplay.LibdragonNotice)
	return opt
}

// withFixed lays what a game can't run without over any other options.
func withFixed(options, fixed map[string]string) map[string]string {
	if len(fixed) == 0 {
		return options
	}
	out := make(map[string]string, len(options)+len(fixed))
	maps.Copy(out, options)
	maps.Copy(out, fixed)
	return out
}

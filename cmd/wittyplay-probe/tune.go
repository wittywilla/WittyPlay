package main

import (
	"flag"

	"github.com/wittywilla/wittyplay/stream"
)

// tuneFlags let a run turn each latency measure on or off, to see what it's worth.
type tuneFlags struct{}

func addTuneFlags(*flag.FlagSet) *tuneFlags { return &tuneFlags{} }

func (t *tuneFlags) tuning() stream.Tuning { return stream.Tuning{} }

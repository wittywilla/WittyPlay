// Command wittyplay-probe measures and inspects game streaming. docs/GAMING.md explains how
// to use it.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: wittyplay-probe frames [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "frames":
		err = frames(os.Args[2:])
	case "run":
		err = run(os.Args[2:])
	default:
		err = fmt.Errorf("no such command: %s", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wittyplay-probe:", err)
		os.Exit(1)
	}
}

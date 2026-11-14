// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package ui

import (
	"bytes"
	"flag"
	"os"
	"slices"
	"testing"

	"github.com/wittywilla/wittymodule/sdui"
	core "github.com/wittywilla/wittymodule/sdui/core"
)

var update = flag.Bool("update-sdui", false, "write sdui.json from the module's Go definition")

// sdui.json is this definition, with its commands, and passes the server's own checks. Run with
// -update-sdui to write it again.
func TestSDUIFile(t *testing.T) {
	for _, is := range sdui.Check(UI(), Module) {
		t.Errorf("%s", is)
	}
	want, err := JSON()
	if err != nil {
		t.Fatal(err)
	}
	// The whole file, commands and all, as the server reads and checks it.
	ui, err := core.ParseModuleUI(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(ui.Commands) != len(Commands()) {
		t.Fatalf("%d commands read back, of %d", len(ui.Commands), len(Commands()))
	}
	for _, is := range core.ValidateModuleUI(ui, Module, core.ModuleChecks{ColorToken: func(s string) bool { return slices.Contains(sdui.ColorTokens, s) }}) {
		if is.Severity == "error" {
			t.Errorf("%s: %s", is.Path, is.Message)
		}
	}
	if *update {
		if err := os.WriteFile("../sdui.json", want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile("../sdui.json")
	if err != nil || !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Errorf("../sdui.json is not the module's Go definition: run go test -run %s -update-sdui", t.Name())
	}
}

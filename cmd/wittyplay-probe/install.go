// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/wittywilla/wittyplay/cores"
)

// install fetches cores with the app's own installer, for measuring on them.
func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	dir := fs.String("cores-dir", "", "the folder for cores")
	system := fs.String("system", "", "the BIOS folder")
	_ = fs.Parse(args)
	mgr := &cores.Manager{Dir: *dir, Extra: []string{}, System: *system}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	for _, id := range fs.Args() {
		if mgr.Find(id) != nil {
			fmt.Println(id, "is already here")
			continue
		}
		got, err := mgr.Install(ctx, id)
		if err != nil {
			fmt.Println(id, "failed:", err)
			continue
		}
		fmt.Println(id, "installed:", got.Path)
	}
	return nil
}

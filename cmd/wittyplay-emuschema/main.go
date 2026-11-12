// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Command wittyplay-emuschema writes emulators/schemas/<id>.json: every setting an emulator has,
// read from the definitions in its own source at the version WittyPlay runs, with where each
// lives in the emulator's settings file, its type, its default and, where the source lists
// them, its choices.
//
//	go run ./cmd/wittyplay-emuschema                 # every emulator
//	go run ./cmd/wittyplay-emuschema -emulator dolphin -cache /tmp/emusrc
//
// It reads the source over HTTPS from raw.githubusercontent.com, at a tag, and nothing else. A
// setting that is a path, an address, the updater or anything else that reaches outside the
// sandbox is written fixed: never offered, always its default.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wittywilla/wittyplay/emulators"
)

// source is where one emulator's settings are defined, and how to read them.
type source struct {
	Repo  string
	Tag   string
	Files []string // paths in the repository, read in this order
	// Extract reads the files (by path) into options.
	Extract func(files map[string]string) ([]option, error)
	// KeyPrefix starts each setting's key, to keep two files' keys apart.
	KeyPrefix func(option) string
}

// The versions WittyPlay runs. Each tag was checked against the project's own repository.
var sources = map[string]source{
	"dolphin": {Repo: "dolphin-emu/dolphin", Tag: "2609b", Files: dolphinFiles, Extract: extractDolphin, KeyPrefix: dolphinPrefix},
	"pcsx2":   {Repo: "PCSX2/pcsx2", Tag: "v2.8.2", Files: pcsx2Files, Extract: extractPCSX2},
	"ppsspp":  {Repo: "hrydgard/ppsspp", Tag: "v1.20.4", Files: ppssppFiles, Extract: extractPPSSPP},
	"melonds": {Repo: "melonDS-emu/melonDS", Tag: "1.1", Files: melondsFiles, Extract: extractMelonDS},
	"flycast": {Repo: "flyinghead/flycast", Tag: "v2.7", Files: flycastFiles, Extract: extractFlycast},
	"xemu":    {Repo: "xemu-project/xemu", Tag: "v0.8.139", Files: xemuFiles, Extract: extractXemu},
	"cemu":    {Repo: "cemu-project/Cemu", Tag: "v2.6", Files: cemuFiles, Extract: extractCemu},
	// RPCS3 tags its releases v0.0.43 and so on, not each build: build 20582 is this commit.
	"rpcs3": {Repo: "RPCS3/rpcs3", Tag: "a90588e7ee8dc10aeaaa9441df674edd48d0d271", Files: rpcs3Files, Extract: extractRPCS3},
}

// versions are what each schema says it was read from, where the tag isn't the version.
var versions = map[string]string{"rpcs3": "0.0.43-20582"}

func main() {
	which := flag.String("emulator", "all", "the emulator to read, or all")
	out := flag.String("out", filepath.Join("emulators", "schemas"), "the folder the schemas go in")
	cache := flag.String("cache", "", "a folder to keep the fetched source in, and read it from next time")
	flag.Parse()
	ids := []string{*which}
	if *which == "all" {
		ids = ids[:0]
		for id := range sources {
			ids = append(ids, id)
		}
		slices.Sort(ids)
	}
	failed := false
	for _, id := range ids {
		n, skipped, err := run(id, *out, *cache)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", id, err)
			failed = true
			continue
		}
		fmt.Printf("%s: %d settings (%d offered)\n", id, n[0], n[1])
		for _, s := range skipped {
			fmt.Printf("  left out %s\n", s)
		}
	}
	if failed {
		os.Exit(1)
	}
}

func run(id, outDir, cache string) ([2]int, []string, error) {
	src, ok := sources[id]
	if !ok {
		return [2]int{}, nil, fmt.Errorf("no emulator %q", id)
	}
	files := map[string]string{}
	for _, p := range src.Files {
		b, err := fetch(src.Repo, src.Tag, p, cache)
		if err != nil {
			return [2]int{}, nil, err
		}
		files[p] = string(b)
	}
	opts, err := src.Extract(files)
	if err != nil {
		return [2]int{}, nil, err
	}
	list, skipped := settings(opts, src.KeyPrefix)
	version := src.Tag
	if v, ok := versions[id]; ok {
		version = v
	}
	f := emulators.SchemaFile{
		Emulator: id,
		Version:  version,
		Source:   "https://github.com/" + src.Repo + "/tree/" + src.Tag,
		Settings: list,
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return [2]int{}, nil, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return [2]int{}, nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, id+".json"), append(b, '\n'), 0o644); err != nil {
		return [2]int{}, nil, err
	}
	return [2]int{len(list), len(emulators.Offered(list))}, skipped, nil
}

// fetch reads one file of an emulator's source at a tag.
func fetch(repo, tag, path, cache string) ([]byte, error) {
	var cached string
	if cache != "" {
		cached = filepath.Join(cache, filepath.FromSlash(repo), tag, filepath.FromSlash(path))
		if b, err := os.ReadFile(cached); err == nil {
			return b, nil
		}
	}
	url := "https://raw.githubusercontent.com/" + repo + "/" + tag + "/" + path
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	// A source file is a few hundred kilobytes at most.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 8<<20 {
		return nil, errors.New(url + ": larger than any source file should be")
	}
	if cached != "" {
		if err := os.MkdirAll(filepath.Dir(cached), 0o755); err == nil {
			_ = os.WriteFile(cached, b, 0o644)
		}
	}
	return b, nil
}

// fileOf finds a fetched file by the end of its path.
func fileOf(files map[string]string, suffix string) string {
	for p, s := range files {
		if strings.HasSuffix(p, suffix) {
			return s
		}
	}
	return ""
}

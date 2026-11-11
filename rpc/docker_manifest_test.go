// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/sandbox/docker"
)

// TestManifestListsDockerSettings: the settings the manifest names are the schema's, every one
// is a field of docker.Settings, every one is admin-only, and Docker is off by default.
func TestManifestListsDockerSettings(t *testing.T) {
	m, err := wittymodule.ParseManifest(wittyplay.ManifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range docker.Schema() {
		keys = append(keys, f.Key)
		if !f.Admin {
			t.Errorf("%s isn't admin-only", f.Key)
		}
		if f.Key == "docker.enabled" && f.Default != false {
			t.Errorf("docker.enabled defaults to %v", f.Default)
		}
	}
	if got := m.Capabilities["dockerSettings"]; !slices.Equal(got, keys) {
		t.Fatalf("module.json lists %v; the schema has %v", got, keys)
	}
	var tags []string
	st := reflect.TypeFor[docker.Settings]()
	for i := range st.NumField() {
		name, _, _ := strings.Cut(st.Field(i).Tag.Get("json"), ",")
		tags = append(tags, name)
	}
	for _, k := range keys {
		if !slices.Contains(tags, k) {
			t.Errorf("the schema's %s isn't a setting", k)
		}
	}
	b, _ := json.Marshal(docker.DefaultSettings())
	if !strings.Contains(string(b), `"docker.enabled":false`) {
		t.Errorf("defaults: %s", b)
	}
}

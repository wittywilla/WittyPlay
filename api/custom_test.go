// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/stream"
)

// Custom cores: an administrator's own code, run on this machine. Only an adult administrator gets
// here, the machine's owner has to allow it first, and the password is asked again for anything
// that brings code in.

// A core is a program, and reads any file the server can. Knowing the password is not enough to
// turn them on: whoever runs the machine has to allow it, and a lock beats everything.
func TestCustomCoresNeedTheMachinesSay(t *testing.T) {
	a, h, ph := coresApp(t)
	ctx := context.Background()
	enable := map[string]any{"enabled": true}
	owner := adminID
	owner.Confirmed = true
	if w := asWho(t, h, "PUT", "/gaming/custom/enabled", owner, enable); w.Code != 403 {
		t.Fatalf("turned on with the machine's say missing: %d %s", w.Code, w.Body)
	}
	// A setting alone doesn't turn them on.
	if err := a.SetConfig(ctx, settingCustomCores, true); err != nil {
		t.Fatal(err)
	}
	if a.customCoresOn(ctx) {
		t.Fatal("a saved setting alone turned them on")
	}
	var st map[string]any
	_ = json.Unmarshal(asWho(t, h, "GET", "/gaming/custom", adminID, nil).Body.Bytes(), &st)
	if st["enabled"] != false || st["canEnable"] != false || st["needsSwitch"] != true {
		t.Fatalf("%v", st)
	}
	// Allowed on the machine (the server's Hello, asked again after a minute).
	ph.mu.Lock()
	ph.machine = true
	ph.mu.Unlock()
	e := a.eng()
	e.machineMu.Lock()
	e.machine = nil
	e.machineMu.Unlock()
	if _, sandboxed := stream.SandboxStrength(); !sandboxed {
		t.Skip("no sandbox for a core on this system")
	}
	if !a.customCoresOn(ctx) {
		t.Error("allowed on the machine and turned on in Settings")
	}
	// Turning on takes the password; turning off doesn't.
	_ = a.SetConfig(ctx, settingCustomCores, false)
	if w := asWho(t, h, "PUT", "/gaming/custom/enabled", adminID, enable); w.Code != 403 || !ph.audited("password-refused") {
		t.Errorf("without the password: %d", w.Code)
	}
	if w := asWho(t, h, "PUT", "/gaming/custom/enabled", owner, enable); w.Code != 200 || !a.customCoresOn(ctx) || !ph.audited("turned-on") {
		t.Errorf("with it: %d %s", w.Code, w.Body)
	}
	if w := asWho(t, h, "PUT", "/gaming/custom/enabled", adminID, map[string]any{"enabled": false}); w.Code != 200 || a.customCoresOn(ctx) {
		t.Errorf("turning off: %d", w.Code)
	}
	t.Setenv(lockCustomCores, "1")
	if w := asWho(t, h, "PUT", "/gaming/custom/enabled", owner, enable); w.Code != 403 || a.customCoresOn(ctx) {
		t.Errorf("locked, turned on: %d", w.Code)
	}
}

// customOnFor turns custom cores on for a test, as an owner would.
func customOnFor(t *testing.T, a *App, ph *coreHost) {
	t.Helper()
	if _, sandboxed := stream.SandboxStrength(); !sandboxed {
		t.Skip("no sandbox for a core on this system")
	}
	ph.mu.Lock()
	ph.machine = true
	ph.mu.Unlock()
	e := a.eng()
	e.machineMu.Lock()
	e.machine = nil // asked again
	e.machineMu.Unlock()
	if err := a.SetConfig(context.Background(), settingCustomCores, true); err != nil {
		t.Fatal(err)
	}
	if !a.customCoresOn(context.Background()) {
		t.Fatal("custom cores aren't on")
	}
}

// Only adult administrators, only while it's on, and only with the password where code comes in.
// A file fetched from an address is fetched from the internet only.
func TestCustomRoutesRefuse(t *testing.T) {
	a, h, ph := coresApp(t)
	for _, path := range []string{"/gaming/custom", "/gaming/custom/staged/url", "/gaming/custom/consoles"} {
		method := "POST"
		if path == "/gaming/custom" {
			method = "GET"
		}
		if w := asWho(t, h, method, path, otherID, map[string]any{}); w.Code != 403 {
			t.Errorf("%s for someone who isn't an admin: %d", path, w.Code)
		}
	}
	if w := asWho(t, h, "POST", "/gaming/custom/consoles", adminID, map[string]any{}); w.Code != 409 {
		t.Errorf("while it's off: %d", w.Code)
	}
	customOnFor(t, a, ph)
	kid := adminID
	kid.Kind = "kids"
	if w := asWho(t, h, "POST", "/gaming/custom/consoles", kid, map[string]any{}); w.Code != 403 {
		t.Errorf("a kids profile: %d", w.Code)
	}
	if w := asWho(t, h, "POST", "/gaming/custom/staged/url", adminID, map[string]any{"url": "https://example.com/core.zip"}); w.Code != 403 {
		t.Errorf("without the password: %d", w.Code)
	}
	owner := adminID
	owner.Confirmed = true
	for _, u := range []string{"http://127.0.0.1/core.zip", "https://192.168.1.2/core.zip", "file:///etc/passwd"} {
		if w := asWho(t, h, "POST", "/gaming/custom/staged/url", owner, map[string]any{"url": u}); w.Code != 422 {
			t.Errorf("%s: %d %s", u, w.Code, w.Body)
		}
	}
	if w := asWho(t, h, "POST", "/gaming/custom/staged/upload", adminID, []byte("x")); w.Code != 403 {
		t.Errorf("an upload without the password: %d", w.Code)
	}
	if w := asWho(t, h, "POST", "/gaming/custom/staged/placed", owner, map[string]any{"key": "../../etc/passwd"}); w.Code != 422 {
		t.Errorf("a placed file nobody listed: %d %s", w.Code, w.Body)
	}
	if w := asWho(t, h, "DELETE", "/gaming/custom/staged/nope", adminID, nil); w.Code != 404 {
		t.Errorf("cancelling nothing: %d", w.Code)
	}
	if w := asWho(t, h, "POST", "/gaming/custom/staged/nope/approve", adminID, map[string]any{"acknowledged": true}); w.Code != 404 {
		t.Errorf("approving nothing: %d", w.Code)
	}
	if w := asWho(t, h, "PUT", "/gaming/custom/cores/nope", adminID, map[string]any{}); w.Code != 404 {
		t.Errorf("a core that isn't here: %d", w.Code)
	}
}

// An approved core is listed under its consoles, plays only where it was assigned, and is read
// in full before every game: a file that has changed doesn't start, and is written down.
func TestAnApprovedCoreIsCheckedEveryTime(t *testing.T) {
	a, h, ph := coresApp(t)
	customOnFor(t, a, ph)
	ctx := context.Background()
	store := a.eng().custom.store
	file := []byte("a core, as approved")
	sum := sha256Hex(file)
	p := store.CorePath("mycore", sum)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, file, 0o644); err != nil {
		t.Fatal(err)
	}
	c := &CustomCore{ID: "mycore", Name: "My Core", SHA256: sum, Size: int64(len(file)), Consoles: []string{"nes"}, AddedAt: 1}
	if err := a.saveCustomCore(ctx, c); err != nil {
		t.Fatal(err)
	}
	got := a.Cores().Find("mycore")
	if got == nil || got.Source != cores.SourceCustom || got.Path != p {
		t.Fatalf("listed: %+v", got)
	}
	if !a.CustomFor(ctx, "nes") || a.CustomFor(ctx, "snes") {
		t.Error("assigned where it was assigned")
	}
	if err := a.CheckCustomCore(ctx, got); err != nil {
		t.Errorf("as approved: %v", err)
	}
	// Changing its settings needs no password; its consoles must exist.
	if w := asWho(t, h, "PUT", "/gaming/custom/cores/mycore", adminID, map[string]any{"consoles": []string{"made-up"}}); w.Code != 400 {
		t.Errorf("a console that isn't: %d", w.Code)
	}
	if w := asWho(t, h, "PUT", "/gaming/custom/cores/mycore", adminID, map[string]any{"consoles": []string{"nes", "snes"}, "defaultFor": []string{"snes"}}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if a.PreferredCores(ctx)["snes"] != "mycore" || a.PreferredCore(ctx, wittyplay.ByID("snes"), 0) != "mycore" {
		t.Error("the default for SNES")
	}
	// What the settings page shows.
	var st CustomStatus
	if err := json.Unmarshal(asWho(t, h, "GET", "/gaming/custom", adminID, nil).Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || len(st.Cores) != 1 || st.Cores[0].State.State != "ok" || len(st.Cores[0].DefaultFor) != 1 || len(st.PadChoices) == 0 {
		t.Errorf("%+v", st)
	}
	// Swapped on disk: it won't start, and that's written down once.
	if err := os.WriteFile(p, []byte("something else"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.CheckCustomCore(ctx, got); err == nil || !strings.Contains(err.Error(), "changed since it was approved") {
		t.Errorf("changed: %v", err)
	}
	if !ph.audited("file-changed") {
		t.Error("the change wasn't written down")
	}
	// Going back, taking the change, adding files: each with the password, and checked.
	owner := adminID
	owner.Confirmed = true
	for _, path := range []string{"reapprove", "rollback", "files"} {
		if w := asWho(t, h, "POST", "/gaming/custom/cores/mycore/"+path, adminID, map[string]any{}); w.Code != 403 {
			t.Errorf("%s without the password: %d", path, w.Code)
		}
	}
	if w := asWho(t, h, "POST", "/gaming/custom/cores/mycore/rollback", owner, map[string]any{"sha256": "../x"}); w.Code != 400 {
		t.Errorf("rolling back to a path: %d", w.Code)
	}
	if w := asWho(t, h, "POST", "/gaming/custom/cores/mycore/rollback", owner, map[string]any{"sha256": strings.Repeat("a", 64)}); w.Code != 404 {
		t.Errorf("rolling back to a version never kept: %d", w.Code)
	}
	// Taking the change test-loads it again, which needs the runner (missing here).
	if w := asWho(t, h, "POST", "/gaming/custom/cores/mycore/reapprove", owner, map[string]any{}); w.Code != 422 || !ph.audited("reapprove-failed") {
		t.Errorf("taking the change with no runner: %d %s", w.Code, w.Body)
	}
	if w := asWho(t, h, "POST", "/gaming/custom/cores/mycore/files", owner, []byte("not a form")); w.Code != 400 {
		t.Errorf("files that aren't a form: %d %s", w.Code, w.Body)
	}
	// Off, it isn't listed, and nothing it was assigned to lifts.
	_ = a.SetConfig(ctx, settingCustomCores, false)
	if a.Cores().Find("mycore") != nil || a.CustomFor(ctx, "nes") {
		t.Error("listed while custom cores are off")
	}
	// Taking it away works while they're off.
	if w := asWho(t, h, "DELETE", "/gaming/custom/cores/mycore", adminID, nil); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if _, err := a.customCoreRow(ctx, "mycore"); err == nil || a.PreferredCores(ctx)["snes"] != "" {
		t.Error("still there")
	}
}

// Custom consoles: data, not code. A library set to one keeps it from being taken away.
func TestCustomConsoles(t *testing.T) {
	a, h, ph := coresApp(t)
	customOnFor(t, a, ph)
	body := []byte(`{"name":"Fantasy Box","short":"FBox","extensions":[".fbx"],"padLike":"snes"}`)
	w := asWho(t, h, "POST", "/gaming/custom/consoles", adminID, body)
	var c wittyplay.Console
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &c) != nil || !wittyplay.IsCustomConsole(c.ID) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if wittyplay.ByID(c.ID) == nil || console(c.ID) == nil || !ph.audited("console-added") {
		t.Fatal("the table doesn't know it")
	}
	// It borrows the SNES's row for its shelf.
	if v := consoleView(t, a, c.ID); v.Shelf == nil || v.Ready {
		t.Errorf("in the list: %+v", v)
	}
	if w := asWho(t, h, "PUT", "/gaming/custom/consoles/"+c.ID, adminID, []byte(`{"name":"Fantasy Box 2","short":"FBox","extensions":[".fbx"],"cores":["no_such_core"]}`)); w.Code != 400 {
		t.Errorf("a core nobody has heard of: %d %s", w.Code, w.Body)
	}
	if w := asWho(t, h, "PUT", "/gaming/custom/consoles/"+c.ID, adminID, []byte(`{"name":"Fantasy Box 2","short":"FBox","extensions":[".fbx"]}`)); w.Code != 200 || wittyplay.ByID(c.ID).Name != "Fantasy Box 2" {
		t.Errorf("changing it: %d %s", w.Code, w.Body)
	}
	ph.counts[c.ID] = 4
	ph.libs = []host.Library{{ID: 3, Name: "Fantasy", Kind: "games", Console: c.ID}}
	var st CustomStatus
	_ = json.Unmarshal(asWho(t, h, "GET", "/gaming/custom", adminID, nil).Body.Bytes(), &st)
	if len(st.Consoles) != 1 || st.Consoles[0].Games != 4 || st.Consoles[0].Libraries != 1 {
		t.Errorf("%+v", st.Consoles)
	}
	if w := asWho(t, h, "DELETE", "/gaming/custom/consoles/"+c.ID, adminID, nil); w.Code != 409 {
		t.Errorf("with a library set to it: %d", w.Code)
	}
	ph.libs = nil
	if w := asWho(t, h, "DELETE", "/gaming/custom/consoles/"+c.ID, adminID, nil); w.Code != 200 || wittyplay.ByID(c.ID) != nil {
		t.Errorf("taken away: %d", w.Code)
	}
	if w := asWho(t, h, "DELETE", "/gaming/custom/consoles/nes", adminID, nil); w.Code != 404 {
		t.Errorf("a console from the table: %d", w.Code)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

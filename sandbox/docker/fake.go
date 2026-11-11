// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package docker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"sync"
	"time"
)

// Fake is a Runtime in memory, for tests here and in packages that use this one.
type Fake struct {
	mu         sync.Mutex
	Down       bool // Available says no, and every call fails
	Containers map[string]*FakeContainer
	Pulled     []string
	Specs      []Spec // every spec created, in order
	seq        int
	// CreateErr, StartErr: the next call fails with it.
	CreateErr, StartErr error
	// Machine, when set, is what Available says of the machine: a small one, for a test.
	Machine *Status
}

// FakeContainer is one container in a Fake.
type FakeContainer struct {
	Info    Info
	Spec    Spec
	Removed bool
}

func (f *Fake) err() error {
	if f.Down {
		return ErrUnavailable
	}
	return nil
}

func (f *Fake) Available(context.Context) Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Down {
		return Status{Host: "fake", Error: "down"}
	}
	st := Status{Available: true, Host: "fake", APIVersion: "1.52", OS: "linux", Arch: "amd64", CPUs: 8, MemoryMB: 16384}
	if f.Machine != nil {
		st.Arch, st.CPUs, st.MemoryMB, st.Rootless = f.Machine.Arch, f.Machine.CPUs, f.Machine.MemoryMB, f.Machine.Rootless
	}
	return st
}

func (f *Fake) Pull(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return err
	}
	f.Pulled = append(f.Pulled, ref)
	return nil
}

func (f *Fake) Create(_ context.Context, s Spec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return "", err
	}
	if f.CreateErr != nil {
		err := f.CreateErr
		f.CreateErr = nil
		return "", err
	}
	if err := Check(s); err != nil {
		return "", err
	}
	if f.Containers == nil {
		f.Containers = map[string]*FakeContainer{}
	}
	f.seq++
	id := fmt.Sprintf("%064d", f.seq)
	f.Containers[id] = &FakeContainer{Spec: s, Info: Info{ID: id, Name: s.Name, Image: s.Image, State: "created", Labels: maps.Clone(s.Labels), Created: time.Now(),
		IP: netip.MustParseAddr("172.30.0.2")}}
	f.Specs = append(f.Specs, s)
	return id, nil
}

// Add puts a container in, as one left by an earlier run would be.
func (f *Fake) Add(labels map[string]string, running bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Containers == nil {
		f.Containers = map[string]*FakeContainer{}
	}
	f.seq++
	id := fmt.Sprintf("%064d", f.seq)
	st := "exited"
	if running {
		st = "running"
	}
	f.Containers[id] = &FakeContainer{Info: Info{ID: id, Labels: labels, Running: running, State: st}}
	return id
}

func (f *Fake) get(id string) (*FakeContainer, error) {
	if err := f.err(); err != nil {
		return nil, err
	}
	c := f.Containers[id]
	if c == nil || c.Removed {
		return nil, errors.New("no such container")
	}
	return c, nil
}

func (f *Fake) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err != nil {
		return err
	}
	if f.StartErr != nil {
		err := f.StartErr
		f.StartErr = nil
		return err
	}
	c.Info.Running, c.Info.State = true, "running"
	return nil
}

func (f *Fake) Stop(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err != nil {
		return err
	}
	c.Info.Running, c.Info.State = false, "exited"
	return nil
}

// Exit makes a container stop by itself.
func (f *Fake) Pause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err != nil {
		return err
	}
	if !c.Info.Running {
		return errors.New("not running")
	}
	c.Info.Paused, c.Info.State = true, "paused"
	return nil
}

func (f *Fake) Unpause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err != nil {
		return err
	}
	c.Info.Paused, c.Info.State = false, "running"
	return nil
}

func (f *Fake) Exit(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.Containers[id]; c != nil {
		c.Info.Running, c.Info.State = false, "exited"
	}
}

func (f *Fake) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err != nil {
		return err
	}
	c.Removed = true
	return nil
}

func (f *Fake) Logs(_ context.Context, id string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(id); err != nil {
		return "", err
	}
	return "fake logs\n", nil
}

func (f *Fake) Inspect(_ context.Context, id string) (Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err != nil {
		return Info{}, err
	}
	return c.Info, nil
}

func (f *Fake) List(_ context.Context, labels map[string]string) ([]Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return nil, err
	}
	var out []Info
	for _, c := range f.Containers {
		if c.Removed {
			continue
		}
		ok := true
		for k, v := range labels {
			if c.Info.Labels[k] != v {
				ok = false
			}
		}
		if ok {
			out = append(out, c.Info)
		}
	}
	return out, nil
}

func (f *Fake) EnsureNetwork(_ context.Context, n NetworkSpec) (NetworkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return NetworkInfo{}, err
	}
	return NetworkInfo{ID: "net", Name: n.Name, Internal: n.Internal, Gateway: netip.MustParseAddr("172.30.0.1")}, nil
}

// Live counts containers not removed.
func (f *Fake) Live() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.Containers {
		if !c.Removed {
			n++
		}
	}
	return n
}

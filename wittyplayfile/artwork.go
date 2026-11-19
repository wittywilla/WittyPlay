// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplayfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MaxPicturesPerRole is how many pictures one artwork role may hold: box art from several
// regions, a few title screens, several pieces of fan art.
const MaxPicturesPerRole = 32

// Pictures are the pictures for one role, the one to show first. In the manifest a role with
// one picture is written as its name, and with more as a list:
//
//	"artwork": {"boxfront": "images/boxfront.png", "fanart": ["images/fanart.jpg", "images/fanart-2.jpg"]}
type Pictures []string

// UnmarshalJSON takes a name or a list of names.
func (p *Pictures) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*p = Pictures{one}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return errors.New("a picture is a name in the archive, or a list of them")
	}
	*p = list
	return nil
}

// MarshalJSON writes one picture as its name, so files with one picture a role read as they
// always have.
func (p Pictures) MarshalJSON() ([]byte, error) {
	if len(p) == 1 {
		return json.Marshal(p[0])
	}
	return json.Marshal([]string(p))
}

// Artwork names the pictures in the archive for each role (ArtworkRoles).
type Artwork map[string]Pictures

// First is the picture shown for a role, "" when it has none.
func (a Artwork) First(role string) string {
	if p := a[role]; len(p) > 0 {
		return p[0]
	}
	return ""
}

func validRole(role string) error {
	if !oneOfList(role, ArtworkRoles) {
		return fmt.Errorf("%q is not an artwork role: use %s", role, strings.Join(ArtworkRoles, ", "))
	}
	return nil
}

func pictureFor(b []byte) (string, error) {
	ext := ImageType(b)
	if ext == "" {
		return "", errors.New("pictures have to be JPEG, PNG or WebP")
	}
	return ext, nil
}

// SetImage puts a picture in the archive as the one shown for a role, in place of the one shown
// before; the role's other pictures stay.
func (d *Doc) SetImage(role string, b []byte) (string, error) {
	if err := validRole(role); err != nil {
		return "", err
	}
	ext, err := pictureFor(b)
	if err != nil {
		return "", err
	}
	g := d.ensureGame()
	name := d.freeImageName(role, ext, true)
	if err := checkImage(name, b); err != nil {
		return "", err
	}
	if g.Artwork == nil {
		g.Artwork = Artwork{}
	}
	p := g.Artwork[role]
	if len(p) > 0 {
		if p[0] != name {
			delete(d.Files, p[0])
		}
		p = append(Pictures{name}, p[1:]...)
	} else {
		p = Pictures{name}
	}
	d.putFile(name, b)
	g.Artwork[role] = p
	return name, nil
}

// AddImage adds a picture for a role after the ones it has.
func (d *Doc) AddImage(role string, b []byte) (string, error) {
	if err := validRole(role); err != nil {
		return "", err
	}
	ext, err := pictureFor(b)
	if err != nil {
		return "", err
	}
	g := d.ensureGame()
	if len(g.Artwork[role]) >= MaxPicturesPerRole {
		return "", fmt.Errorf("%s already has %d pictures, the most a file holds", role, MaxPicturesPerRole)
	}
	name := d.freeImageName(role, ext, false)
	if err := checkImage(name, b); err != nil {
		return "", err
	}
	if g.Artwork == nil {
		g.Artwork = Artwork{}
	}
	d.putFile(name, b)
	g.Artwork[role] = append(g.Artwork[role], name)
	return name, nil
}

// RemoveImage takes one picture out of a role, or every one when name is "".
func (d *Doc) RemoveImage(role, name string) bool {
	if d.Game == nil {
		return false
	}
	p := d.Game.Artwork[role]
	keep := Pictures{}
	gone := false
	for _, n := range p {
		if name == "" || n == name {
			delete(d.Files, n)
			gone = true
			continue
		}
		keep = append(keep, n)
	}
	if len(keep) == 0 {
		delete(d.Game.Artwork, role)
	} else {
		d.Game.Artwork[role] = keep
	}
	return gone
}

// ShowFirst makes one of a role's pictures the one shown.
func (d *Doc) ShowFirst(role, name string) bool {
	if d.Game == nil {
		return false
	}
	p := d.Game.Artwork[role]
	for i, n := range p {
		if n == name {
			out := append(Pictures{n}, p[:i]...)
			d.Game.Artwork[role] = append(out, p[i+1:]...)
			return true
		}
	}
	return false
}

func (d *Doc) putFile(name string, b []byte) {
	if d.Files == nil {
		d.Files = map[string][]byte{}
	}
	d.Files[name] = b
}

// freeImageName is images/<role><ext> for the first picture, else images/<role>-<n><ext> with the
// lowest n not in use.
func (d *Doc) freeImageName(role, ext string, first bool) string {
	inUse := func(n string) bool {
		if _, ok := d.Files[n]; ok {
			return true
		}
		if d.Game != nil {
			for _, p := range d.Game.Artwork {
				for _, q := range p {
					if q == n {
						return true
					}
				}
			}
		}
		return false
	}
	base := "images/" + role + ext
	var shown string
	if d.Game != nil {
		shown = d.Game.Artwork.First(role)
	}
	if first && (shown == base || !inUse(base)) {
		return base
	}
	for n := 2; ; n++ {
		if c := "images/" + role + "-" + strconv.Itoa(n) + ext; !inUse(c) {
			return c
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"net"
	"testing"
)

// A core fetched from an address someone typed can't be used to reach this machine or its network,
// however that address is written.
func TestFetchedAddressesAreOnTheInternet(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "10.0.0.1": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false, "0.0.0.0": false, "100.64.0.1": false,
		"::1": false, "::": false, "fe80::1": false, "fc00::1": false, "ff02::1": false,
		"::ffff:127.0.0.1": false, "::ffff:169.254.169.254": false,
		"64:ff9b::7f00:1": false, "64:ff9b::a9fe:a9fe": false, "64:ff9b::a00:1": false, "2002:7f00:1::": false, "2002:a00:1::": false, "2001:0:0:0::1": false,
		"198.18.0.1": false, "192.0.2.1": false, "240.0.0.1": false,
	} {
		ip := net.ParseIP(addr)
		if ip == nil {
			t.Fatalf("%s isn't an address", addr)
		}
		if got := onTheInternet(ip); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
}

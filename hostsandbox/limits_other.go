//go:build unix && !linux && !freebsd && !darwin

package hostsandbox

// moreLimits has nothing to add on a system this project doesn't ship to.
func moreLimits(Limits) {}

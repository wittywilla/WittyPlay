package main

import (
	"fmt"
	"math/rand/v2"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A network that isn't this machine, for measuring how a stream copes with one. The browser
// is given this relay's port instead of the server's, and everything between them goes
// through here to be delayed, limited and lost.

type impairment struct {
	delay, jitter time.Duration
	loss          float64 // from 0 to 1, on what reaches the browser
	kbps          int     // what reaches the browser, 0 for no limit
	queue         time.Duration
}

func parseImpairment(s string) (impairment, error) {
	im := impairment{queue: 100 * time.Millisecond}
	if s == "" {
		return im, nil
	}
	for _, kv := range strings.Split(s, ",") {
		k, v, _ := strings.Cut(kv, "=")
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return im, fmt.Errorf("can't read %q", kv)
		}
		switch k {
		case "delay":
			im.delay = time.Duration(n * float64(time.Millisecond))
		case "jitter":
			im.jitter = time.Duration(n * float64(time.Millisecond))
		case "loss":
			im.loss = n / 100
		case "kbps":
			im.kbps = int(n)
		case "queue":
			im.queue = time.Duration(n * float64(time.Millisecond))
		default:
			return im, fmt.Errorf("no such setting: %s", k)
		}
	}
	return im, nil
}

// relay carries packets between one browser and the server's fixed UDP port.
type relay struct {
	im     impairment
	front  *net.UDPConn // what the browser talks to
	server *net.UDPAddr

	mu      sync.Mutex
	backs   map[string]*net.UDPConn
	blocked atomic.Bool  // a network that has gone away
	busy    atomic.Int64 // when the limited link is free, in nanoseconds since start
	start   time.Time
	dropped atomic.Int64
	carried atomic.Int64
}

func startRelay(bind string, serverPort int, im impairment) (*relay, error) {
	front, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(bind)})
	if err != nil {
		return nil, err
	}
	r := &relay{im: im, front: front, server: &net.UDPAddr{IP: net.ParseIP(bind), Port: serverPort}, backs: map[string]*net.UDPConn{}, start: time.Now()}
	go r.run()
	return r, nil
}

func (r *relay) port() int { return r.front.LocalAddr().(*net.UDPAddr).Port }

// answer points a browser at the relay instead of the server.
func (r *relay) answer(sdp string, serverPort int) string {
	re := regexp.MustCompile(`( udp \d+ \S+ )` + strconv.Itoa(serverPort) + `( typ host)`)
	return re.ReplaceAllString(sdp, "${1}"+strconv.Itoa(r.port())+"${2}")
}

func (r *relay) run() {
	buf := make([]byte, 2048)
	for {
		n, from, err := r.front.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if r.blocked.Load() {
			continue
		}
		back, err := r.backFor(from)
		if err != nil {
			continue
		}
		p := append([]byte(nil), buf[:n]...)
		r.after(r.im.delay/2, func() { _, _ = back.WriteToUDP(p, r.server) })
	}
}

func (r *relay) backFor(from *net.UDPAddr) (*net.UDPConn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b := r.backs[from.String()]; b != nil {
		return b, nil
	}
	b, err := net.ListenUDP("udp4", &net.UDPAddr{IP: r.server.IP})
	if err != nil {
		return nil, err
	}
	r.backs[from.String()] = b
	to := *from
	// Jitter delays packets unevenly, but one path keeps them in order: a packet never
	// overtakes the one before it. Each is sent from a queue of its own, in order.
	out := make(chan timedPacket, 4096)
	go func() {
		for tp := range out {
			if d := time.Until(tp.at); d > 0 {
				time.Sleep(d)
			}
			_, _ = r.front.WriteToUDP(tp.b, &to)
		}
	}()
	go func() {
		defer close(out)
		var last time.Time
		buf := make([]byte, 2048)
		for {
			n, _, err := b.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if r.blocked.Load() || (r.im.loss > 0 && rand.Float64() < r.im.loss) {
				r.dropped.Add(1)
				continue
			}
			wait := r.im.delay / 2
			if r.im.jitter > 0 {
				wait += time.Duration(rand.Float64() * float64(r.im.jitter))
			}
			if r.im.kbps > 0 {
				// A link of this speed with this much queue: what doesn't fit is lost.
				now := time.Since(r.start).Nanoseconds()
				free := r.busy.Load()
				if free < now {
					free = now
				}
				if time.Duration(free-now) > r.im.queue {
					r.dropped.Add(1)
					continue
				}
				took := int64(n*8) * 1e6 / int64(r.im.kbps)
				r.busy.Store(free + took)
				wait += time.Duration(free + took - now)
			}
			r.carried.Add(1)
			at := time.Now().Add(wait)
			if at.Before(last) {
				at = last
			}
			last = at
			select {
			case out <- timedPacket{at, append([]byte(nil), buf[:n]...)}:
			default:
				r.dropped.Add(1)
			}
		}
	}()
	return b, nil
}

func (r *relay) after(d time.Duration, f func()) {
	if d <= 0 {
		f()
		return
	}
	time.AfterFunc(d, f)
}

type timedPacket struct {
	at time.Time
	b  []byte
}

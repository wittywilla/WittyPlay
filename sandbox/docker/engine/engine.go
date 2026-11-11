package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/wittywilla/wittyplay/sandbox/docker"
)

// Engine is the Runtime backed by a real Docker daemon, through the Docker Engine SDK. This is
// the only package in WittyPlay that imports it, so nothing else links it.
type Engine struct {
	c    *client.Client
	host string
}

// Connect makes an Engine for DOCKER_HOST, or the platform's default socket. It doesn't reach
// the daemon: Available does, and says what it found.
func Connect() (*Engine, error) {
	c, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", docker.ErrUnavailable, err)
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = c.DaemonHost()
	}
	return &Engine{c: c, host: host}, nil
}

// Close lets go of the connection.
func (e *Engine) Close() error { return e.c.Close() }

// Available pings the daemon, with whatever time ctx allows.
func (e *Engine) Available(ctx context.Context) docker.Status {
	st := docker.Status{Host: e.host}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := e.c.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Available, st.APIVersion, st.OS = true, p.APIVersion, p.OSType
	return st
}

// Pull fetches an image, which must be pinned to a digest.
func (e *Engine) Pull(ctx context.Context, ref string) error {
	if !docker.Pinned(ref) {
		return docker.ErrNotPinned
	}
	resp, err := e.c.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer resp.Close()
	return resp.Wait(ctx)
}

// Create checks the spec again, then makes the container from it, field by field.
func (e *Engine) Create(ctx context.Context, s docker.Spec) (string, error) {
	if err := docker.Check(s); err != nil {
		return "", fmt.Errorf("refusing to create a container: %w", err)
	}
	stop := int(s.StopTimeout / time.Second)
	cfg := &container.Config{
		Image:       s.Image,
		User:        s.User,
		Env:         s.Env,
		Cmd:         s.Cmd,
		Labels:      s.Labels,
		StopTimeout: &stop,
	}
	pids := s.PidsLimit
	init := s.Init
	hc := &container.HostConfig{
		NetworkMode:    container.NetworkMode(s.Network),
		ReadonlyRootfs: s.ReadOnlyRoot,
		CapDrop:        s.CapDrop,
		CapAdd:         s.CapAdd,
		SecurityOpt:    s.SecurityOpt,
		Privileged:     s.Privileged,
		Init:           &init,
		ShmSize:        s.ShmBytes,
		Tmpfs:          s.Tmpfs,
		ExtraHosts:     s.ExtraHosts,
		Resources: container.Resources{
			Memory:     s.MemoryBytes,
			MemorySwap: s.MemoryBytes, // no swap beyond the limit
			NanoCPUs:   s.NanoCPUs,
			PidsLimit:  &pids,
		},
	}
	for _, m := range s.Mounts {
		hc.Mounts = append(hc.Mounts, mount.Mount{Type: mount.TypeBind, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly,
			BindOptions: &mount.BindOptions{Propagation: mount.PropagationRPrivate}})
	}
	for _, d := range s.Devices {
		hc.Devices = append(hc.Devices, container.DeviceMapping{PathOnHost: d, PathInContainer: d, CgroupPermissions: "rw"})
	}
	if s.NvidiaGPUs {
		hc.DeviceRequests = []container.DeviceRequest{{Driver: "nvidia", Count: -1, Capabilities: [][]string{{"gpu", "video", "graphics", "compute", "utility"}}}}
	}
	if len(s.Publish) > 0 {
		cfg.ExposedPorts = network.PortSet{}
		hc.PortBindings = network.PortMap{}
		for _, p := range s.Publish {
			port, err := network.ParsePort(strconv.Itoa(p.Port) + "/" + p.Proto)
			if err != nil {
				return "", err
			}
			cfg.ExposedPorts[port] = struct{}{}
			hc.PortBindings[port] = []network.PortBinding{{HostIP: p.HostIP, HostPort: ""}} // a free port, on loopback
		}
	}
	res, err := e.c.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hc, Name: s.Name})
	if err != nil {
		return "", err
	}
	return res.ID, nil
}

func (e *Engine) Start(ctx context.Context, id string) error {
	_, err := e.c.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

func (e *Engine) Stop(ctx context.Context, id string, timeout time.Duration) error {
	secs := int(timeout / time.Second)
	_, err := e.c.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &secs})
	return err
}

func (e *Engine) Remove(ctx context.Context, id string) error {
	_, err := e.c.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	return err
}

// Logs returns the end of what a container said, at most 64 KiB.
func (e *Engine) Logs(ctx context.Context, id string, tail int) (string, error) {
	rc, err := e.c.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(max(tail, 1))})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	var out bytes.Buffer
	lim := &limitWriter{w: &out, n: 64 << 10}
	if _, err := stdcopy.StdCopy(lim, lim, rc); err != nil && !errors.Is(err, errFull) {
		return out.String(), err
	}
	return out.String(), nil
}

var errFull = errors.New("full")

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errFull
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	n, err := l.w.Write(p)
	l.n -= n
	return n, err
}

func (e *Engine) Inspect(ctx context.Context, id string) (docker.Info, error) {
	res, err := e.c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return docker.Info{}, err
	}
	c := res.Container
	info := docker.Info{ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), Image: c.Image}
	if c.Config != nil {
		info.Labels = c.Config.Labels
	}
	if c.State != nil {
		info.Running, info.State = c.State.Running, string(c.State.Status)
	}
	if t, err := time.Parse(time.RFC3339Nano, c.Created); err == nil {
		info.Created = t
	}
	if ns := c.NetworkSettings; ns != nil {
		for _, ep := range ns.Networks {
			if ep != nil && ep.IPAddress.IsValid() {
				info.IP = ep.IPAddress
				break
			}
		}
		for port, binds := range ns.Ports {
			for _, b := range binds {
				if n, err := strconv.Atoi(b.HostPort); err == nil && b.HostIP.IsLoopback() {
					if info.HostPorts == nil {
						info.HostPorts = map[string]int{}
					}
					info.HostPorts[port.String()] = n
				}
			}
		}
	}
	return info, nil
}

func (e *Engine) List(ctx context.Context, labels map[string]string) ([]docker.Info, error) {
	f := make(client.Filters)
	for k, v := range labels {
		f.Add("label", k+"="+v)
	}
	res, err := e.c.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	out := make([]docker.Info, 0, len(res.Items))
	for _, c := range res.Items {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, docker.Info{ID: c.ID, Name: name, Image: c.Image, Labels: c.Labels, State: string(c.State),
			Running: c.State == container.StateRunning, Created: time.Unix(c.Created, 0)})
	}
	return out, nil
}

// EnsureNetwork finds WittyPlay's network or makes it: a bridge of its own, with no
// inter-container traffic, so one session can't reach another.
func (e *Engine) EnsureNetwork(ctx context.Context, n docker.NetworkSpec) (docker.NetworkInfo, error) {
	got, err := e.c.NetworkInspect(ctx, n.Name, client.NetworkInspectOptions{})
	if err != nil {
		if _, cerr := e.c.NetworkCreate(ctx, n.Name, client.NetworkCreateOptions{
			Driver:   "bridge",
			Internal: n.Internal,
			Options: map[string]string{
				"com.docker.network.bridge.enable_icc":           "false",
				"com.docker.network.bridge.host_binding_ipv4":    "127.0.0.1",
				"com.docker.network.bridge.enable_ip_masquerade": strconv.FormatBool(!n.Internal),
			},
			Labels: map[string]string{docker.LabelManaged: "true"},
		}); cerr != nil {
			return docker.NetworkInfo{}, fmt.Errorf("making WittyPlay's Docker network: %w", cerr)
		}
		if got, err = e.c.NetworkInspect(ctx, n.Name, client.NetworkInspectOptions{}); err != nil {
			return docker.NetworkInfo{}, err
		}
	}
	info := docker.NetworkInfo{ID: got.Network.ID, Name: got.Network.Name, Internal: got.Network.Internal}
	for _, c := range got.Network.IPAM.Config {
		if c.Gateway.IsValid() {
			info.Gateway = c.Gateway
			break
		}
	}
	if !info.Gateway.IsValid() {
		info.Gateway = netip.Addr{}
	}
	return info, nil
}

var _ docker.Runtime = (*Engine)(nil)

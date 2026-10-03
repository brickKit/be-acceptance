// Package infra starts the suite's own throwaway infrastructure (a Docker network,
// PostgreSQL 16, NATS with JetStream) and the containers of the component under test,
// through the docker CLI. Every container and the network carry the run's prefix and are
// removed by Env.Close.
package infra

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Images of the throwaway infrastructure.
const (
	PostgresImage = "postgres:16-alpine"
	NATSImage     = "nats:2.12-alpine"
)

// Env is one run's Docker network and the containers started in it.
type Env struct {
	Prefix  string
	Network string
	dir     string
	mu      sync.Mutex
	names   []string
}

func docker(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// NewEnv creates the network <prefix>-net (IPv6 enabled when the daemon allows it).
// dir holds env files; it should be private to the run.
func NewEnv(ctx context.Context, prefix, dir string) (*Env, error) {
	e := &Env{Prefix: prefix, Network: prefix + "-net", dir: dir}
	if _, err := docker(ctx, "network", "create", "--ipv6", e.Network); err != nil {
		if _, err2 := docker(ctx, "network", "create", e.Network); err2 != nil {
			return nil, err2
		}
	}
	return e, nil
}

// Close removes every container of the run and the network.
func (e *Env) Close(ctx context.Context) {
	e.mu.Lock()
	names := append([]string(nil), e.names...)
	e.mu.Unlock()
	if len(names) > 0 {
		_, _ = docker(ctx, append([]string{"rm", "-f", "-v"}, names...)...)
	}
	_, _ = docker(ctx, "network", "rm", e.Network)
}

// RunSpec describes one container.
type RunSpec struct {
	Name           string // suffix; the container is <prefix>-<name>
	Image          string
	Aliases        []string // network aliases
	Env            []string // KEY=VALUE, passed through a 0600 env file
	Mounts         []string // docker -v values
	Ports          []int    // container ports published on 127.0.0.1 at random host ports
	Cmd            []string
	AddHostGateway bool // host.docker.internal -> the host, where the fakes listen
}

// Container is one started container.
type Container struct {
	Name string
}

// Run starts a detached container.
func (e *Env) Run(ctx context.Context, s RunSpec) (*Container, error) {
	name := e.Prefix + "-" + s.Name
	args := []string{"run", "-d", "--name", name, "--network", e.Network}
	for _, a := range s.Aliases {
		args = append(args, "--network-alias", a)
	}
	if len(s.Env) > 0 {
		f := filepath.Join(e.dir, name+".env")
		if err := writeEnvFile(f, s.Env); err != nil {
			return nil, err
		}
		args = append(args, "--env-file", f)
	}
	for _, m := range s.Mounts {
		args = append(args, "-v", m)
	}
	for _, p := range s.Ports {
		args = append(args, "-p", "127.0.0.1::"+strconv.Itoa(p))
	}
	if s.AddHostGateway {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	args = append(append(args, s.Image), s.Cmd...)
	e.mu.Lock()
	e.names = append(e.names, name)
	e.mu.Unlock()
	if _, err := docker(ctx, args...); err != nil {
		return nil, err
	}
	return &Container{Name: name}, nil
}

// writeEnvFile writes KEY=VALUE lines; docker env files cannot carry a newline in a value.
func writeEnvFile(path string, kv []string) error {
	var b strings.Builder
	for _, l := range kv {
		if strings.ContainsAny(l, "\r\n") {
			k, _, _ := strings.Cut(l, "=")
			return fmt.Errorf("env %s: a value with a newline cannot travel in an env file", k)
		}
		b.WriteString(l + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// Port returns the host port published for a container port.
func (c *Container) Port(ctx context.Context, p int) (int, error) {
	out, err := docker(ctx, "port", c.Name, strconv.Itoa(p)+"/tcp")
	if err != nil {
		return 0, err
	}
	line := strings.SplitN(out, "\n", 2)[0]
	i := strings.LastIndex(line, ":")
	return strconv.Atoi(strings.TrimSpace(line[i+1:]))
}

// Exec runs a command in the container and returns its combined output and exit code.
func (c *Container) Exec(ctx context.Context, cmd ...string) (string, int, error) {
	x := exec.CommandContext(ctx, "docker", append([]string{"exec", c.Name}, cmd...)...)
	out, err := x.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode(), nil
	}
	return string(out), 0, err
}

// Signal sends a signal (TERM, KILL, …) to the container's main process.
func (c *Container) Signal(ctx context.Context, sig string) error {
	_, err := docker(ctx, "kill", "-s", sig, c.Name)
	return err
}

// Stop stops the container, giving it timeout seconds.
func (c *Container) Stop(ctx context.Context, timeout int) error {
	_, err := docker(ctx, "stop", "-t", strconv.Itoa(timeout), c.Name)
	return err
}

// Start starts a stopped container.
func (c *Container) Start(ctx context.Context) error {
	_, err := docker(ctx, "start", c.Name)
	return err
}

// Wait waits for the container to exit and returns its exit code.
func (c *Container) Wait(ctx context.Context, timeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := docker(ctx, "wait", c.Name)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(out)
}

// Running reports whether the container runs, and its exit code when it does not.
func (c *Container) Running(ctx context.Context) (bool, int, error) {
	out, err := docker(ctx, "inspect", "-f", "{{.State.Running}} {{.State.ExitCode}}", c.Name)
	if err != nil {
		return false, -1, err
	}
	r, code, _ := strings.Cut(out, " ")
	n, _ := strconv.Atoi(code)
	return r == "true", n, nil
}

// InspectEnv returns the container's configured environment (KEY=VALUE).
func (c *Container) InspectEnv(ctx context.Context) ([]string, error) {
	out, err := docker(ctx, "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", c.Name)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(out), "\n"), nil
}

// FollowLogs streams stdout and stderr lines to feed until the container stops or ctx ends.
func (c *Container) FollowLogs(ctx context.Context, feed func(stream, line string)) error {
	cmd := exec.CommandContext(ctx, "docker", "logs", "-f", c.Name)
	so, _ := cmd.StdoutPipe()
	se, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	pump := func(stream string, r *bufio.Scanner) {
		defer wg.Done()
		r.Buffer(make([]byte, 1<<20), 1<<20)
		for r.Scan() {
			feed(stream, r.Text())
		}
	}
	wg.Add(2)
	go pump("stdout", bufio.NewScanner(so))
	go pump("stderr", bufio.NewScanner(se))
	wg.Wait()
	return cmd.Wait()
}

// Logs returns everything the container wrote so far (stdout then stderr).
func (c *Container) Logs(ctx context.Context) (string, error) {
	x := exec.CommandContext(ctx, "docker", "logs", c.Name)
	out, err := x.CombinedOutput()
	return string(out), err
}

// ImageDigest is the image's content ID (sha256:…).
func ImageDigest(ctx context.Context, ref string) (string, error) {
	return docker(ctx, "image", "inspect", "-f", "{{.Id}}", ref)
}

// Remove removes the container and its anonymous volumes.
func (c *Container) Remove(ctx context.Context) error {
	_, err := docker(ctx, "rm", "-f", "-v", c.Name)
	return err
}

// Pause freezes every process of the container (a holder that stops renewing, without dying).
func (c *Container) Pause(ctx context.Context) error {
	_, err := docker(ctx, "pause", c.Name)
	return err
}

// Unpause resumes a paused container.
func (c *Container) Unpause(ctx context.Context) error {
	_, err := docker(ctx, "unpause", c.Name)
	return err
}

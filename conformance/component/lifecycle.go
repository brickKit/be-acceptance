package compconf

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"github.com/brickKit/be-acceptance/conformance/component/infra"
)

// spec is the container spec of the component with an environment and a command.
func (r *Run) spec(name string, env, cmd []string) infra.RunSpec {
	ports := []int{r.comp.Manifest.Deployment.Port}
	for _, p := range r.comp.Manifest.Deployment.ExtraPorts {
		ports = append(ports, p.Port)
	}
	return infra.RunSpec{Name: name, Image: r.opt.Image, Env: env, Cmd: cmd, Ports: ports, AddHostGateway: true,
		Mounts: []string{filepath.Join(r.runDir, "secrets") + ":" + SecretsRoot + ":ro"}}
}

// runOnce runs the image to completion and returns its exit code and output.
func (r *Run) runOnce(ctx context.Context, name string, env, cmd []string, timeout time.Duration) (int, string, error) {
	c, err := r.env.Run(ctx, r.spec(fmt.Sprintf("%s-%d", name, r.seq.Add(1)), env, cmd))
	if err != nil {
		return -1, "", err
	}
	defer func() { _ = c.Remove(context.Background()) }()
	code, err := c.Wait(ctx, timeout)
	logs, _ := c.Logs(context.Background())
	if err != nil {
		return -1, logs, fmt.Errorf("did not exit within %v", timeout)
	}
	return code, logs, nil
}

// instance is a serving container of the component.
type instance struct {
	c        *infra.Container
	base     string
	grpcAddr string
	logs     *fakes.Logs
	stop     context.CancelFunc
}

// startInstance starts the serve entry point with env and follows its logs into logs.
func (r *Run) startInstance(ctx context.Context, name string, env []string, logs *fakes.Logs) (*instance, error) {
	c, err := r.env.Run(ctx, r.spec(fmt.Sprintf("%s-%d", name, r.seq.Add(1)), env, nil))
	if err != nil {
		return nil, err
	}
	in := &instance{c: c, logs: logs}
	lctx, cancel := context.WithCancel(context.Background())
	in.stop = cancel
	go func() { _ = c.FollowLogs(lctx, logs.Feed) }()
	p, err := c.Port(ctx, r.comp.Manifest.Deployment.Port)
	if err != nil {
		return in, err
	}
	in.base = "http://127.0.0.1:" + strconv.Itoa(p)
	if gp := r.comp.GRPCPort(); gp != 0 {
		if hp, err := c.Port(ctx, gp); err == nil {
			in.grpcAddr = "127.0.0.1:" + strconv.Itoa(hp)
		}
	}
	return in, nil
}

// startMain starts the instance every steady-state step uses.
func (r *Run) startMain(ctx context.Context) error {
	in, err := r.startInstance(ctx, "svc", r.compEnv, r.logs)
	if in != nil {
		r.main, r.base, r.grpcAddr, r.stopLogs = in.c, in.base, in.grpcAddr, in.stop
	}
	return err
}

// restartMain replaces the main instance with a fresh one and waits until it is ready. A case
// that has just shown a fault which leaves the component unable to serve calls it, so that one
// fault fails one case and not every case after it.
func (r *Run) restartMain(ctx context.Context) error {
	if r.main != nil {
		_ = r.main.Stop(ctx, r.comp.Manifest.Deployment.StopGracePeriodSeconds)
		_ = r.main.Remove(ctx)
	}
	if r.stopLogs != nil {
		r.stopLogs()
	}
	r.mainUp = false
	if err := r.startMain(ctx); err != nil {
		return err
	}
	if _, err := waitStatus(ctx, r.base+"/readyz", 200, 60*time.Second); err != nil {
		return err
	}
	r.mainUp = true
	return nil
}

// waitStatus polls url until it answers want or the timeout passes; it returns the last status.
func waitStatus(ctx context.Context, url string, want int, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	last, lastErr := 0, error(nil)
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := httpClient.Do(req)
		if err == nil {
			last, lastErr = resp.StatusCode, nil
			resp.Body.Close()
			if last == want {
				return last, nil
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if lastErr != nil {
				return last, lastErr
			}
			return last, fmt.Errorf("answered %d, not %d, for %v", last, want, timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// startPeriod is how long the component may take to answer /healthz at start.
func (r *Run) startPeriod() time.Duration {
	if s := r.comp.Manifest.HealthCheck.StartPeriodSeconds; s > 0 {
		return time.Duration(s) * time.Second
	}
	return 60 * time.Second
}

// call makes one request to the main instance. Every request carries a fresh X-Request-Id
// unless an option sets one, so its access-log line can be found.
func (r *Run) call(ctx context.Context, method, target string, opts ...reqOpt) *exchange {
	return r.callAt(ctx, r.base, method, target, opts...)
}

func (r *Run) callAt(ctx context.Context, base, method, target string, opts ...reqOpt) *exchange {
	rid := fmt.Sprintf("compconf-%d-%s", r.seq.Add(1), infra.RandHex(3))
	all := append([]reqOpt{withHeader("X-Request-Id", rid)}, opts...)
	return send(ctx, r.rec, base, method, target, all...)
}

// envWith returns the component env with some keys replaced (value "" with del = removed).
func envWith(env []string, set map[string]string, del ...string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if contains(del, k) {
			continue
		}
		if _, ok := set[k]; ok {
			continue
		}
		out = append(out, kv)
	}
	for _, k := range sortedKeys(set) {
		out = append(out, k+"="+set[k])
	}
	return out
}

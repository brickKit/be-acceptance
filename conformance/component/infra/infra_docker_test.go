//go:build compconf_docker

package infra

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestEnvStartsThrowawayInfrastructure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env, err := NewEnv(ctx, fmt.Sprintf("sdkb-acc-it%d", time.Now().Unix()%100000), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close(context.Background())

	pg, err := env.StartPostgres(ctx, PostgresImage)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, pg.HostDSN("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	var v string
	_ = conn.QueryRow(ctx, "SHOW server_version").Scan(&v)
	conn.Close(ctx)
	if !strings.HasPrefix(v, "16.") {
		t.Fatalf("postgres version %q", v)
	}

	nats, err := env.StartNATS(ctx, NATSImage)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", nats.HostPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	// A plain container: env file, port publishing, exec, signal, exit code, logs.
	box, err := env.Run(ctx, RunSpec{Name: "box", Image: "alpine:3", Env: []string{"HELLO=world"},
		Ports: []int{8080}, AddHostGateway: true,
		Cmd: []string{"sh", "-c", "echo out-$HELLO; echo err-line >&2; trap 'exit 7' TERM; while :; do sleep 0.2; done"}})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := box.Port(ctx, 8080); err != nil || p == 0 {
		t.Fatalf("port: %d %v", p, err)
	}
	out, code, err := box.Exec(ctx, "sh", "-c", "command -v wget && exit 3")
	if err != nil || code != 3 || !strings.Contains(out, "wget") {
		t.Fatalf("exec: %q %d %v", out, code, err)
	}
	envs, _ := box.InspectEnv(ctx)
	if !contains(envs, "HELLO=world") {
		t.Fatalf("env = %v", envs)
	}
	var mu sync.Mutex
	var lines []string
	go box.FollowLogs(ctx, func(stream, line string) { mu.Lock(); lines = append(lines, stream+":"+line); mu.Unlock() })
	time.Sleep(time.Second)
	if err := box.Signal(ctx, "TERM"); err != nil {
		t.Fatal(err)
	}
	exit, err := box.Wait(ctx, 10*time.Second)
	if err != nil || exit != 7 {
		t.Fatalf("exit = %d %v", exit, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !contains(lines, "stdout:out-world") || !contains(lines, "stderr:err-line") {
		t.Fatalf("logs = %v", lines)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

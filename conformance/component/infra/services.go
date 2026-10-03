package infra

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
)

// Postgres is the run's throwaway PostgreSQL; components reach it as pg:5432.
type Postgres struct {
	C        *Container
	HostPort int
	Password string
	Alias    string
}

// StartPostgres starts PostgreSQL and waits until it accepts TCP connections.
func (e *Env) StartPostgres(ctx context.Context, image string) (*Postgres, error) {
	pw := randHex(12)
	c, err := e.Run(ctx, RunSpec{Name: "pg", Image: image, Aliases: []string{"pg"}, Ports: []int{5432},
		Env: []string{"POSTGRES_PASSWORD=" + pw}})
	if err != nil {
		return nil, err
	}
	p := &Postgres{C: c, Password: pw, Alias: "pg"}
	if p.HostPort, err = c.Port(ctx, 5432); err != nil {
		return nil, err
	}
	return p, p.WaitReady(ctx, 60*time.Second)
}

// HostDSN is a superuser DSN through the published port.
func (p *Postgres) HostDSN(db string) string {
	return fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%d/%s?sslmode=disable", p.Password, p.HostPort, db)
}

// WaitReady waits until a superuser query succeeds.
func (p *Postgres) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, err := pgx.Connect(cctx, p.HostDSN("postgres"))
		if err == nil {
			_, err = conn.Exec(cctx, "SELECT 1")
			conn.Close(cctx)
		}
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("postgres not ready: %w", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Stop stops PostgreSQL (the published port may change on restart; Start refreshes it).
func (p *Postgres) Stop(ctx context.Context) error { return p.C.Stop(ctx, 2) }

// Start starts it again and waits until it is ready.
func (p *Postgres) Start(ctx context.Context) error {
	if err := p.C.Start(ctx); err != nil {
		return err
	}
	var err error
	if p.HostPort, err = p.C.Port(ctx, 5432); err != nil {
		return err
	}
	return p.WaitReady(ctx, 60*time.Second)
}

// NATS is the run's throwaway NATS with JetStream; components reach it as nats:4222.
type NATS struct {
	C        *Container
	HostPort int
	Alias    string
}

// StartNATS starts nats-server -js and waits until it accepts TCP connections.
func (e *Env) StartNATS(ctx context.Context, image string) (*NATS, error) {
	// A fixed host port: a restarted container keeps it, so clients reconnect (a random one
	// would change on docker start).
	hp, err := freePort()
	if err != nil {
		return nil, err
	}
	c, err := e.Run(ctx, RunSpec{Name: "nats", Image: image, Aliases: []string{"nats"}, FixedPorts: map[int]int{4222: hp},
		Cmd: []string{"-js"}})
	if err != nil {
		return nil, err
	}
	n := &NATS{C: c, Alias: "nats"}
	if n.HostPort, err = c.Port(ctx, 4222); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", n.HostPort), time.Second)
		if err == nil {
			buf := make([]byte, 4)
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, err = conn.Read(buf)
			conn.Close()
			if err == nil && string(buf) == "INFO" {
				return n, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("nats not ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RandHex returns n random bytes as hex (for random identities and secrets).
func RandHex(n int) string { return randHex(n) }

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

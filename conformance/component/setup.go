package compconf

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"github.com/brickKit/be-acceptance/conformance/component/infra"
	beprotocol "github.com/brickKit/be-protocol"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
)

const (
	tenantID = "compconf"
	issuer   = "urn:be:compconf:iam"
)

// setup starts the throwaway infrastructure and the fakes, creates the database identity and
// builds the component's environment and secret files.
func (r *Run) setup(ctx context.Context) error {
	var err error
	if r.runDir, err = os.MkdirTemp("", "compconf-"); err != nil {
		return err
	}
	if r.env, err = infra.NewEnv(ctx, r.opt.Prefix, r.runDir); err != nil {
		return err
	}
	if err := r.startFakes(); err != nil {
		return err
	}
	if r.comp.HasConfigKey("PG_SCHEMA") || r.comp.HasConfigKey("PG_HOST") {
		if r.pg, err = r.env.StartPostgres(ctx, infra.PostgresImage); err != nil {
			return err
		}
		if err := r.createIdentity(ctx); err != nil {
			return err
		}
	}
	if r.comp.HasConfigKey("NATS_URL") || r.comp.HasConfigKey("EVENT_BUS_URL") {
		if r.nats, err = r.env.StartNATS(ctx, infra.NATSImage); err != nil {
			return err
		}
		if r.natsConn, err = nats.Connect(fmt.Sprintf("nats://127.0.0.1:%d", r.nats.HostPort)); err == nil {
			r.authz.OnChange = r.poke
		}
	}
	r.setupPersonas()
	if err := r.buildEnv(); err != nil {
		return err
	}
	if r.comp.GRPCPort() != 0 {
		contracts, _ := fs.Sub(r.comp.FS, "contracts")
		protoRoot, _ := fs.Sub(beprotocol.FS, "proto")
		if r.methods, err = fakes.CompileProtos(contracts, fakes.ProtoImports(protoRoot)...); err != nil {
			return fmt.Errorf("compiling the component's protos: %w", err)
		}
	}
	return nil
}

func (r *Run) startFakes() error {
	var err error
	if r.iam, err = fakes.NewIAM(issuer, tenantID); err != nil {
		return err
	}
	r.iamSrv = fakes.NewServer(r.iam.Handler())
	r.authz = fakes.NewAuthz()
	r.authzSrv = fakes.NewServer(r.authz.Handler())
	r.obs = fakes.NewObserver()
	r.obsSrv = fakes.NewServer(r.obs.Handler())
	for _, s := range []*fakes.Server{r.iamSrv, r.authzSrv, r.obsSrv} {
		if err := s.Start("0.0.0.0:0"); err != nil {
			return err
		}
	}
	if err := r.authz.StartGRPC("0.0.0.0:0"); err != nil {
		return err
	}
	r.iam.SetBaseURL(r.iamSrv.URL(r.opt.FakeHost))
	for _, d := range r.comp.Dependencies() {
		if d.Optional { // the suite never installs optional dependencies (P2.5)
			continue
		}
		p, err := r.newPeer(d.ID)
		if err != nil {
			return err
		}
		if err := p.Start("0.0.0.0:0", "0.0.0.0:0"); err != nil {
			return err
		}
		r.peers[d.ID] = p
	}
	return nil
}

func (r *Run) newPeer(id string) (*fakes.Peer, error) {
	protoRoot, _ := fs.Sub(beprotocol.FS, "proto")
	var contracts fs.FS
	switch {
	case r.opt.DepContracts[id] != "":
		contracts = os.DirFS(r.opt.DepContracts[id])
	case id == "conformance/peer":
		contracts, _ = fs.Sub(beprotocol.FS, "fixtures/peer/contracts")
	default:
		return nil, fmt.Errorf("no contracts for dependency %s: pass --dep-contracts %s=<dir>", id, id)
	}
	p, err := fakes.NewPeer(id, contracts, fakes.ProtoImports(protoRoot)...)
	if err != nil {
		return nil, err
	}
	for call, c := range r.comp.Fixtures.Dependencies[id] {
		a := fakes.PeerAnswer{Hang: c.Hang}
		if c.ResponseFile != "" {
			if a.Response, err = r.comp.FixtureFile(c.ResponseFile); err != nil {
				return nil, err
			}
		} else if c.Response != nil {
			a.Response, _ = json.Marshal(c.Response)
		}
		a.Response = []byte(r.substitute(string(a.Response), ""))
		p.SetAnswer(call, a)
	}
	return p, nil
}

func (r *Run) poke() {
	if r.natsConn == nil {
		return
	}
	b, _ := json.Marshal(map[string]any{"revision": strconv.FormatInt(r.authz.Revision(), 10), "bundle": true})
	_ = r.natsConn.Publish("infra.authz.changed.v1", b)
}

// createIdentity creates a random database, owner role, runtime role and schema the way the
// project's database initialisation does (F27): the owner owns the schema; the runtime role
// gets USAGE and, through default privileges, DML only.
func (r *Run) createIdentity(ctx context.Context) error {
	h := infra.RandHex(4)
	r.db = dbIdentity{Database: "compconf", Owner: "cc_" + h + "_owner", OwnerPassword: infra.RandHex(12),
		Runtime: "cc_" + h + "_rt", RuntimePassword: infra.RandHex(12), Schema: "cc_" + h}
	if err := r.superExec(ctx, "postgres", "CREATE DATABASE compconf"); err != nil {
		return err
	}
	q := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	stmts := []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", q(r.db.Owner), r.db.OwnerPassword),
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", q(r.db.Runtime), r.db.RuntimePassword),
		fmt.Sprintf("CREATE SCHEMA %s AUTHORIZATION %s", q(r.db.Schema), q(r.db.Owner)),
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", q(r.db.Schema), q(r.db.Runtime)),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s",
			q(r.db.Owner), q(r.db.Schema), q(r.db.Runtime)),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT USAGE, SELECT ON SEQUENCES TO %s",
			q(r.db.Owner), q(r.db.Schema), q(r.db.Runtime)),
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
	}
	for _, s := range stmts {
		if err := r.superExec(ctx, r.db.Database, s); err != nil {
			return err
		}
	}
	return nil
}

func (r *Run) superExec(ctx context.Context, db, sql string, args ...any) error {
	conn, err := pgx.Connect(ctx, r.pg.HostDSN(db))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql, args...)
	return err
}

func (r *Run) superQuery(ctx context.Context, db, sql string, args ...any) (string, error) {
	conn, err := pgx.Connect(ctx, r.pg.HostDSN(db))
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var s string
	err = conn.QueryRow(ctx, sql, args...).Scan(&s)
	return s, err
}

// buildEnv computes the environment and writes the secret files, mounted like brickKit does.
func (r *Run) buildEnv() error {
	v := suiteValues{FakeHost: r.opt.FakeHost, AuthzPort: r.authzSrv.Port(), AuthzGRPCPort: r.authz.GRPCPort(), IAMPort: r.iamSrv.Port(),
		ObserverPort: r.obsSrv.Port(), PeerHTTP: map[string]int{}, PeerGRPC: map[string]int{},
		DB: r.db, Issuer: issuer, Tenant: tenantID, HasNATS: r.nats != nil}
	for id, p := range r.peers {
		v.PeerHTTP[id], v.PeerGRPC[id] = p.HTTPServer().Port(), p.GRPCPort()
	}
	for k, p := range r.comp.Manifest.ConfigSchema.Properties {
		if _, given := r.comp.Fixtures.Config[k]; given {
			continue
		}
		if p.Secret && !strings.HasPrefix(k, "PG_") { // the component's own secrets: random values
			if v.Extra == nil {
				v.Extra = map[string]string{}
			}
			v.Extra[k] = "compconf-secret-" + infra.RandHex(8)
		}
	}
	env, secrets, err := componentEnv(r.comp, v)
	if err != nil {
		return err
	}
	r.compEnv, r.secrets = env, secrets
	dir := filepath.Join(r.runDir, "secrets", r.comp.ServiceName())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Join(r.runDir, "secrets"), 0o755)
	for k, val := range secrets {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(val), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (r *Run) teardown() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if r.stopLogs != nil {
		r.stopLogs()
	}
	if r.natsConn != nil {
		r.natsConn.Close()
	}
	for _, s := range []*fakes.Server{r.iamSrv, r.authzSrv, r.obsSrv} {
		if s != nil {
			s.Stop()
		}
	}
	for _, p := range r.peers {
		p.Stop()
	}
	if r.authz != nil {
		r.authz.StopGRPC()
	}
	if r.opt.Keep {
		r.logf("kept containers with prefix %s and %s", r.opt.Prefix, r.runDir)
		return
	}
	if r.env != nil {
		r.env.Close(ctx)
	}
	if r.runDir != "" {
		_ = os.RemoveAll(r.runDir)
	}
}

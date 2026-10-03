// Command rawstub is conformance/rawstub: a be-protocol 1.0 component in plain Go with no
// official SDK, written from the protocol text (profiles core, obs, err, auth).
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
)

const (
	componentID      = "conformance/rawstub"
	componentVersion = "1.0.0"
)

// broken is set at build time (-ldflags "-X main.broken=<name>"), see broken-variants.yaml.
var broken = ""

var brokenVariants = map[string]bool{"": true, "accept-refresh": true, "healthz-db": true, "leak-internal": true, "ipv4-only": true,
	"readyz-live-db": true, "no-redact": true, "no-goaway": true}

// App holds the running component.
type App struct {
	cfg      *Config
	log      *Logger
	metrics  *Metrics
	exporter *Exporter
	bundles  *BundleStore
	verifier *tokenVerifier
	db       *DB
	ready    *Readiness
	routes   []*route
	broken   string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	mode := "serve"
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "migrate" && args[1] == "up":
		mode = "migrate"
	default:
		os.Stderr.WriteString("usage: rawstub [migrate up]\n")
		return 64 // P1.1: before the configuration is read
	}
	if !brokenVariants[broken] {
		newLogger("info", componentID, componentVersion).Error("config_error", F{"key": "BROKEN", "error": "unknown broken variant " + broken})
		return 78
	}
	cfg, probs := loadConfig(os.LookupEnv, mode)
	if probs != nil {
		l := newLogger("info", componentID, componentVersion)
		for _, p := range probs {
			l.Error("configuration error: "+p.Key, F{"key": p.Key, "error.reason": p.Class, "error": p.Detail})
		}
		return 78
	}
	log := newLogger(cfg.LogLevel, cfg.ComponentID, cfg.ComponentVersion)
	if mode == "migrate" {
		return runMigrate(cfg, log)
	}
	return serve(cfg, log)
}

// serve follows P1.2: configuration (done), open the ports, then connect in the background.
func serve(cfg *Config, log *Logger) int {
	m := newMetrics(cfg.ComponentID)
	db, err := openDB(cfg, log, m)
	if err != nil {
		log.Error("config_error", F{"key": "PG_PASSWORD_FILE", "error": err.Error()})
		return 78
	}
	a := &App{cfg: cfg, log: log, metrics: m, exporter: newExporter(cfg, log), db: db, routes: routes(), broken: broken}
	a.bundles = newBundleStore(cfg.AuthzURL, log)
	jwks := newJWKS(cfg.IAMURL, log)
	a.verifier = &tokenVerifier{keys: jwks, issuer: cfg.IAMIssuer, tenant: cfg.TenantID, now: time.Now, acceptRefresh: broken == "accept-refresh"}
	a.ready = &Readiness{bundles: a.bundles}
	m.Gauge("be_authz_bundle_age_seconds", a.bundles.Age)
	m.Gauge("be_db_identity_ok", a.ready.identity)

	network, httpAddr, grpcAddr := "tcp", ":8080", ":9090"
	if broken == "ipv4-only" {
		network, httpAddr, grpcAddr = "tcp4", "0.0.0.0:8080", "0.0.0.0:9090"
	}
	hl, err := net.Listen(network, httpAddr)
	if err != nil {
		log.Error("listen_failed", F{"error": err.Error()})
		return 1
	}
	gl, err := net.Listen(network, grpcAddr)
	if err != nil {
		log.Error("listen_failed", F{"error": err.Error()})
		return 1
	}
	httpSrv, grpcSrv := newHTTPServer(a), newGRPCServer(a)
	go func() { _ = httpSrv.Serve(hl) }()
	go func() { _ = grpcSrv.Serve(gl) }()

	bg, stopBG := context.WithCancel(context.Background())
	fatal := make(chan error, 1)
	go a.exporter.Run(bg)
	go a.bundles.Run(bg)
	go jwks.Run(bg)
	go db.secret.Watch(bg)
	go RunDBProbe(bg, db, a.ready, log, func(err error) { fatal <- err })
	log.Info("serving", F{"http": 8080, "grpc": 9090})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	code := 0
	select {
	case s := <-sig:
		log.Info("shutdown_started", F{"signal": s.String()})
	case err := <-fatal:
		log.Error("fatal", F{"error": err.Error()})
		code = 1
	}
	shutdown(a, httpSrv, grpcSrv)
	stopBG()
	a.exporter.Wait(3 * time.Second)
	db.pool.Close()
	log.Info("shutdown_done", F{"exit_code": code})
	return code
}

// shutdown is P1.6: stop accepting, let in-flight requests finish within SHUTDOWN_GRACE.
func shutdown(a *App, httpSrv *http.Server, grpcSrv *grpc.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownGrace)
	defer cancel()
	done := make(chan struct{})
	go func() { grpcSrv.GracefulStop(); close(done) }()
	if err := httpSrv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		a.log.Warn("shutdown_grace_exceeded", F{"error": err.Error()})
		_ = httpSrv.Close()
	}
	select {
	case <-done:
	case <-ctx.Done():
		grpcSrv.Stop()
	}
}

// newUUIDv7 is RFC 9562 version 7 with the millisecond timestamp of t.
func newUUIDv7(t time.Time) string {
	var b [16]byte
	_, _ = rand.Read(b[6:])
	ms := uint64(t.UnixMilli())
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms)
	copy(b[0:6], ts[2:8])
	b[6] = 0x70 | (b[6] & 0x0f)
	b[8] = 0x80 | (b[8] & 0x3f)
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

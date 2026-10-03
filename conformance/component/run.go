package compconf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"github.com/brickKit/be-acceptance/conformance/component/infra"
	"github.com/brickKit/be-acceptance/conformance/component/protoschema"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Options configure one run.
type Options struct {
	Dir          string            // component root: component.yaml, assembly.yaml, contracts/, conformance/
	Image        string            // the image under test
	OutDir       string            // where compconf-report.json and .md go
	Profiles     []string          // restrict to these profiles; nil = every implemented one selected
	Prefix       string            // container name prefix; default sdkb-acc-<random>
	DepContracts map[string]string // dependency ID -> directory with its .proto files
	FakeHost     string            // how containers reach the fakes; default host.docker.internal
	Keep         bool              // keep containers and the run directory for debugging
	Progress     io.Writer
}

// Run is one execution of the suite against one component.
type Run struct {
	opt      Options
	comp     *Component
	cat      *Catalog
	cats     catalogs
	ev       *evidence
	selected []string
	ran      []string
	runDir   string

	env      *infra.Env
	pg       *infra.Postgres
	nats     *infra.NATS
	natsConn *nats.Conn
	db       dbIdentity

	iam      *fakes.IAM
	iamSrv   *fakes.Server
	authz    *fakes.Authz
	authzSrv *fakes.Server
	obs      *fakes.Observer
	obsSrv   *fakes.Server
	peers    map[string]*fakes.Peer

	compEnv  []string
	secrets  map[string]string
	main     *infra.Container
	mainUp   bool
	base     string // http://127.0.0.1:<published main port>
	grpcAddr string
	logs     *fakes.Logs
	rec      *recorder
	seq      atomic.Int64

	personas   map[string]persona
	tokens     []string
	methods    map[string]protoreflect.MethodDescriptor
	grpcSeen   [][2]string // (domain, reason) of gRPC errors seen
	stopLogs   context.CancelFunc
	notRunNote string
	lastInfo   map[string]any // the last /_be/info body
	ready      bool
	slowID     string // the record a slow path with {id} uses
	migrated   bool
	replica    *instance // the second serving instance of the jobs profile
	scope      *scopeWorld
	bus        busRecorder
	oldSecrets []string // secret values replaced during the run (CP-DB-06), still never logged
}

// Execute runs the suite and writes the report files.
func Execute(ctx context.Context, o Options) (*Report, error) {
	if o.FakeHost == "" {
		o.FakeHost = "host.docker.internal"
	}
	if o.Prefix == "" {
		o.Prefix = "sdkb-acc-" + infra.RandHex(3)
	}
	if o.Progress == nil {
		o.Progress = io.Discard
	}
	comp, err := LoadComponent(os.DirFS(o.Dir))
	if err != nil {
		return nil, err
	}
	cat, err := LoadCatalog()
	if err != nil {
		return nil, err
	}
	if comp.Assembly.Protocol != cat.Protocol {
		return nil, fmt.Errorf("the component declares protocol %q; this suite implements %q (P20.2)", comp.Assembly.Protocol, cat.Protocol)
	}
	r := &Run{opt: o, comp: comp, cat: cat, ev: newEvidence(cat), logs: fakes.NewLogs(), rec: &recorder{},
		peers: map[string]*fakes.Peer{}}
	if r.cats, err = loadCatalogs(comp); err != nil {
		return nil, err
	}
	r.selected = SelectProfiles(comp)
	r.ran, r.notRunNote = chooseProfiles(r.selected, o.Profiles)
	digest, err := infra.ImageDigest(ctx, o.Image)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	r.logf("compconf %s %s: profiles %v, running %v", comp.ID(), comp.Version(), r.selected, r.ran)
	defer r.teardown()
	if err := r.setup(ctx); err != nil {
		return nil, fmt.Errorf("setup: %w", err)
	}
	for _, s := range scenario() {
		if s.Case != "" && !contains(r.ran, r.profileOf(s.Case)) {
			continue
		}
		r.runStep(ctx, s)
	}
	rep := buildReport(cat, r.ev, r.selected, r.ran, comp.Assembly.Conformance.Skip, reportMeta{
		Component: comp.ID(), Version: comp.Version(), ImageRef: o.Image, ImageDigest: digest,
		SDK: r.sdkOfInfo(ctx), Env: r.infraVersions(ctx), Started: started, NotRunReason: r.notRunNote})
	return &rep, r.writeReport(rep)
}

// chooseProfiles: the implemented profiles among the selected ones, further restricted by
// the caller's list.
func chooseProfiles(selected, only []string) ([]string, string) {
	var ran []string
	for _, p := range selected {
		if contains(ImplementedProfiles, p) && (only == nil || contains(only, p)) {
			ran = append(ran, p)
		}
	}
	note := "not implemented in be-acceptance@" + SuiteVersion + " (implemented: " + strings.Join(ImplementedProfiles, ", ") + ")"
	if only != nil {
		note += " or excluded by --profiles"
	}
	return ran, note
}

func (r *Run) profileOf(id string) string {
	c, _ := r.cat.Case(id)
	return c.Profile
}

func (r *Run) logf(f string, a ...any) { fmt.Fprintf(r.opt.Progress, "compconf: "+f+"\n", a...) }

func (r *Run) runStep(ctx context.Context, s step) {
	r.logf("step %s %s", s.Case, s.Name)
	start := time.Now()
	func() {
		defer func() {
			if p := recover(); p != nil {
				id := s.Case
				if id == "" {
					id = "CP-CORE-03"
				}
				r.ev.fail(id, "suite step %q panicked: %v", s.Name, p)
				r.logf("panic in %s: %v\n%s", s.Name, p, debug.Stack())
			}
		}()
		sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		s.Fn(sctx, r)
	}()
	if s.Case != "" {
		r.ev.addTime(s.Case, time.Since(start))
	}
}

func (r *Run) writeReport(rep Report) error {
	s, err := protoschema.ProtocolSchema("compconf-report.schema.json")
	if err != nil {
		return err
	}
	if err := protoschema.ValidateValue(s, rep); err != nil {
		return fmt.Errorf("the report does not match compconf-report.schema.json: %w", err)
	}
	out := r.opt.OutDir
	if out == "" {
		out = "."
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "compconf-report.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "compconf-report.md"), []byte(rep.Markdown(r.cat)), 0o644)
}

func (r *Run) infraVersions(ctx context.Context) map[string]string {
	m := map[string]string{}
	if r.pg != nil {
		if v, err := r.superQuery(ctx, "postgres", "SHOW server_version"); err == nil {
			m["postgres"] = v
		}
	}
	if r.natsConn != nil {
		m["nats"] = r.natsConn.ConnectedServerVersion()
	}
	return m
}

func (r *Run) sdkOfInfo(ctx context.Context) json.RawMessage {
	info := r.lastInfo
	if info == nil {
		return nil
	}
	b, _ := json.Marshal(info["sdk"])
	return b
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

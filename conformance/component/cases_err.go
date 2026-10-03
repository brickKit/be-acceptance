package compconf

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

// CP-ERR-01: every 4xx/5xx the component answered during the run is problem+json, and its
// reason is catalogued (P4.1, P4.5, P4.8). Evaluated at the end over every exchange.
func caseErr01(_ context.Context, r *Run) {
	const id = "CP-ERR-01"
	seen, n := 0, 0
	for _, x := range r.rec.list() {
		if x.Err != nil || x.Status < 400 || x.Method == "HEAD" {
			continue
		}
		seen++
		for _, is := range problemIssues(x, r.comp.ID(), r.cats) {
			r.failCapped(id, &n, "%s", is)
		}
	}
	r.ev.check(id, seen > 0, "the run saw no 4xx or 5xx answer")
	r.ev.pass(id, fmt.Sprintf("%d error answers checked", seen))
}

// CP-ERR-04: every reason seen during the run, REST and gRPC, is catalogued (P4.4, P4.7).
func caseErr04(_ context.Context, r *Run) {
	const id = "CP-ERR-04"
	pairs := map[[2]string]bool{}
	for _, x := range r.rec.list() {
		if x.Problem != nil && x.reason() != "" {
			pairs[[2]string{x.str("domain"), x.reason()}] = true
		}
	}
	for _, p := range r.grpcSeen {
		pairs[p] = true
	}
	for p := range pairs {
		for _, is := range reasonIssues(p[0], p[1], r.comp.ID(), r.cats) {
			r.ev.fail(id, "%s", is)
		}
	}
	r.ev.pass(id, fmt.Sprintf("%d distinct reasons", len(pairs)))
}

// CP-ERR-03: with the runtime role's table grants revoked, operations answer 500 INTERNAL with
// only generic text and the trace ID; no SQL text (P4.3).
func caseErr03(ctx context.Context, r *Run) {
	const id = "CP-ERR-03"
	if r.pg == nil {
		r.ev.notApplicable(id, "the component has no database (no PG_SCHEMA)")
		return
	}
	if !r.needMain(id) {
		return
	}
	q := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	revoke := fmt.Sprintf("REVOKE SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s FROM %s", q(r.db.Schema), q(r.db.Runtime))
	grant := fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO %s", q(r.db.Schema), q(r.db.Runtime))
	if !r.ev.check(id, r.superExec(ctx, r.db.Database, revoke) == nil, "revoking the runtime role's grants failed") {
		return
	}
	defer func() { _ = r.superExec(context.Background(), r.db.Database, grant) }()
	idents := append([]string{r.db.Schema, r.db.Runtime, r.db.Owner}, r.tableNames(ctx)...)
	got500 := 0
	for _, res := range sortedKeys(r.comp.Fixtures.Resources) {
		for _, name := range sortedKeys(r.comp.Fixtures.Resources[res]) {
			op := r.comp.Fixtures.Resources[res][name]
			if op.Path == "" {
				continue
			}
			persona := op.As
			if persona == "" {
				persona = pAll
			}
			opts := []reqOpt{withToken(r.token(persona))}
			if b := r.fixtureBody(op); b != nil {
				opts = append(opts, withBody(b))
			}
			x := r.call(ctx, op.Method, r.fixtureTarget(op, "", map[string]string{"ms": "1"}), opts...)
			if x.Status != 500 {
				continue
			}
			got500++
			r.ev.check(id, x.reason() == "INTERNAL" && x.str("domain") == "be" && x.str("code") == "INTERNAL",
				"%s %s: 500 with reason %q domain %q code %q, want INTERNAL / be / INTERNAL", op.Method, op.Path, x.reason(), x.str("domain"), x.str("code"))
			r.ev.check(id, hex32.MatchString(x.str("trace_id")), "%s %s: 500 without trace_id", op.Method, op.Path)
			for _, is := range leakIssues(x.Problem, idents) {
				r.ev.fail(id, "%s %s: %s", op.Method, op.Path, is)
			}
		}
	}
	r.ev.check(id, got500 > 0, "no fixtures operation answered 500 while the runtime role had no table grants: none reached the database, or the error was not mapped to INTERNAL")
}

func (r *Run) tableNames(ctx context.Context) []string {
	conn, err := pgx.Connect(ctx, r.pg.HostDSN(r.db.Database))
	if err != nil {
		return nil
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname = $1", r.db.Schema)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		_ = rows.Scan(&t)
		out = append(out, t)
	}
	return out
}

// CP-ERR-02: gRPC errors carry ErrorInfo; the same failure over gRPC and REST maps both ways
// (P4.2).
func caseErr02(ctx context.Context, r *Run) {
	const id = "CP-ERR-02"
	if r.comp.GRPCPort() == 0 {
		r.ev.notApplicable(id, "no grpc port")
		return
	}
	if !r.needMain(id) {
		return
	}
	cc, err := grpc.NewClient(r.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if !r.ev.check(id, err == nil, "dialling the grpc port: %v", err) {
		return
	}
	defer cc.Close()
	n := 0
	for _, res := range sortedKeys(r.comp.Fixtures.Resources) {
		for _, name := range sortedKeys(r.comp.Fixtures.Resources[res]) {
			op := r.comp.Fixtures.Resources[res][name]
			if op.GRPC == "" || op.UserFacing {
				continue
			}
			n++
			rid := uuidv7()
			st := r.grpcCall(ctx, cc, op, rid, false)
			ei := errorInfo(st)
			r.ev.check(id, st.Code() == codes.Unauthenticated && ei != nil && ei.Reason == "MISSING_CALLER" && ei.Domain == "be" && st.Message() != "",
				"%s without be-caller = %s %v %q, want UNAUTHENTICATED with ErrorInfo MISSING_CALLER/be and a message (P4.2, P7.2)", op.GRPC, st.Code(), ei, st.Message())
			st = r.grpcCall(ctx, cc, op, rid, true)
			if st.Code() != codes.OK {
				ei = errorInfo(st)
				r.ev.check(id, ei != nil, "%s answered %s without ErrorInfo (P4.2)", op.GRPC, st.Code())
				r.compareWithREST(ctx, id, res, rid, op, st, ei)
			}
		}
	}
	if n == 0 {
		r.ev.notApplicable(id, "fixtures name no gRPC operation")
	}
}

// grpcCall invokes a fixtures gRPC operation with {id} = rid, with or without be-caller.
func (r *Run) grpcCall(ctx context.Context, cc *grpc.ClientConn, op FixtureOp, rid string, caller bool) *status.Status {
	md, ok := r.methods[op.GRPC]
	if !ok {
		return status.New(codes.Unimplemented, "compconf: method not in the component's protos")
	}
	in := dynamicpb.NewMessage(md.Input())
	if op.Body != nil {
		_ = protojson.Unmarshal([]byte(r.substitute(mustJSON(op.Body), rid)), in)
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if caller {
		cctx = metadata.AppendToOutgoingContext(cctx, "be-caller", "compconf/suite", "x-request-id", "compconf-grpc-"+rid[:8])
	}
	err := cc.Invoke(cctx, "/"+op.GRPC, in, dynamicpb.NewMessage(md.Output()))
	st := status.Convert(err)
	if ei := errorInfo(st); ei != nil {
		r.grpcSeen = append(r.grpcSeen, [2]string{ei.Domain, ei.Reason})
	}
	return st
}

func errorInfo(st *status.Status) *errdetails.ErrorInfo {
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			return ei
		}
	}
	return nil
}

// compareWithREST: the resource's REST get for the same ID gives the same reason, domain and
// the mapped status.
func (r *Run) compareWithREST(ctx context.Context, id, res, rid string, op FixtureOp, st *status.Status, ei *errdetails.ErrorInfo) {
	get, ok := r.comp.Fixtures.Resources[res]["get"]
	if !ok || get.Path == "" || ei == nil || !strings.Contains(mustJSON(op.Body), "{id}") {
		return
	}
	persona := pAll
	if get.Key != "" {
		persona = r.personaWith(get.Key)
	}
	x := r.call(ctx, "GET", r.fixtureTarget(get, rid, nil), withToken(r.token(persona)))
	code := strings.ToUpper(strings.ReplaceAll(codeName(st.Code()), " ", "_"))
	r.ev.check(id, x.Status == httpOfCode[code] && x.reason() == ei.Reason && x.str("domain") == ei.Domain,
		"%s for a missing %s = %s %s/%s, REST GET = %d %s/%s: they must map both ways (P4.2)",
		op.GRPC, res, code, ei.Domain, ei.Reason, x.Status, x.str("domain"), x.reason())
}

func codeName(c codes.Code) string {
	names := map[codes.Code]string{codes.InvalidArgument: "INVALID_ARGUMENT", codes.FailedPrecondition: "FAILED_PRECONDITION",
		codes.OutOfRange: "OUT_OF_RANGE", codes.Unauthenticated: "UNAUTHENTICATED", codes.PermissionDenied: "PERMISSION_DENIED",
		codes.NotFound: "NOT_FOUND", codes.AlreadyExists: "ALREADY_EXISTS", codes.Aborted: "ABORTED",
		codes.ResourceExhausted: "RESOURCE_EXHAUSTED", codes.Canceled: "CANCELLED", codes.Unimplemented: "UNIMPLEMENTED",
		codes.Unavailable: "UNAVAILABLE", codes.DeadlineExceeded: "DEADLINE_EXCEEDED", codes.Internal: "INTERNAL",
		codes.Unknown: "UNKNOWN", codes.DataLoss: "DATA_LOSS"}
	return names[c]
}

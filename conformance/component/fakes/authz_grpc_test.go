package fakes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	authzv2 "github.com/brickKit/contract-infra-authz/v2/gen/go/infra/authz/v2"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func dialAuthz(t *testing.T, f *Authz) authzv2.AuthzProviderClient {
	t.Helper()
	if err := f.StartGRPC("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.StopGRPC)
	cc, err := grpc.NewClient(f.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return authzv2.NewAuthzProviderClient(cc)
}

func asCaller(id string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "be-caller", id)
}

func reasonOf(err error) (codes.Code, string, map[string]string) {
	st := status.Convert(err)
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			return st.Code(), ei.Reason, ei.Metadata
		}
	}
	return st.Code(), "", nil
}

func TestAuthzGRPCRefusesACallWithoutBeCaller(t *testing.T) {
	c := dialAuthz(t, NewAuthz())
	_, err := c.GetBundle(context.Background(), &authzv2.GetBundleRequest{})
	if code, reason, _ := reasonOf(err); code != codes.Unauthenticated || reason != "MISSING_CALLER" {
		t.Fatalf("no be-caller: %v %s", code, reason)
	}
}

func TestAuthzGRPCBundleEqualsTheRESTBundle(t *testing.T) {
	f := NewAuthz()
	f.SetRole("editor", []string{"a.b.view"}, RoleGrant{DefaultLevel: "all"})
	c := dialAuthz(t, f)
	resp, err := c.GetBundle(asCaller("conformance/x"), &authzv2.GetBundleRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateContract("authz", "bundle.schema.json", resp.Json); err != nil {
		t.Fatalf("bundle: %v", err)
	}
	again, err := c.GetBundle(asCaller("conformance/x"), &authzv2.GetBundleRequest{IfNoneMatch: resp.Etag})
	if err != nil || !again.NotModified || len(again.Json) != 0 {
		t.Fatalf("If-None-Match: %v %+v", err, again)
	}
	if got := f.GRPCCalls("GetBundle"); len(got) != 2 || got[0].Caller != "conformance/x" {
		t.Fatalf("recorded calls %+v", got)
	}
}

func TestAuthzGRPCOptionalCapabilityAnswersCapabilityUnavailable(t *testing.T) {
	c := dialAuthz(t, NewAuthz())
	_, err := c.Check(asCaller("conformance/x"), &authzv2.CheckRequest{Key: "a.b.view"})
	code, reason, md := reasonOf(err)
	if code != codes.Unimplemented || reason != "CAPABILITY_UNAVAILABLE" || md["capability"] != "check" {
		t.Fatalf("Check without capability: %v %s %v", code, reason, md)
	}
	_, err = c.WriteTuples(asCaller("conformance/x"), &authzv2.WriteTuplesRequest{})
	if _, reason, md := reasonOf(err); reason != "CAPABILITY_UNAVAILABLE" || md["capability"] != "sharing" {
		t.Fatalf("WriteTuples without sharing: %s %v", reason, md)
	}
}

func TestAuthzSharingWritesTuplesIntoTheChangefeed(t *testing.T) {
	f := NewAuthz()
	f.SetCapability("sharing", true)
	c := dialAuthz(t, f)
	tuple := &authzv2.Tuple{Object: &authzv2.ObjectRef{Type: "a.b.thing", Id: "t1"}, Relation: "viewer", Subject: "user:u2"}
	w, err := c.WriteTuples(asCaller("a/b"), &authzv2.WriteTuplesRequest{Writes: []*authzv2.Tuple{tuple}, IdempotencyKey: "k1", Source: "a/b"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.WriteTuples(asCaller("a/b"), &authzv2.WriteTuplesRequest{Writes: []*authzv2.Tuple{tuple}, IdempotencyKey: "k1", Source: "a/b"})
	if err != nil || again.Revision != w.Revision {
		t.Fatalf("replayed key: %v %v, want revision %s", err, again, w.Revision)
	}
	ts := httptest.NewServer(f.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/authz/v2/changes?types=a.b.thing&after=0")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := validateContract("authz", "changefeed.schema.json", b); err != nil {
		t.Fatalf("changes: %v\n%s", err, b)
	}
	var page struct {
		Changes []struct {
			Revision, Op string
			Tuple        map[string]any
		}
		Next, Watermark string
	}
	_ = json.Unmarshal(b, &page)
	if len(page.Changes) != 1 || page.Changes[0].Op != "upsert" || page.Changes[0].Revision != w.Revision || page.Watermark != w.Revision {
		t.Fatalf("page %s", b)
	}
	tuples, err := c.ReadTuples(asCaller("a/b"), &authzv2.ReadTuplesRequest{Type: "a.b.thing"})
	if err != nil || len(tuples.Tuples) != 1 || tuples.Revision != w.Revision {
		t.Fatalf("ReadTuples %v %v", err, tuples)
	}
	if _, err := c.WriteTuples(asCaller("a/b"), &authzv2.WriteTuplesRequest{Deletes: []*authzv2.Tuple{tuple}, IdempotencyKey: "k2", Source: "a/b"}); err != nil {
		t.Fatal(err)
	}
	if got := f.Tuples("a.b.thing"); len(got) != 0 {
		t.Fatalf("after delete %v", got)
	}
}

func TestAuthzReadChangesOtherTypesAdvanceOnlyTheWatermark(t *testing.T) {
	f := NewAuthz()
	f.SetCapability("sharing", true)
	f.PutTuple(TupleRec{Type: "a.b.thing", ID: "t1", Relation: "viewer", Subject: "role:r1"})
	c := dialAuthz(t, f)
	page, err := c.ReadChanges(asCaller("x/y"), &authzv2.ReadChangesRequest{Types: []string{"x.y.other"}, After: "0"})
	if err != nil || len(page.Changes) != 0 || page.Watermark != page.Next || page.Watermark == "0" {
		t.Fatalf("other type: %v %+v", err, page)
	}
}

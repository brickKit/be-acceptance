package fakes

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"

	beprotocol "github.com/brickKit/be-protocol"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

const reserve = "conformance.peer.v1.PeerService/Reserve"

func newPeerForTest(t *testing.T) (*Peer, *grpc.ClientConn) {
	t.Helper()
	contracts, _ := fs.Sub(beprotocol.FS, "fixtures/peer/contracts")
	protoRoot, _ := fs.Sub(beprotocol.FS, "proto")
	p, err := NewPeer("conformance/peer", contracts, protoRoot)
	if err != nil {
		t.Fatal(err)
	}
	p.SetAnswer(reserve, PeerAnswer{Response: []byte(`{"reservation_id":"rsv-0001","status":"RESERVATION_STATUS_RESERVED"}`)})
	p.SetHTTPAnswer("GET /conformance/peer/owners/{owner_id}", PeerAnswer{Response: []byte(`{"owner_id":"o1","display_name":"A"}`)})
	if err := p.Start("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	cc, err := grpc.NewClient(p.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return p, cc
}

func invoke(t *testing.T, p *Peer, cc *grpc.ClientConn, method string, timeout time.Duration) (*dynamicpb.Message, error) {
	md, ok := p.Method(method)
	if !ok {
		t.Fatalf("method %s unknown", method)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "be-caller", "conformance/widget")
	out := dynamicpb.NewMessage(md.Output())
	err := cc.Invoke(ctx, "/"+method, dynamicpb.NewMessage(md.Input()), out)
	return out, err
}

func TestPeerAnswersCannedResponseAndRecordsCall(t *testing.T) {
	p, cc := newPeerForTest(t)
	out, err := invoke(t, p, cc, reserve, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	js, _ := protojson.Marshal(out)
	if !strings.Contains(string(js), "rsv-0001") {
		t.Fatalf("response = %s", js)
	}
	calls := p.Calls(reserve)
	if len(calls) != 1 || calls[0].Metadata.Get("be-caller")[0] != "conformance/widget" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Deadline <= 0 || calls[0].Deadline > 2*time.Second {
		t.Fatalf("deadline = %v", calls[0].Deadline)
	}
	if p.Connections() != 1 {
		t.Fatalf("connections = %d", p.Connections())
	}
}

func TestPeerFailsWithErrorInfo(t *testing.T) {
	p, cc := newPeerForTest(t)
	p.SetAnswer(reserve, PeerAnswer{Code: codes.FailedPrecondition, Reason: "QUOTA_EXCEEDED", Domain: "conformance/peer"})
	_, err := invoke(t, p, cc, reserve, time.Second)
	st := status.Convert(err)
	if st.Code() != codes.FailedPrecondition || len(st.Details()) != 1 {
		t.Fatalf("status = %v %v", st.Code(), st.Details())
	}
	if ei := st.Details()[0].(*errdetails.ErrorInfo); ei.Reason != "QUOTA_EXCEEDED" || ei.Domain != "conformance/peer" {
		t.Fatalf("ErrorInfo = %v", ei)
	}
}

func TestPeerHangsUntilDeadline(t *testing.T) {
	p, cc := newPeerForTest(t)
	p.SetAnswer(reserve, PeerAnswer{Hang: true})
	start := time.Now()
	_, err := invoke(t, p, cc, reserve, 300*time.Millisecond)
	if status.Code(err) != codes.DeadlineExceeded || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("hang: %v after %v", err, time.Since(start))
	}
}

func TestPeerServesHTTPAndRecordsHeaders(t *testing.T) {
	p, _ := newPeerForTest(t)
	req, _ := http.NewRequest("GET", p.HTTPURL()+"/conformance/peer/owners/o1", nil)
	req.Header.Set("Authorization", "Bearer x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "display_name") {
		t.Fatalf("http = %d %s", resp.StatusCode, b)
	}
	calls := p.HTTPCalls()
	if len(calls) != 1 || calls[0].Header.Get("Authorization") != "Bearer x" || calls[0].Route != "GET /conformance/peer/owners/{owner_id}" {
		t.Fatalf("http calls = %+v", calls)
	}
	r2, _ := http.Get(p.HTTPURL() + "/conformance/peer/nothing")
	r2.Body.Close()
	if r2.StatusCode != 404 {
		t.Fatalf("unknown path = %d", r2.StatusCode)
	}
}

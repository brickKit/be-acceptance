package compconf

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

// The widget fixture's canned peer answers load and fit the peer's proto messages.
func TestWidgetPeerAnswersFitTheProtos(t *testing.T) {
	r := &Run{comp: widget(t)}
	r.personas = map[string]persona{"alice": {Sub: "0192aaaa-0000-7000-8000-00000000a11c"}}
	p, err := r.newPeer("conformance/peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	cc, err := grpc.NewClient(p.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	for call := range r.comp.Fixtures.Dependencies["conformance/peer"] {
		if strings.Contains(call, " ") {
			continue
		}
		md, ok := p.Method(call)
		if !ok {
			t.Errorf("%s is not in the peer's protos", call)
			continue
		}
		out := dynamicpb.NewMessage(md.Output())
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := cc.Invoke(ctx, "/"+call, dynamicpb.NewMessage(md.Input()), out)
		cancel()
		if err != nil {
			t.Errorf("%s: %v", call, err)
			continue
		}
		if js, _ := protojson.Marshal(out); string(js) == "{}" {
			t.Errorf("%s answered an empty message", call)
		}
	}
	calls := p.Calls("")
	if len(calls) == 0 {
		t.Fatal("no call recorded")
	}
}

package compconf

import (
	"io/fs"
	"testing"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	beprotocol "github.com/brickKit/be-protocol"
)

func TestMaxItemsReadsTheLimitsOption(t *testing.T) {
	contracts, _ := fs.Sub(beprotocol.FS, "fixtures/widget/contracts")
	root, _ := fs.Sub(beprotocol.FS, "proto")
	ms, err := fakes.CompileProtos(contracts, fakes.ProtoImports(root)...)
	if err != nil {
		t.Fatal(err)
	}
	md := ms["conformance.widget.v1.WidgetService/BatchGetWidgets"]
	if got := maxItems(md.Input().Fields().ByName("ids")); got != 100 {
		t.Fatalf("max_items = %d, want 100", got)
	}
	peer, _ := fs.Sub(beprotocol.FS, "fixtures/peer/contracts")
	pm, err := fakes.CompileProtos(peer, fakes.ProtoImports(root)...)
	if err != nil {
		t.Fatal(err)
	}
	if got := maxItems(pm["conformance.peer.v1.PeerService/BatchGetOwners"].Input().Fields().ByName("ids")); got != 500 {
		t.Fatalf("peer max_items = %d", got)
	}
	if got := maxItems(ms["conformance.widget.v1.WidgetService/TouchWidget"].Input().Fields().ByName("id")); got != 500 {
		t.Fatalf("default = %d", got)
	}
}

package compconf

import (
	"io/fs"
	"os"
	"reflect"
	"testing"

	beprotocol "github.com/brickKit/be-protocol"
)

func widget(t *testing.T) *Component {
	t.Helper()
	sub, err := fs.Sub(beprotocol.FS, "fixtures/widget")
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadComponent(sub)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func rawstub(t *testing.T) *Component {
	t.Helper()
	c, err := LoadComponent(os.DirFS("testdata/rawstub"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoadComponentWidget(t *testing.T) {
	c := widget(t)
	if c.ID() != "conformance/widget" || c.Version() != "1.0.0" {
		t.Fatalf("id/version = %s %s", c.ID(), c.Version())
	}
	if c.Manifest.Deployment.Port != 8080 || c.GRPCPort() != 9090 {
		t.Fatalf("ports = %d %d", c.Manifest.Deployment.Port, c.GRPCPort())
	}
	if !c.Manifest.ConfigSchema.Properties["PG_PASSWORD_FILE"].Secret {
		t.Fatal("PG_PASSWORD_FILE not secret")
	}
	deps := c.Dependencies()
	if len(deps) != 2 || deps[0].ID != "conformance/peer" || deps[0].Optional || !deps[1].Optional {
		t.Fatalf("deps = %+v", deps)
	}
	if len(c.Fixtures.Users) != 7 || c.Fixtures.PIILog == nil || c.Fixtures.Slow == nil {
		t.Fatalf("fixtures not loaded: %+v", c.Fixtures.Users)
	}
	op, ok := c.OperationByID("approveWidget")
	if !ok || op.Guard != "conformance.widget.approve" || op.DeadlineSeconds != 15 ||
		op.Method != "POST" || op.Path != "/conformance/widget/widgets/{id}/approve" {
		t.Fatalf("approve op = %+v", op)
	}
	if c.Errors.Domain != "conformance/widget" || len(c.Errors.Reasons) == 0 {
		t.Fatalf("errors = %+v", c.Errors)
	}
}

func TestLoadComponentValidatesFixtures(t *testing.T) {
	c := rawstub(t)
	if c.Assembly.Language != "go" || c.Assembly.Protocol != "1.0" {
		t.Fatalf("assembly = %+v", c.Assembly)
	}
	if got := c.Fixtures.Resources["notes"]["create"].Path; got != "/conformance/rawstub/notes" {
		t.Fatalf("create path = %q", got)
	}
	body, err := c.FixtureFile("samples/note-create.json")
	if err != nil || len(body) == 0 {
		t.Fatalf("sample: %v", err)
	}
}

func TestSelectProfilesWidget(t *testing.T) {
	got := SelectProfiles(widget(t))
	want := []string{"core", "obs", "err", "auth", "scope", "grpc", "outbound", "events-pub", "events-sub",
		"idempotency", "db", "jobs", "lifecycle", "blob"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles = %v\nwant       %v", got, want)
	}
}

func TestSelectProfilesRawstub(t *testing.T) {
	got := SelectProfiles(rawstub(t))
	want := []string{"core", "obs", "err", "auth", "grpc", "idempotency", "db", "jobs", "lifecycle"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles = %v, want %v", got, want)
	}
}

func TestSelectProfilesPublicOnlyHasNoAuth(t *testing.T) {
	c := rawstub(t)
	for i := range c.Operations {
		c.Operations[i].Guard = "public"
	}
	for _, p := range SelectProfiles(c) {
		if p == "auth" {
			t.Fatal("auth selected for a component with only public routes")
		}
	}
}

// rc.2 P3.16 and P2.8 trigger keys: a missing guard counts as protected unless x-be-internal;
// db by PG_SCHEMA or PG_HOST, blob by S3_BUCKET or S3_URL.
func TestSelectProfilesFailClosedAndTriggerKeys(t *testing.T) {
	c := rawstub(t)
	for i := range c.Operations {
		c.Operations[i].Guard = "public"
	}
	c.Operations[0].Guard = ""
	if !contains(SelectProfiles(c), "auth") {
		t.Fatal("an operation without x-be-permission must select auth")
	}
	c.Operations[0].Internal = true
	if contains(SelectProfiles(c), "auth") {
		t.Fatal("an x-be-internal operation needs no guard")
	}
	delete(c.Manifest.ConfigSchema.Properties, "PG_SCHEMA")
	c.Manifest.ConfigSchema.Properties["S3_URL"] = ConfigProp{Type: "string"}
	got := SelectProfiles(c)
	if !contains(got, "db") || !contains(got, "blob") {
		t.Fatalf("trigger keys: %v", got)
	}
}

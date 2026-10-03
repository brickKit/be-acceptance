//go:build compconf_docker

package compconf

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The suite's self-test (be-protocol "red before green"): the protocol stub written from the
// text alone passes every MUST case of the implemented profiles, and each deliberately broken
// variant fails exactly the cases broken-variants.yaml names (also_fails may fail too).

const stubDir = "testdata/rawstub"

type brokenVariant struct {
	Name      string   `yaml:"name"`
	MustFail  []string `yaml:"must_fail"`
	AlsoFails []string `yaml:"also_fails"`
}

func buildStub(t *testing.T, variant string) string {
	t.Helper()
	tag := "compconf-rawstub:1.0.0"
	if variant != "" {
		tag += "-broken-" + variant
	}
	cmd := exec.Command("docker", "build", "-q", "--build-arg", "BROKEN="+variant, "-t", tag, ".")
	cmd.Dir = stubDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker build %s: %v\n%s", tag, err, out)
	}
	return tag
}

func runStub(t *testing.T, image string) *Report {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	rep, err := Execute(ctx, Options{Dir: stubDir, Image: image, OutDir: t.TempDir(), Profiles: ImplementedProfiles,
		Progress: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func caseStatuses(rep *Report) map[string]reportCase {
	out := map[string]reportCase{}
	for _, p := range ImplementedProfiles {
		for _, c := range rep.Profiles[p].Cases {
			out[c.ID] = c
		}
	}
	return out
}

func TestSelftestRawstubPasses(t *testing.T) {
	rep := runStub(t, buildStub(t, ""))
	for id, c := range caseStatuses(rep) {
		if c.Status == "fail" {
			t.Errorf("%s failed: %s", id, c.Message)
		}
	}
}

func TestSelftestBrokenVariantsFailTheirCases(t *testing.T) {
	b, err := os.ReadFile(stubDir + "/broken-variants.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Variants []brokenVariant `yaml:"variants"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, v := range doc.Variants {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			st := caseStatuses(runStub(t, buildStub(t, v.Name)))
			for _, id := range v.MustFail {
				if st[id].Status != "fail" {
					t.Errorf("%s: %s = %s, want fail: %s", v.Name, id, st[id].Status, st[id].Message)
				}
			}
			for id, c := range st {
				if c.Status == "fail" && !contains(v.MustFail, id) && !contains(v.AlsoFails, id) {
					t.Errorf("%s: %s failed but the variant breaks only %v: %s", v.Name, id, v.MustFail, c.Message)
				}
			}
		})
	}
}

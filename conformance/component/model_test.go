package compconf

import "testing"

func TestWidgetTypedFixturesAndResources(t *testing.T) {
	c := widget(t)
	res := c.Assembly.Resources
	if len(res) != 1 || res[0].Type != "conformance.widget.widget" || res[0].Table != "widgets" ||
		res[0].ViewKey != "conformance.widget.view" || len(res[0].Fields) != 1 || res[0].Share == nil {
		t.Fatalf("resources = %+v", res)
	}
	scopes := c.DataScopes()
	if len(scopes) != 3 || scopes[0].Dimension != "owner" || scopes[0].Column != "owner_id" || scopes[2].Mode != "in" {
		t.Fatalf("data scopes = %+v", scopes)
	}
	ev := c.Fixtures.Events
	if len(ev.Produces) != 2 || ev.Produces[1].Subject != "conformance.widget.approved.v1" || len(ev.Consumes) != 2 ||
		ev.Consumes[1].Produces != "conformance.widget.reverted.v1" || ev.Consumes[0].Observe.SQL == "" {
		t.Fatalf("events = %+v", ev)
	}
	j := c.Fixtures.Jobs
	if len(j.Cron) != 1 || j.Cron[0].Name != "widget.daily" || j.Cron[0].Override["cron"] != "@every 2s" ||
		len(j.Enqueues) != 1 || len(j.Reconcilers) != 1 || j.Reconcilers[0].Resolve.ResponseFile == "" {
		t.Fatalf("jobs = %+v", j)
	}
	list := c.Fixtures.Resources["conformance.widget.widget"]["list"]
	if list.Filters["region"].Dimension != "region" || list.Sort == nil || list.Sort.Param != "sort" || list.Range == nil {
		t.Fatalf("list op = %+v", list)
	}
	appr := c.Fixtures.Resources["conformance.widget.widget"]["approve"]
	if appr.Triggers == nil || appr.Triggers.GRPC != "conformance.peer.v1.PeerService/Reserve" || appr.Fingerprint == nil {
		t.Fatalf("approve op = %+v", appr)
	}
}

func TestSingleProduceIsAccepted(t *testing.T) {
	var e FixtureEvents
	if err := yamlUnmarshal([]byte("produces: {via: resources.a.create, subject: a.b.c.v1}"), &e); err != nil || len(e.Produces) != 1 {
		t.Fatalf("%v %+v", err, e)
	}
}

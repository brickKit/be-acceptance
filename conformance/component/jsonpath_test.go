package compconf

import "testing"

func TestJSONPath(t *testing.T) {
	doc := []byte(`{"id":"w1","items":[{"id":"a","n":2},{"id":"b"}],"o":{"p":{"q":true}}}`)
	cases := map[string]string{"$.id": "w1", "$.items[1].id": "b", "$.items[0].n": "2", "$.o.p.q": "true", "$.missing": "", "$.items[5].id": ""}
	for p, want := range cases {
		if got := jsonPathString(doc, p); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	if jsonPathString([]byte("not json"), "$.id") != "" {
		t.Error("not JSON must give empty")
	}
}

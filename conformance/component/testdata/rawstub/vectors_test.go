package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// vectorCase is one case of a be-protocol vector file (vectors/case-file.schema.json).
type vectorCase struct {
	ID            string          `json:"id"`
	Op            string          `json:"op"`
	Input         json.RawMessage `json:"input"`
	Expected      json.RawMessage `json:"expected"`
	ExpectedError *struct {
		Reason string `json:"reason"`
	} `json:"expected_error"`
}

func loadVectors(t *testing.T, name string) []vectorCase {
	t.Helper()
	b, err := os.ReadFile("testdata/vectors/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []vectorCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Cases
}

func jsonEqual(t *testing.T, got any, want json.RawMessage) bool {
	t.Helper()
	gb, _ := json.Marshal(got)
	var g, w any
	_ = json.Unmarshal(gb, &g)
	_ = json.Unmarshal(want, &w)
	return reflect.DeepEqual(g, w)
}

func TestRedactionVectors(t *testing.T) {
	for _, c := range loadVectors(t, "redaction-redact.json") {
		t.Run(c.ID, func(t *testing.T) {
			switch c.Op {
			case "redact":
				var in struct {
					Record map[string]any `json:"record"`
				}
				_ = json.Unmarshal(c.Input, &in)
				if got := map[string]any{"record": redactRecord(in.Record)}; !jsonEqual(t, got, c.Expected) {
					t.Errorf("got %v, want %s", got, c.Expected)
				}
			case "protected_key":
				var in struct{ Key string }
				_ = json.Unmarshal(c.Input, &in)
				if got := map[string]bool{"protected": protectedKey(in.Key)}; !jsonEqual(t, got, c.Expected) {
					t.Errorf("%s: got %v, want %s", in.Key, got, c.Expected)
				}
			}
		})
	}
}

var vectorFormat = map[string]string{"integer": "int", "boolean": "bool", "duration_list": "durations"}

func TestConfigValueVectors(t *testing.T) {
	for _, c := range loadVectors(t, "config-values.json") {
		t.Run(c.ID, func(t *testing.T) {
			var in struct {
				Type     string   `json:"type"`
				Value    *string  `json:"value"`
				Default  *string  `json:"default"`
				Required bool     `json:"required"`
				Secret   bool     `json:"secret"`
				Minimum  *int64   `json:"minimum"`
				Schemes  []string `json:"schemes"`
				JSONKind string   `json:"json_kind"`
				Enum     []string `json:"enum"`
				Content  string   `json:"content"`
			}
			_ = json.Unmarshal(c.Input, &in)
			var got any
			var errClass string
			switch c.Op {
			case "parse_value":
				f := in.Type
				if m, ok := vectorFormat[f]; ok {
					f = m
				}
				p, err := parseTyped(valueSpec{format: f, value: in.Value, def: in.Default, required: in.Required,
					secret: in.Secret, minimum: in.Minimum, schemes: in.Schemes, jsonKind: in.JSONKind, enum: in.Enum})
				if err != nil {
					errClass = err.class
				}
				got = renderParsed(f, in.Secret, p)
			case "secret_text":
				v, ok := secretText(in.Content)
				switch {
				case ok:
					got = map[string]any{"set": true, "value": v}
				case in.Required:
					errClass = "CONFIG_MISSING"
				default:
					got = map[string]any{"set": false}
				}
			default:
				t.Skip("op not implemented by the stub: " + c.Op)
			}
			checkVector(t, c, got, errClass)
		})
	}
}

func renderParsed(format string, secret bool, p parsed) map[string]any {
	if !p.set {
		return map[string]any{"set": false}
	}
	if secret {
		return map[string]any{"set": true, "source": "file", "path": p.s}
	}
	var v any
	switch format {
	case "int":
		v = p.i
	case "bool":
		v = p.b
	case "duration":
		v = strconv.FormatInt(int64(p.d), 10)
	case "durations":
		var l []string
		for _, d := range p.ds {
			l = append(l, strconv.FormatInt(int64(d), 10))
		}
		v = l
	case "json":
		v = p.json
	default:
		v = p.s
	}
	return map[string]any{"set": true, "value": v}
}

func checkVector(t *testing.T, c vectorCase, got any, errClass string) {
	t.Helper()
	if c.ExpectedError != nil {
		if errClass != c.ExpectedError.Reason {
			t.Errorf("want error %s, got %q (value %v)", c.ExpectedError.Reason, errClass, got)
		}
		return
	}
	if errClass != "" {
		t.Errorf("unexpected error %s, want %s", errClass, c.Expected)
		return
	}
	if !jsonEqual(t, got, c.Expected) {
		t.Errorf("got %v, want %s", got, c.Expected)
	}
}

func TestFamilyAddressVectors(t *testing.T) {
	for _, c := range loadVectors(t, "config-endpoints.json") {
		if c.Op != "family_address" {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			var in struct {
				Key   string  `json:"key"`
				Value *string `json:"value"`
			}
			_ = json.Unmarshal(c.Input, &in)
			if !contains([]string{"AUTHZ_URL", "AUTHZ_GRPC_URL", "IAM_URL", "IAM_GRPC_URL"}, in.Key) {
				checkVector(t, c, nil, "CONFIG_KEY_INVALID")
				return
			}
			if in.Value == nil {
				checkVector(t, c, map[string]any{"present": false}, "")
				return
			}
			base, ok := familyURL(*in.Value)
			if !ok {
				checkVector(t, c, nil, "CONFIG_INVALID")
				return
			}
			if strings.HasSuffix(in.Key, "_GRPC_URL") {
				checkVector(t, c, map[string]any{"present": true, "target": strings.TrimPrefix(base, "http://")}, "")
				return
			}
			checkVector(t, c, map[string]any{"present": true, "base": base}, "")
		})
	}
}

func TestErrorLevelVectors(t *testing.T) {
	for _, c := range loadVectors(t, "errors-levels.json") {
		var in struct{ Code string }
		_ = json.Unmarshal(c.Input, &in)
		if got := map[string]string{"level": levelForCode(in.Code)}; !jsonEqual(t, got, c.Expected) {
			t.Errorf("%s: got %v, want %s", c.ID, got, c.Expected)
		}
	}
}

func TestErrorCodeVectors(t *testing.T) {
	for _, c := range loadVectors(t, "errors-codes.json") {
		var in struct{ Code, Reason, Domain string }
		_ = json.Unmarshal(c.Input, &in)
		switch c.Op {
		case "grpc_to_http":
			gc, ok := grpcCodes[in.Code]
			if !ok {
				checkVector(t, c, nil, "CODE_UNKNOWN")
				continue
			}
			e := &apiError{Code: in.Code, Reason: in.Reason, Domain: in.Domain}
			if !jsonEqual(t, map[string]int{"number": gc.num, "http": e.httpStatus()}, c.Expected) {
				t.Errorf("%s: got %d/%d, want %s", c.ID, gc.num, e.httpStatus(), c.Expected)
			}
		case "be_reason":
			ent, ok := beReasons[in.Reason]
			if !ok {
				continue // the stub carries only the be reasons it can raise; catalog_test checks those
			}
			if !jsonEqual(t, map[string]any{"code": ent.code, "domain": "be", "http": ent.http}, c.Expected) {
				t.Errorf("%s: got %v, want %s", c.ID, ent, c.Expected)
			}
		}
	}
}

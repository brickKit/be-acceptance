package protoschema

import "testing"

func TestSchemaValidatesYAMLDocument(t *testing.T) {
	s, err := ProtocolSchema("assembly-protocol.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateYAML(s, []byte("protocol: \"1.0\"\nlanguage: go\n")); err != nil {
		t.Fatalf("valid assembly refused: %v", err)
	}
	if err := ValidateYAML(s, []byte("language: go\n")); err == nil {
		t.Fatal("assembly without protocol accepted")
	}
}

func TestSchemaResolvesCrossFileRefs(t *testing.T) {
	// compconf-report.schema.json refers to conformance-cases.schema.json by relative $ref.
	s, err := ProtocolSchema("compconf-report.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string]any{"suite": "x"}
	if err := ValidateValue(s, bad); err == nil {
		t.Fatal("invalid report accepted")
	}
}

func TestCompileDoc(t *testing.T) {
	s, err := CompileDoc("p", []byte(`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ValidateJSON(s, []byte(`{"a":"x"}`)) != nil || ValidateJSON(s, []byte(`{"a":1}`)) == nil {
		t.Fatal("validation wrong")
	}
}

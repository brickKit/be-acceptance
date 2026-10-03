package fakes

import (
	"encoding/base64"
	"github.com/brickKit/be-acceptance/conformance/component/protoschema"
	"testing"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func validateContract(set, schema string, b []byte) error {
	s, err := protoschema.ContractSchema(set, schema)
	if err != nil {
		return err
	}
	return protoschema.ValidateJSON(s, b)
}

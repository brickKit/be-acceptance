// Package protoschema compiles the JSON Schemas of be-protocol and the two family contracts
// (contract-infra-authz, contract-infra-iam) from their pinned Go modules.
package protoschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"

	beprotocol "github.com/brickKit/be-protocol"
	authzcontract "github.com/brickKit/contract-infra-authz/v2"
	iamcontract "github.com/brickKit/contract-infra-iam"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

var (
	compilerOnce sync.Once
	compiler     *jsonschema.Compiler
	compilerErr  error
	schemaIDs    = map[string]string{} // "<set>/<file>" -> $id
	schemaMu     sync.Mutex
)

// schemaSets are the JSON Schema directories the suite validates against.
var schemaSets = map[string]fs.FS{
	"be-protocol": beprotocol.FS,
	"authz":       authzcontract.FS,
	"iam":         iamcontract.FS,
}

func loadCompiler() (*jsonschema.Compiler, error) {
	compilerOnce.Do(func() {
		c := jsonschema.NewCompiler()
		for set, fsys := range schemaSets {
			files, err := fs.Glob(fsys, "schemas/*.json")
			if err != nil {
				compilerErr = err
				return
			}
			for _, f := range files {
				b, err := fs.ReadFile(fsys, f)
				if err != nil {
					compilerErr = err
					return
				}
				doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
				if err != nil {
					compilerErr = fmt.Errorf("%s/%s: %w", set, f, err)
					return
				}
				id, _ := doc.(map[string]any)["$id"].(string)
				if id == "" {
					id = "https://compconf.invalid/" + set + "/" + f
				}
				if err := c.AddResource(id, doc); err != nil {
					compilerErr = err
					return
				}
				schemaIDs[set+"/"+path.Base(f)] = id
			}
		}
		compiler = c
	})
	return compiler, compilerErr
}

// ProtocolSchema compiles one of be-protocol's schemas by file name.
func ProtocolSchema(file string) (*jsonschema.Schema, error) {
	return compileSchema("be-protocol/" + file)
}

// ContractSchema compiles a family contract's schema: set is "authz" or "iam".
func ContractSchema(set, file string) (*jsonschema.Schema, error) {
	return compileSchema(set + "/" + file)
}

func compileSchema(key string) (*jsonschema.Schema, error) {
	c, err := loadCompiler()
	if err != nil {
		return nil, err
	}
	schemaMu.Lock()
	defer schemaMu.Unlock()
	id, ok := schemaIDs[key]
	if !ok {
		return nil, fmt.Errorf("schema %s not found", key)
	}
	return c.Compile(id)
}

// ValidateValue validates any Go value by its JSON encoding.
func ValidateValue(s *jsonschema.Schema, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ValidateJSON(s, b)
}

// ValidateJSON validates a JSON document.
func ValidateJSON(s *jsonschema.Schema, b []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return err
	}
	return conciseErr(s.Validate(doc))
}

// ValidateYAML validates a YAML document through its JSON form.
func ValidateYAML(s *jsonschema.Schema, b []byte) error {
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		return err
	}
	return ValidateValue(s, v)
}

func conciseErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if len(msg) > 600 {
		msg = msg[:600] + "…"
	}
	return fmt.Errorf("%s", strings.TrimSpace(msg))
}

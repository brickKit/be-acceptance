package compconf

import (
	"io/fs"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Operation guards: a permission key, or one of these two words (P6.2).
const (
	GuardPublic        = "public"
	GuardAuthenticated = "authenticated"
)

// Operation is one user-plane OpenAPI operation as the suite needs it.
type Operation struct {
	OperationID     string
	Method          string
	Path            string // server prefix + path template, e.g. /erp/sales/orders/{id}
	Guard           string // x-be-permission
	DeadlineSeconds int    // x-be-deadline-seconds, 0 = default
	MaxBodyBytes    int    // x-be-max-body-bytes, 0 = default
	Idempotency     bool   // accepts Idempotency-Key or idempotency_key
	Internal        bool   // x-be-internal: system traffic only, no guard needed (P3.16)
}

type oaDoc struct {
	Servers []struct {
		URL string `yaml:"url"`
	} `yaml:"servers"`
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

type oaOp struct {
	OperationID string `yaml:"operationId"`
	Permission  string `yaml:"x-be-permission"`
	Internal    bool   `yaml:"x-be-internal"`
	Deadline    int    `yaml:"x-be-deadline-seconds"`
	MaxBody     int    `yaml:"x-be-max-body-bytes"`
	Parameters  []struct {
		Name string `yaml:"name"`
		In   string `yaml:"in"`
	} `yaml:"parameters"`
	RequestBody struct {
		Content map[string]struct {
			Schema struct {
				Properties map[string]any `yaml:"properties"`
			} `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"requestBody"`
}

var httpMethods = map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true, "head": true}

// loadOperations reads every contracts/**/*.openapi.yaml.
func loadOperations(fsys fs.FS) ([]Operation, error) {
	var files []string
	err := fs.WalkDir(fsys, "contracts", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(p, ".openapi.yaml") || strings.HasSuffix(p, ".openapi.yml")) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []Operation
	for _, f := range files {
		ops, err := parseOpenAPI(fsys, f)
		if err != nil {
			return nil, err
		}
		out = append(out, ops...)
	}
	return out, nil
}

func parseOpenAPI(fsys fs.FS, file string) ([]Operation, error) {
	b, err := fs.ReadFile(fsys, file)
	if err != nil {
		return nil, err
	}
	var doc oaDoc
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	prefix := ""
	if len(doc.Servers) > 0 {
		prefix = strings.TrimSuffix(doc.Servers[0].URL, "/")
	}
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []Operation
	for _, p := range paths {
		for method, node := range doc.Paths[p] {
			if !httpMethods[method] {
				continue
			}
			var op oaOp
			if err := node.Decode(&op); err != nil {
				return nil, err
			}
			out = append(out, toOperation(prefix+p, method, op))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}

func toOperation(p, method string, op oaOp) Operation {
	o := Operation{OperationID: op.OperationID, Method: strings.ToUpper(method), Path: p,
		Guard: op.Permission, DeadlineSeconds: op.Deadline, MaxBodyBytes: op.MaxBody, Internal: op.Internal}
	for _, prm := range op.Parameters {
		if prm.In == "header" && strings.EqualFold(prm.Name, "Idempotency-Key") {
			o.Idempotency = true
		}
	}
	for _, ct := range op.RequestBody.Content {
		if _, ok := ct.Schema.Properties["idempotency_key"]; ok {
			o.Idempotency = true
		}
	}
	return o
}

// Protected reports whether the operation is not Public. A missing guard counts as protected
// (P3.16, fail closed); an x-be-internal operation carries no user and is not.
func (o Operation) Protected() bool { return o.Guard != GuardPublic && !o.Internal }

// KeyGuarded reports whether the guard is a permission key.
func (o Operation) KeyGuarded() bool {
	return o.Guard != GuardPublic && o.Guard != GuardAuthenticated && o.Guard != ""
}

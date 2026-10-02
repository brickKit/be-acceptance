package gates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const openapiBase = `openapi: 3.0.3
info: { title: t, version: 1.0.0 }
paths:
  /customers:
    parameters:
      - { name: X-Tenant, in: header, schema: { type: string } }
    get:
      parameters:
        - { name: cursor, in: query, schema: { type: string } }
        - { name: fields, in: query, required: true, schema: { type: string } }
        - { name: status_filter, in: query, schema: { $ref: "#/components/schemas/CustomerStatus" } }
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  customers: { type: array, items: { $ref: "#/components/schemas/Customer" } }
                  next_cursor: { type: string }
        "404": { description: not found }
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/CreateRequest" }
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Customer" }
  /customers/{id}:
    get:
      parameters:
        - { name: id, in: path, required: true, schema: { type: string } }
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Customer" }
components:
  schemas:
    CustomerStatus: { type: string, enum: [ACTIVE, FROZEN] }
    Customer:
      type: object
      required: [id]
      properties:
        id: { type: string }
        name: { type: string }
        status: { $ref: "#/components/schemas/CustomerStatus" }
    CreateRequest:
      type: object
      required: [name]
      properties:
        name: { type: string }
        tax_no: { type: string }
        address:
          type: object
          properties:
            city: { type: string }
`

// mutate 把 openapiBase 解析成 map，交给 f 改，再序列化回去。
func mutate(t *testing.T, f func(doc map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(openapiBase), &doc); err != nil {
		t.Fatal(err)
	}
	f(doc)
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// at 沿着键路径取一个 map 节点；数字段落用 "#<i>" 取切片元素。
func at(t *testing.T, doc map[string]any, keys ...string) map[string]any {
	t.Helper()
	var cur any = doc
	for _, k := range keys {
		switch c := cur.(type) {
		case map[string]any:
			cur = c[k]
		case []any:
			var i int
			for _, ch := range strings.TrimPrefix(k, "#") {
				i = i*10 + int(ch-'0')
			}
			cur = c[i]
		}
	}
	m, ok := cur.(map[string]any)
	if !ok {
		t.Fatalf("路径 %v 不是 map：%T", keys, cur)
	}
	return m
}

func findings(t *testing.T, newData []byte) []OpenAPIFinding {
	t.Helper()
	fs, err := OpenAPIBreaking([]byte(openapiBase), newData)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// expectOne 断言恰好一条，且 Rule 与 Location 符合预期。
func expectOne(t *testing.T, fs []OpenAPIFinding, rule, location string) {
	t.Helper()
	if len(fs) != 1 {
		t.Fatalf("期望恰好 1 条（%s @ %s），得到 %d：%+v", rule, location, len(fs), fs)
	}
	if fs[0].Rule != rule || fs[0].Location != location {
		t.Fatalf("期望 %s @ %s，得到 %s @ %s（%s）", rule, location, fs[0].Rule, fs[0].Location, fs[0].Detail)
	}
}

func TestOpenAPIBreaking_原样不变零违规(t *testing.T) {
	if fs := findings(t, []byte(openapiBase)); len(fs) != 0 {
		t.Fatalf("同一份契约不该有违规：%+v", fs)
	}
}

func TestOpenAPIBreaking_只增与放宽都不算破坏(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "info")["version"] = "2.0.0"
		at(t, d, "info")["description"] = "改了说明"
		// 新路径、新方法
		at(t, d, "paths")["/products"] = map[string]any{"get": map[string]any{"responses": map[string]any{"200": map[string]any{"description": "OK"}}}}
		at(t, d, "paths", "/customers/{id}")["delete"] = map[string]any{"responses": map[string]any{"204": map[string]any{"description": "gone"}}}
		// 新的可选参数（试点 mdm/customer 的 q 就是这一类）
		get := at(t, d, "paths", "/customers", "get")
		get["parameters"] = append([]any{map[string]any{"name": "q", "in": "query", "schema": map[string]any{"type": "string"}, "description": "关键字"}}, get["parameters"].([]any)...)
		// 必填参数改成可选：放宽
		at(t, d, "paths", "/customers", "get", "parameters", "#2")["required"] = false
		// 新响应码、新响应属性
		at(t, d, "paths", "/customers", "get", "responses")["400"] = map[string]any{"description": "bad"}
		at(t, d, "paths", "/customers", "get", "responses", "200", "content", "application/json", "schema", "properties")["total"] = map[string]any{"type": "integer"}
		// 新枚举值
		st := at(t, d, "components", "schemas", "CustomerStatus")
		st["enum"] = append(st["enum"].([]any), "ARCHIVED")
		// 请求里新增可选属性、必填改可选
		cr := at(t, d, "components", "schemas", "CreateRequest")
		at(t, d, "components", "schemas", "CreateRequest", "properties")["remark"] = map[string]any{"type": "string"}
		cr["required"] = []any{}
		// 响应里属性变成必填：对调用方只是多了保证
		at(t, d, "components", "schemas", "Customer")["required"] = []any{"id", "name"}
		// 新 schema
		at(t, d, "components", "schemas")["Product"] = map[string]any{"type": "object"}
	})
	if fs := findings(t, data); len(fs) != 0 {
		t.Fatalf("只增/放宽不该有违规：%+v", fs)
	}
}

func TestOpenAPIBreaking_删路径只报一条(t *testing.T) {
	data := mutate(t, func(d map[string]any) { delete(at(t, d, "paths"), "/customers/{id}") })
	expectOne(t, findings(t, data), "removed", "GET /customers/{id}")
}

func TestOpenAPIBreaking_删方法(t *testing.T) {
	data := mutate(t, func(d map[string]any) { delete(at(t, d, "paths", "/customers"), "post") })
	expectOne(t, findings(t, data), "removed", "POST /customers")
}

func TestOpenAPIBreaking_删参数(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		get := at(t, d, "paths", "/customers", "get")
		get["parameters"] = get["parameters"].([]any)[1:] // 去掉 cursor
	})
	expectOne(t, findings(t, data), "removed", "GET /customers › param query:cursor")
}

func TestOpenAPIBreaking_删路径级参数(t *testing.T) {
	data := mutate(t, func(d map[string]any) { delete(at(t, d, "paths", "/customers"), "parameters") })
	fs := findings(t, data)
	// 路径级参数作用于该路径下每个方法
	if len(fs) != 2 {
		t.Fatalf("GET 与 POST 各一条，得到 %+v", fs)
	}
	for _, f := range fs {
		if f.Rule != "removed" || !strings.HasSuffix(f.Location, "› param header:X-Tenant") {
			t.Errorf("意外：%+v", f)
		}
	}
}

func TestOpenAPIBreaking_参数的in变了(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "paths", "/customers", "get", "parameters", "#0")["in"] = "header"
	})
	fs := findings(t, data)
	expectOne(t, fs, "removed", "GET /customers › param query:cursor")
}

func TestOpenAPIBreaking_参数改名(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "paths", "/customers", "get", "parameters", "#0")["name"] = "page_token"
	})
	expectOne(t, findings(t, data), "removed", "GET /customers › param query:cursor")
}

func TestOpenAPIBreaking_可选参数变必填(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "paths", "/customers", "get", "parameters", "#0")["required"] = true
	})
	expectOne(t, findings(t, data), "became-required", "GET /customers › param query:cursor")
}

func TestOpenAPIBreaking_已有操作新增必填参数(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		get := at(t, d, "paths", "/customers", "get")
		get["parameters"] = append(get["parameters"].([]any), map[string]any{"name": "tenant", "in": "query", "required": true, "schema": map[string]any{"type": "string"}})
	})
	expectOne(t, findings(t, data), "new-required", "GET /customers › param query:tenant")
}

func TestOpenAPIBreaking_新操作的必填参数不算(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "paths")["/orders/{id}"] = map[string]any{"get": map[string]any{
			"parameters": []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}},
			"responses":  map[string]any{"200": map[string]any{"description": "OK"}},
		}}
	})
	if fs := findings(t, data); len(fs) != 0 {
		t.Fatalf("全新操作的必填参数不是破坏：%+v", fs)
	}
}

func TestOpenAPIBreaking_删schema属性(t *testing.T) {
	data := mutate(t, func(d map[string]any) { delete(at(t, d, "components", "schemas", "Customer", "properties"), "name") })
	expectOne(t, findings(t, data), "removed", "schema Customer › .name")
}

func TestOpenAPIBreaking_删内联响应属性(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		delete(at(t, d, "paths", "/customers", "get", "responses", "200", "content", "application/json", "schema", "properties"), "next_cursor")
	})
	expectOne(t, findings(t, data), "removed", "GET /customers › response 200 › application/json › .next_cursor")
}

func TestOpenAPIBreaking_删嵌套请求属性(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		delete(at(t, d, "components", "schemas", "CreateRequest", "properties", "address", "properties"), "city")
	})
	expectOne(t, findings(t, data), "removed", "schema CreateRequest › .address › .city")
}

func TestOpenAPIBreaking_属性改类型(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "components", "schemas", "Customer", "properties", "name")["type"] = "integer"
	})
	fs := findings(t, data)
	expectOne(t, fs, "type-changed", "schema Customer › .name")
	if !strings.Contains(fs[0].Detail, "string") {
		t.Errorf("Detail 应写出原来的类型：%q", fs[0].Detail)
	}
}

func TestOpenAPIBreaking_引用换了schema(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "components", "schemas")["OtherStatus"] = map[string]any{"type": "string"}
		at(t, d, "components", "schemas", "Customer", "properties", "status")["$ref"] = "#/components/schemas/OtherStatus"
	})
	expectOne(t, findings(t, data), "ref-changed", "schema Customer › .status")
}

func TestOpenAPIBreaking_删枚举值(t *testing.T) {
	data := mutate(t, func(d map[string]any) { at(t, d, "components", "schemas", "CustomerStatus")["enum"] = []any{"ACTIVE"} })
	fs := findings(t, data)
	expectOne(t, fs, "enum-removed", "schema CustomerStatus")
	if !strings.Contains(fs[0].Detail, "FROZEN") {
		t.Errorf("Detail 应写出被删的值：%q", fs[0].Detail)
	}
}

func TestOpenAPIBreaking_删整个schema只报一条(t *testing.T) {
	data := mutate(t, func(d map[string]any) { delete(at(t, d, "components", "schemas"), "CustomerStatus") })
	expectOne(t, findings(t, data), "removed", "schema CustomerStatus")
}

func TestOpenAPIBreaking_删响应码(t *testing.T) {
	data := mutate(t, func(d map[string]any) { delete(at(t, d, "paths", "/customers", "get", "responses"), "404") })
	expectOne(t, findings(t, data), "removed", "GET /customers › response 404")
}

func TestOpenAPIBreaking_请求里的可选属性变必填(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "components", "schemas", "CreateRequest")["required"] = []any{"name", "tax_no"}
	})
	expectOne(t, findings(t, data), "became-required", "schema CreateRequest › .tax_no")
}

func TestOpenAPIBreaking_请求里新增必填属性(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		cr := at(t, d, "components", "schemas", "CreateRequest")
		at(t, d, "components", "schemas", "CreateRequest", "properties")["region"] = map[string]any{"type": "string"}
		cr["required"] = []any{"name", "region"}
	})
	expectOne(t, findings(t, data), "new-required", "schema CreateRequest › .region")
}

func TestOpenAPIBreaking_可选请求体变必填(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "paths", "/customers", "post", "requestBody")["required"] = false
	})
	base := data
	// base 里请求体可选，new 里必填
	fs, err := OpenAPIBreaking(base, []byte(openapiBase))
	if err != nil {
		t.Fatal(err)
	}
	expectOne(t, fs, "became-required", "POST /customers › requestBody")
}

func TestOpenAPIBreaking_组件级参数引用被解析(t *testing.T) {
	old := `openapi: 3.0.3
paths:
  /a:
    get:
      parameters: [{ $ref: "#/components/parameters/Cursor" }]
      responses: { "200": { description: OK } }
components:
  parameters:
    Cursor: { name: cursor, in: query, schema: { type: string } }
`
	broken := strings.Replace(old, "name: cursor", "name: page", 1)
	fs, err := OpenAPIBreaking([]byte(old), []byte(broken))
	if err != nil {
		t.Fatal(err)
	}
	expectOne(t, fs, "removed", "GET /a › param query:cursor")
}

func TestOpenAPIBreaking_allOf的属性合并比较(t *testing.T) {
	old := `openapi: 3.0.3
paths: {}
components:
  schemas:
    Base: { type: object, properties: { id: { type: string } } }
    Item:
      allOf:
        - $ref: "#/components/schemas/Base"
        - type: object
          properties: { qty: { type: string } }
`
	fs, err := OpenAPIBreaking([]byte(old), []byte(strings.Replace(old, "qty: { type: string }", "qty: { type: integer }", 1)))
	if err != nil {
		t.Fatal(err)
	}
	expectOne(t, fs, "type-changed", "schema Item › .qty")
}

func TestOpenAPIBreaking_新版本不是合法YAML报错(t *testing.T) {
	if _, err := OpenAPIBreaking([]byte(openapiBase), []byte("paths: [\n")); err == nil {
		t.Fatal("坏 YAML 应该报错")
	}
}

// ---- 发布 tag 的选取与整条 gate ----

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
}

// newComponentRepo 在 root/components/mdm/customer 建一个 git 仓库，提交 openapiBase。
func newComponentRepo(t *testing.T) (root, compDir, specPath string) {
	t.Helper()
	root = t.TempDir()
	compDir = filepath.Join(root, "components/mdm/customer")
	specPath = filepath.Join(compDir, "contracts/customer.openapi.yaml")
	write(t, specPath, openapiBase)
	gitRun(t, compDir, "init", "-q", "-b", "main")
	gitRun(t, compDir, "add", ".")
	gitRun(t, compDir, "commit", "-qm", "base")
	return
}

func TestLatestReleaseTag_取最高语义版本且忽略gen与预发布(t *testing.T) {
	needGit(t)
	_, compDir, _ := newComponentRepo(t)
	for _, tag := range []string{"v1.0.9", "v1.0.10", "gen/mdm/customer/v1.0.99", "v3.0.0-rc.1", "not-a-version"} {
		gitRun(t, compDir, "tag", tag)
	}
	tag, ok := latestReleaseTag(compDir)
	if !ok || tag != "v1.0.10" {
		t.Fatalf("应取 v1.0.10（1.0.10 > 1.0.9，gen/* 与预发布不算），得到 %q ok=%v", tag, ok)
	}

	// 2.x 起裸 tag 与 v tag 打在同一个提交上：同号时取裸 tag
	gitRun(t, compDir, "tag", "2.0.0")
	gitRun(t, compDir, "tag", "v2.0.0")
	tag, _ = latestReleaseTag(compDir)
	if tag != "2.0.0" {
		t.Fatalf("同号时应取裸 tag 2.0.0，得到 %q", tag)
	}
}

func TestLatestReleaseTag_没有tag或不是独立仓库(t *testing.T) {
	needGit(t)
	_, compDir, _ := newComponentRepo(t)
	if tag, ok := latestReleaseTag(compDir); ok {
		t.Fatalf("没有 tag 应返回 ok=false，得到 %q", tag)
	}

	// 组件目录不是自己的仓库根（只是外层仓库的子目录）时不能借用外层仓库的 tag
	outer := t.TempDir()
	gitRun(t, outer, "init", "-q", "-b", "main")
	write(t, filepath.Join(outer, "components/mdm/customer/x.txt"), "x")
	gitRun(t, outer, "add", ".")
	gitRun(t, outer, "commit", "-qm", "outer")
	gitRun(t, outer, "tag", "9.9.9")
	if tag, ok := latestReleaseTag(filepath.Join(outer, "components/mdm/customer")); ok {
		t.Fatalf("不该拿到外层仓库的 tag，得到 %q", tag)
	}
}

func TestOpenAPIAdditiveScan_对着上一个1x发布tag比较(t *testing.T) {
	needGit(t)
	root, compDir, specPath := newComponentRepo(t)
	gitRun(t, compDir, "tag", "v1.0.10")

	// main 上往 2.0.0 走：先提交一次纯新增（不算违规）
	write(t, specPath, strings.Replace(openapiBase, "version: 1.0.0", "version: 2.0.0", 1))
	gitRun(t, compDir, "commit", "-qam", "2.0.0")

	vs, notices, err := OpenAPIAdditiveScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 || len(notices) != 0 {
		t.Fatalf("只改了 info.version，不该有违规或提示：%+v %v", vs, notices)
	}

	// 工作区删掉 404：要按 v1.0.10 报出来
	var doc map[string]any
	_ = yaml.Unmarshal([]byte(openapiBase), &doc)
	delete(at(t, doc, "paths", "/customers", "get", "responses"), "404")
	broken, _ := yaml.Marshal(doc)
	write(t, specPath, string(broken))

	vs, _, err = OpenAPIAdditiveScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("期望 1 条，得到 %+v", vs)
	}
	v := vs[0]
	if v.Component != "mdm/customer" || v.File != "contracts/customer.openapi.yaml" || v.BaseTag != "v1.0.10" ||
		v.Rule != "removed" || v.Location != "GET /customers › response 404" {
		t.Fatalf("字段不对：%+v", v)
	}
}

func TestOpenAPIAdditiveScan_已提交但未发布的破坏也要报(t *testing.T) {
	needGit(t)
	root, compDir, specPath := newComponentRepo(t)
	gitRun(t, compDir, "tag", "1.0.0")
	write(t, specPath, strings.Replace(openapiBase, "        tax_no: { type: string }\n", "", 1))
	gitRun(t, compDir, "commit", "-qam", "删 tax_no")

	vs, _, err := OpenAPIAdditiveScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Location != "schema CreateRequest › .tax_no" {
		t.Fatalf("工作区与 HEAD 一致也要对着 tag 报：%+v", vs)
	}
}

func TestOpenAPIAdditiveScan_没有tag或新文件时跳过并提示(t *testing.T) {
	needGit(t)
	root, compDir, _ := newComponentRepo(t)

	vs, notices, err := OpenAPIAdditiveScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 || len(notices) != 1 || !strings.Contains(notices[0].Text, "mdm/customer") || notices[0].Warn {
		t.Fatalf("没有 tag：应 0 违规 + 1 条提示，得到 %+v %v", vs, notices)
	}

	gitRun(t, compDir, "tag", "1.0.0")
	write(t, filepath.Join(compDir, "contracts/extra.openapi.yaml"), "openapi: 3.0.3\npaths: {}\n")
	vs, notices, err = OpenAPIAdditiveScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 || len(notices) != 1 || !strings.Contains(notices[0].Text, "extra.openapi.yaml") || notices[0].Warn {
		t.Fatalf("tag 里没有的新契约：应 0 违规 + 1 条提示，得到 %+v %v", vs, notices)
	}
}

// make gates 传的是 --root .：相对路径下也必须认出组件仓库的 tag（真机首跑时全部被误判成"没有 tag"）。
func TestOpenAPIAdditiveScan_相对root也能找到tag(t *testing.T) {
	needGit(t)
	root, compDir, specPath := newComponentRepo(t)
	gitRun(t, compDir, "tag", "v1.0.0")
	write(t, specPath, strings.Replace(openapiBase, "        tax_no: { type: string }\n", "", 1))
	t.Chdir(root)

	vs, notices, err := OpenAPIAdditiveScan(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 0 || len(vs) != 1 || vs[0].BaseTag != "v1.0.0" {
		t.Fatalf("相对 root 应对比 v1.0.0 并报 1 条，得到 %+v %v", vs, notices)
	}
}

func TestOpenAPIBreaking_数组元素与map值的类型变化(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "components", "schemas")["Other"] = map[string]any{"type": "object"}
		at(t, d, "paths", "/customers", "get", "responses", "200", "content", "application/json", "schema", "properties", "customers", "items")["$ref"] = "#/components/schemas/Other"
	})
	expectOne(t, findings(t, data), "ref-changed", "GET /customers › response 200 › application/json › .customers › []")

	old := `openapi: 3.0.3
paths: {}
components:
  schemas:
    Labels: { type: object, additionalProperties: { type: string } }
`
	fs, err := OpenAPIBreaking([]byte(old), []byte(strings.Replace(old, "additionalProperties: { type: string }", "additionalProperties: { type: integer }", 1)))
	if err != nil {
		t.Fatal(err)
	}
	expectOne(t, fs, "type-changed", "schema Labels › {}")
}

// yaml.v3 把不加引号的 200: 解成 map[interface{}]interface{}——不处理的话整个 responses 被静默跳过。
func TestOpenAPIBreaking_不加引号的响应码(t *testing.T) {
	old := `openapi: 3.0.3
paths:
  /a:
    get:
      responses:
        200: { description: OK }
        404: { description: nf }
`
	fs, err := OpenAPIBreaking([]byte(old), []byte(strings.Replace(old, "        404: { description: nf }\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	expectOne(t, fs, "removed", "GET /a › response 404")
}

// ---- 修复轮（06b T8 B1 审查）----

// I1：tag 里有、工作区里没有的整份契约（删了或改了名）必须报出来。
func TestOpenAPIAdditiveScan_整份契约删掉或改名要报(t *testing.T) {
	needGit(t)
	root, compDir, specPath := newComponentRepo(t)
	gitRun(t, compDir, "tag", "2.0.0")

	// 改名：customer.openapi.yaml → api.openapi.yaml
	gitRun(t, compDir, "mv", "contracts/customer.openapi.yaml", "contracts/api.openapi.yaml")
	vs, notices, err := OpenAPIAdditiveScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].File != "contracts/customer.openapi.yaml" || vs[0].Rule != "removed" || vs[0].BaseTag != "2.0.0" {
		t.Fatalf("改名后旧文件必须报 removed，得到 %+v", vs)
	}
	if len(notices) != 1 || !strings.Contains(notices[0].Text, "api.openapi.yaml") {
		t.Fatalf("新名字照旧按新契约提示，得到 %+v", notices)
	}

	// 整份删掉：工作区一份契约都不剩
	if err := os.Remove(filepath.Join(compDir, "contracts/api.openapi.yaml")); err != nil {
		t.Fatal(err)
	}
	_ = specPath
	vs, _, err = OpenAPIAdditiveScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].File != "contracts/customer.openapi.yaml" || vs[0].Rule != "removed" {
		t.Fatalf("契约全删了也必须报，得到 %+v", vs)
	}
}

// I3：响应一侧不再保证返回。
func TestOpenAPIBreaking_响应属性不再必填(t *testing.T) {
	data := mutate(t, func(d map[string]any) { at(t, d, "components", "schemas", "Customer")["required"] = []any{} })
	expectOne(t, findings(t, data), "no-longer-required", "schema Customer › .id")
}

func TestOpenAPIBreaking_内联响应属性不再必填(t *testing.T) {
	old := `openapi: 3.0.3
paths:
  /a:
    get:
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: { type: object, required: [n], properties: { n: { type: string } } }
`
	fs, err := OpenAPIBreaking([]byte(old), []byte(strings.Replace(old, "required: [n], ", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	expectOne(t, fs, "no-longer-required", "GET /a › response 200 › application/json › .n")
}

// I3：响应一侧变可空（3.0 的 nullable 与 3.1 的 type 带 null 两种写法）。
func TestOpenAPIBreaking_响应属性变可空(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "components", "schemas", "Customer", "properties", "name")["nullable"] = true
	})
	expectOne(t, findings(t, data), "became-nullable", "schema Customer › .name")

	data = mutate(t, func(d map[string]any) {
		at(t, d, "components", "schemas", "Customer", "properties", "name")["type"] = []any{"string", "null"}
	})
	expectOne(t, findings(t, data), "became-nullable", "schema Customer › .name")
}

// 请求一侧放宽成可空、去掉必填都不是破坏。
func TestOpenAPIBreaking_请求属性变可空不算破坏(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "components", "schemas", "CreateRequest", "properties", "tax_no")["nullable"] = true
		at(t, d, "components", "schemas", "CreateRequest", "properties", "name")["type"] = []any{"string", "null"}
	})
	if fs := findings(t, data); len(fs) != 0 {
		t.Fatalf("请求一侧变可空是放宽：%+v", fs)
	}
}

// 请求与响应共用的 schema 按保守处理：响应一侧的规则照样适用。
func TestOpenAPIBreaking_共用schema不再必填也报(t *testing.T) {
	old := `openapi: 3.0.3
paths:
  /a:
    post:
      requestBody: { content: { application/json: { schema: { $ref: "#/components/schemas/Line" } } } }
      responses:
        "200": { description: OK, content: { application/json: { schema: { $ref: "#/components/schemas/Line" } } } }
components:
  schemas:
    Line: { type: object, required: [qty], properties: { qty: { type: string } } }
`
	fs, err := OpenAPIBreaking([]byte(old), []byte(strings.Replace(old, "required: [qty], ", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	expectOne(t, fs, "no-longer-required", "schema Line › .qty")
}

// Minor 1：format 变了；请求一侧给已有字段新加 enum（收窄）。
func TestOpenAPIBreaking_format变了(t *testing.T) {
	old := `openapi: 3.0.3
paths: {}
components:
  schemas:
    T: { type: object, properties: { at: { type: string, format: date-time }, n: { type: integer, format: int32 } } }
`
	neu := strings.Replace(strings.Replace(old, "format: date-time", "format: date", 1), "format: int32", "format: int64", 1)
	fs, err := OpenAPIBreaking([]byte(old), []byte(neu))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[0].Rule != "format-changed" || fs[1].Rule != "format-changed" ||
		fs[0].Location != "schema T › .at" || fs[1].Location != "schema T › .n" {
		t.Fatalf("两处 format 变化都要报，得到 %+v", fs)
	}
}

func TestOpenAPIBreaking_请求一侧新加enum(t *testing.T) {
	data := mutate(t, func(d map[string]any) {
		at(t, d, "components", "schemas", "CreateRequest", "properties", "tax_no")["enum"] = []any{"A", "B"}
		// 响应一侧加 enum 只是多了保证，不报
		at(t, d, "components", "schemas", "Customer", "properties", "name")["enum"] = []any{"X"}
	})
	expectOne(t, findings(t, data), "enum-added", "schema CreateRequest › .tax_no")
}

// Minor 2：基线只取 HEAD 的祖先上的 tag。
func TestLatestReleaseTag_只认HEAD祖先上的tag(t *testing.T) {
	needGit(t)
	_, compDir, specPath := newComponentRepo(t)
	gitRun(t, compDir, "tag", "2.0.0")
	gitRun(t, compDir, "checkout", "-q", "-b", "side")
	write(t, specPath, openapiBase+"# side\n")
	gitRun(t, compDir, "commit", "-qam", "side")
	gitRun(t, compDir, "tag", "2.1.0") // 旁支（或比检出的指针更新）的 tag
	gitRun(t, compDir, "checkout", "-q", "main")

	tag, ok := latestReleaseTag(compDir)
	if !ok || tag != "2.0.0" {
		t.Fatalf("HEAD 的祖先上最高是 2.0.0，得到 %q ok=%v", tag, ok)
	}
}

// Minor 3：组件目录不是独立仓库（子模块没初始化 / 只是外层仓库的子目录）时，是 ⚠ 不是 ℹ。
func TestOpenAPIAdditiveScan_不是独立仓库时警告(t *testing.T) {
	needGit(t)
	outer := t.TempDir()
	write(t, filepath.Join(outer, "components/mdm/customer/contracts/customer.openapi.yaml"), openapiBase)
	gitRun(t, outer, "init", "-q", "-b", "main")
	gitRun(t, outer, "add", ".")
	gitRun(t, outer, "commit", "-qm", "outer")
	gitRun(t, outer, "tag", "1.0.0")
	if err := os.MkdirAll(filepath.Join(outer, "components/mdm/empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	vs, notices, err := OpenAPIAdditiveScan(outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 || len(notices) != 2 {
		t.Fatalf("两个目录各一条警告，得到 %+v %+v", vs, notices)
	}
	for _, n := range notices {
		if !n.Warn {
			t.Errorf("没有对比到的组件要标成警告：%+v", n)
		}
	}
}

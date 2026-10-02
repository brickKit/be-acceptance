package gates

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// OpenAPIFinding 是两份 OpenAPI 文档之间的一条破坏性变更。
type OpenAPIFinding struct {
	// Location 是变更所在位置，段与段之间用 " › " 连接，例如
	//   GET /customers › param query:cursor
	//   GET /customers › response 200 › application/json › .next_cursor
	//   schema Customer › .name
	Location string
	// Rule：
	//   removed         —— 路径 / 方法 / 参数 / 响应码 / 媒体类型 / schema / 属性被删（参数改 in 或改名也落在这里）
	//   type-changed    —— type 变了
	//   ref-changed     —— $ref 指向的 schema 变了（按名字比较）
	//   enum-removed    —— 删了枚举值
	//   became-required —— 原来可选的参数 / 请求体 / 请求属性变成必填
	//   new-required    —— 已有操作或已有请求 schema 上新增了必填的输入（决策 0302：新字段对既有调用方必须是可选的）
	Rule   string
	Detail string
}

// OpenAPIBreakingViolation 是 gate 报出的一条违规：哪个组件的哪份契约，对比的是哪个 tag。
type OpenAPIBreakingViolation struct {
	Component string // 组件 ID，如 "mdm/customer"
	File      string // 相对组件目录，如 "contracts/customer.openapi.yaml"
	BaseTag   string // 对比基线，如 "v1.0.10" 或 "2.0.0"
	OpenAPIFinding
}

// OpenAPIAdditiveScan 对每个 components/<scope>/<name>/contracts/*.openapi.yaml，
// 把工作区版本与该组件仓库最近一次发布 tag 里的同一路径比较，只允许新增
// （决策 0302）。`buf breaking` 只管 .proto，REST 契约此前全靠人眼（06b 试点评审 Part 3 (c)-7）。
//
// 基线是"最近一次发布"，不是 main：06b 期间 main 正从 1.x 走向 2.0.0，契约仍必须
// 相对最后一个 1.x tag 只增；已经提交但还没发布的破坏同样要拦。
//
// 没有发布 tag、或 tag 里还没有这份文件（新契约）、或 tag 里的旧版本解析不了时，
// 跳过并返回一条提示（notices），不判红。
func OpenAPIAdditiveScan(root string) ([]OpenAPIBreakingViolation, []string, error) {
	componentsDir := filepath.Join(root, "components")
	var violations []OpenAPIBreakingViolation
	var notices []string

	for _, compDir := range componentDirs(componentsDir) {
		id := componentID(componentsDir, compDir)
		specs, err := filepath.Glob(filepath.Join(compDir, "contracts", "*.openapi.yaml"))
		if err != nil {
			return nil, nil, err
		}
		if len(specs) == 0 {
			continue
		}
		sort.Strings(specs)
		tag, ok := latestReleaseTag(compDir)
		if !ok {
			notices = append(notices, fmt.Sprintf("%s：没有发布 tag（X.Y.Z 或 vX.Y.Z），跳过 openapi 只增检查", id))
			continue
		}
		for _, spec := range specs {
			rel := filepath.ToSlash(filepath.Join("contracts", filepath.Base(spec)))
			newData, err := os.ReadFile(spec)
			if err != nil {
				return nil, nil, fmt.Errorf("读 %s/%s：%w", id, rel, err)
			}
			oldData, err := exec.Command("git", "-C", compDir, "show", tag+":"+rel).Output()
			if err != nil {
				notices = append(notices, fmt.Sprintf("%s/%s：%s 里还没有这份文件（新契约），跳过", id, rel, tag))
				continue
			}
			if _, err := flattenOpenAPI(oldData); err != nil {
				notices = append(notices, fmt.Sprintf("%s/%s：%s 里的旧版本解析不了（%v），跳过", id, rel, tag, err))
				continue
			}
			fs, err := OpenAPIBreaking(oldData, newData)
			if err != nil {
				return nil, nil, fmt.Errorf("%s/%s：%w", id, rel, err)
			}
			for _, f := range fs {
				violations = append(violations, OpenAPIBreakingViolation{Component: id, File: rel, BaseTag: tag, OpenAPIFinding: f})
			}
		}
	}
	return violations, notices, nil
}

// releaseTagRe：发布 tag。2.x 起一个提交上有 `2.0.0`（brickKit）与 `v2.0.0`（Go）两个 tag；
// 1.x 只打过 `v1.0.x`。`gen/...` 是契约子模块自己的发布节奏，预发布不算发布。
var releaseTagRe = regexp.MustCompile(`^(v?)([0-9]+)\.([0-9]+)\.([0-9]+)$`)

// latestReleaseTag 返回组件仓库里语义版本最高的发布 tag；同号时取裸 tag。
// 组件目录不是自己仓库的根（只是外层仓库的一个子目录）时返回 ok=false——
// 否则 `git -C` 会一路向上找到外层仓库，拿外层的 tag 当基线。
func latestReleaseTag(compDir string) (string, bool) {
	top, err := exec.Command("git", "-C", compDir, "rev-parse", "--show-toplevel").Output()
	if err != nil || !samePath(strings.TrimSpace(string(top)), compDir) {
		return "", false
	}
	out, err := exec.Command("git", "-C", compDir, "tag", "--list").Output()
	if err != nil {
		return "", false
	}
	best, bestKey := "", [4]int{-1}
	for _, tag := range strings.Fields(string(out)) {
		m := releaseTagRe.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		var key [4]int
		for i := 0; i < 3; i++ {
			key[i], _ = strconv.Atoi(m[i+2])
		}
		if m[1] == "" {
			key[3] = 1 // 同号时裸 tag 优先
		}
		if lessKey(bestKey, key) {
			best, bestKey = tag, key
		}
	}
	return best, best != ""
}

func lessKey(a, b [4]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// samePath 比较两个路径是否指向同一目录。先转绝对路径：--root 通常是 "."，
// 而 git rev-parse --show-toplevel 给的是绝对路径。
func samePath(a, b string) bool {
	if abs, err := filepath.Abs(a); err == nil {
		a = abs
	}
	if abs, err := filepath.Abs(b); err == nil {
		b = abs
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// ---- 比较 ----

const locSep = " › "

// oaAttr 是某个位置上的一个属性值：type / ref / enum。
type oaAttr struct{ loc, kind, value string }

// oaFlat 是一份 OpenAPI 文档拍平后的结果。$ref 指向的组件 schema 不展开，
// 按名字记成 ref 属性，组件 schema 自己在 "schema <名字>" 位置下单独拍平一次。
type oaFlat struct {
	present    map[string]bool // 存在的位置
	attrs      map[oaAttr]bool
	optional   map[string]bool // 可选的参数 / 请求体 / 属性 → 是否在请求一侧
	requiredIn map[string]bool // 请求一侧必填的参数 / 请求体 / 属性
	types      map[string][]string
}

// OpenAPIBreaking 比较两份 OpenAPI 文档，返回 new 相对 old 的全部破坏性变更。
// 一个位置被整个删掉时，只报它自己，不再逐条报它下面的子位置。
func OpenAPIBreaking(oldData, newData []byte) ([]OpenAPIFinding, error) {
	old, err := flattenOpenAPI(oldData)
	if err != nil {
		return nil, fmt.Errorf("旧版本解析失败：%w", err)
	}
	cur, err := flattenOpenAPI(newData)
	if err != nil {
		return nil, fmt.Errorf("不是合法的 OpenAPI YAML：%w", err)
	}

	removed := func(loc string) bool { return old.present[loc] && !cur.present[loc] }
	// underRemoved：某个祖先位置已经整个删掉（那一条已经报过）。
	underRemoved := func(loc string) bool {
		segs := strings.Split(loc, locSep)
		for i := 1; i < len(segs); i++ {
			if removed(strings.Join(segs[:i], locSep)) {
				return true
			}
		}
		return false
	}

	var out []OpenAPIFinding
	for loc := range old.present {
		if removed(loc) && !underRemoved(loc) {
			out = append(out, OpenAPIFinding{Location: loc, Rule: "removed", Detail: "删掉了（参数改 in 或改名也算删）"})
		}
	}
	for a := range old.attrs {
		if cur.attrs[a] || !cur.present[a.loc] || underRemoved(a.loc) {
			continue
		}
		switch a.kind {
		case "type":
			now := strings.Join(cur.types[a.loc], " / ")
			if now == "" {
				now = "（没有 type）"
			}
			out = append(out, OpenAPIFinding{Location: a.loc, Rule: "type-changed", Detail: "type 原来是 " + a.value + "，现在是 " + now})
		case "ref":
			out = append(out, OpenAPIFinding{Location: a.loc, Rule: "ref-changed", Detail: "原来引用 " + a.value + "，现在不再引用它"})
		case "enum":
			out = append(out, OpenAPIFinding{Location: a.loc, Rule: "enum-removed", Detail: "删了枚举值 " + a.value})
		}
	}
	for loc, inRequest := range old.optional {
		_, stillOptional := cur.optional[loc] // 值是"是否在请求一侧"，这里只看在不在
		if inRequest && cur.present[loc] && !stillOptional && !underRemoved(loc) {
			out = append(out, OpenAPIFinding{Location: loc, Rule: "became-required", Detail: "原来可选，现在必填"})
		}
	}
	for loc := range cur.requiredIn {
		parent := parentLoc(loc)
		if !old.present[loc] && parent != "" && old.present[parent] {
			out = append(out, OpenAPIFinding{Location: loc, Rule: "new-required", Detail: "在已有的 " + parent + " 上新增了必填输入，老调用方不会传它"})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Location != out[j].Location {
			return out[i].Location < out[j].Location
		}
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Detail < out[j].Detail
	})
	return out, nil
}

func parentLoc(loc string) string {
	i := strings.LastIndex(loc, locSep)
	if i < 0 {
		return ""
	}
	return loc[:i]
}

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func flattenOpenAPI(data []byte) (*oaFlat, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	f := &oaFlat{
		present:    map[string]bool{},
		attrs:      map[oaAttr]bool{},
		optional:   map[string]bool{},
		requiredIn: map[string]bool{},
		types:      map[string][]string{},
	}
	comps := asMap(doc["components"])
	reqSchemas := requestSchemas(doc, comps)

	for path, rawItem := range asMap(doc["paths"]) {
		item := asMap(resolveComponent(rawItem, comps))
		for _, method := range httpMethods {
			op := asMap(item[method])
			if op == nil {
				continue
			}
			opLoc := strings.ToUpper(method) + " " + path
			f.present[opLoc] = true
			f.flattenParams(opLoc, item["parameters"], op["parameters"], comps)
			f.flattenRequestBody(opLoc, op["requestBody"], comps)
			for code, rawResp := range asMap(op["responses"]) {
				rLoc := opLoc + locSep + "response " + code
				f.present[rLoc] = true
				f.flattenContent(rLoc, asMap(resolveComponent(rawResp, comps))["content"], false)
			}
		}
	}
	for name, s := range asMap(comps["schemas"]) {
		loc := "schema " + name
		f.present[loc] = true
		f.walkSchema(s, loc, reqSchemas[name])
	}
	return f, nil
}

// flattenParams：路径级参数作用于每个方法，操作级同 in+name 的覆盖它。
func (f *oaFlat) flattenParams(opLoc string, pathLevel, opLevel any, comps map[string]any) {
	params := map[string]map[string]any{}
	for _, list := range []any{pathLevel, opLevel} {
		for _, raw := range asSlice(list) {
			p := asMap(resolveComponent(raw, comps))
			name, _ := p["name"].(string)
			in, _ := p["in"].(string)
			if name == "" || in == "" {
				continue
			}
			params[in+":"+name] = p
		}
	}
	for key, p := range params {
		loc := opLoc + locSep + "param " + key
		f.present[loc] = true
		required, _ := p["required"].(bool)
		f.markRequired(loc, required || p["in"] == "path", true)
		f.walkSchema(p["schema"], loc, true)
	}
}

func (f *oaFlat) flattenRequestBody(opLoc string, raw any, comps map[string]any) {
	rb := asMap(resolveComponent(raw, comps))
	if rb == nil {
		return
	}
	loc := opLoc + locSep + "requestBody"
	f.present[loc] = true
	required, _ := rb["required"].(bool)
	f.markRequired(loc, required, true)
	f.flattenContent(loc, rb["content"], true)
}

func (f *oaFlat) flattenContent(loc string, content any, inRequest bool) {
	for mediaType, media := range asMap(content) {
		mLoc := loc + locSep + mediaType
		f.present[mLoc] = true
		f.walkSchema(asMap(media)["schema"], mLoc, inRequest)
	}
}

func (f *oaFlat) markRequired(loc string, required, inRequest bool) {
	if required {
		if inRequest {
			f.requiredIn[loc] = true
		}
		return
	}
	f.optional[loc] = inRequest
}

// walkSchema 拍平一个 schema 节点。allOf / oneOf / anyOf 的成员并进同一个位置
// （属性取并集），$ref 只记引用的名字不展开。
func (f *oaFlat) walkSchema(node any, loc string, inRequest bool) {
	m := asMap(node)
	if m == nil {
		return
	}
	if ref, ok := m["$ref"].(string); ok {
		f.attrs[oaAttr{loc, "ref", refName(ref)}] = true
		return
	}
	if t, ok := m["type"]; ok {
		ts := typeString(t)
		f.attrs[oaAttr{loc, "type", ts}] = true
		f.types[loc] = append(f.types[loc], ts)
	}
	for _, v := range asSlice(m["enum"]) {
		f.attrs[oaAttr{loc, "enum", fmt.Sprint(v)}] = true
	}

	required := map[string]bool{}
	for _, r := range asSlice(m["required"]) {
		if s, ok := r.(string); ok {
			required[s] = true
		}
	}
	for name, child := range asMap(m["properties"]) {
		cLoc := loc + locSep + "." + name
		f.present[cLoc] = true
		f.markRequired(cLoc, required[name], inRequest)
		f.walkSchema(child, cLoc, inRequest)
	}
	// 数组元素与 map 值也是位置：不记 present 的话，它们身上的 type / $ref 变化会被当成"整个没了"而跳过。
	if items, ok := m["items"]; ok {
		f.present[loc+locSep+"[]"] = true
		f.walkSchema(items, loc+locSep+"[]", inRequest)
	}
	if ap := asMap(m["additionalProperties"]); ap != nil {
		f.present[loc+locSep+"{}"] = true
		f.walkSchema(ap, loc+locSep+"{}", inRequest)
	}
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		for _, member := range asSlice(m[key]) {
			f.walkSchema(member, loc, inRequest)
		}
	}
}

// requestSchemas 找出从请求一侧（参数、请求体）可达的组件 schema，含经 $ref 传递可达的。
// 只有这些 schema 上"可选变必填 / 新增必填"才算破坏；响应一侧多一个必填属性只是多了保证。
func requestSchemas(doc, comps map[string]any) map[string]bool {
	seeds := map[string]bool{}
	for _, rawItem := range asMap(doc["paths"]) {
		item := asMap(resolveComponent(rawItem, comps))
		for _, raw := range asSlice(item["parameters"]) {
			collectSchemaRefs(resolveComponent(raw, comps), seeds)
		}
		for _, method := range httpMethods {
			op := asMap(item[method])
			for _, raw := range asSlice(op["parameters"]) {
				collectSchemaRefs(resolveComponent(raw, comps), seeds)
			}
			collectSchemaRefs(resolveComponent(op["requestBody"], comps), seeds)
		}
	}
	schemas := asMap(comps["schemas"])
	reach := map[string]bool{}
	queue := make([]string, 0, len(seeds))
	for n := range seeds {
		queue = append(queue, n)
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if reach[n] {
			continue
		}
		reach[n] = true
		next := map[string]bool{}
		collectSchemaRefs(schemas[n], next)
		for m := range next {
			if !reach[m] {
				queue = append(queue, m)
			}
		}
	}
	return reach
}

func collectSchemaRefs(node any, out map[string]bool) {
	if m := asMap(node); m != nil {
		for k, v := range m {
			if s, ok := v.(string); ok && k == "$ref" && strings.HasPrefix(s, "#/components/schemas/") {
				out[refName(s)] = true
				continue
			}
			collectSchemaRefs(v, out)
		}
		return
	}
	for _, v := range asSlice(node) {
		collectSchemaRefs(v, out)
	}
}

// resolveComponent 展开指向 components 下参数 / 请求体 / 响应 / 路径项的 $ref；
// schema 的 $ref 不走这里（按名字比较）。
func resolveComponent(node any, comps map[string]any) any {
	for i := 0; i < 8; i++ {
		m := asMap(node)
		ref, ok := m["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/components/") {
			return node
		}
		parts := strings.SplitN(strings.TrimPrefix(ref, "#/components/"), "/", 2)
		if len(parts) != 2 || parts[0] == "schemas" {
			return node
		}
		node = asMap(comps[parts[0]])[parts[1]]
	}
	return node
}

func refName(ref string) string {
	if strings.HasPrefix(ref, "#/components/schemas/") {
		return strings.TrimPrefix(ref, "#/components/schemas/")
	}
	return ref
}

func typeString(t any) string {
	if s, ok := t.(string); ok {
		return s
	}
	var parts []string
	for _, v := range asSlice(t) {
		parts = append(parts, fmt.Sprint(v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// asMap 取一个 YAML 映射。yaml.v3 遇到非字符串键（比如不加引号的响应码 200:）
// 会解成 map[interface{}]interface{}，这里把键转成字符串，不让它被静默跳过。
func asMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out
	}
	return nil
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

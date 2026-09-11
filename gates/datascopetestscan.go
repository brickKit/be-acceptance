package gates

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DataScopeTestGap 是一个组件：`assembly.yaml` 声明了真实的 `data_scopes`
// 维度（不是 `none`），但测试文件里一个「越权/超出范围被拒绝」形状的测试
// 都没有。
type DataScopeTestGap struct {
	Component  string
	Dimensions []string // 声明了哪些维度，供报错信息说清楚该测什么
}

var dimensionRe = regexp.MustCompile(`dimension:\s*([a-z_]+)`)

// 判定一条测试是不是在验证"越权/超出数据范围被拒绝"，用两档信号：
//   - strongSignals 单独出现就算数——"forbidden"（`ErrForbidden`）是本仓库
//     四个既有组件（erp-sales/erp-finance/erp-inventory/infra-workflow）
//     几乎全部在用的专属错误标识，不太可能出现在无关测试里。
//   - scopeQualifiers + denialWords **必须成对出现**才算数——单独一个
//     "拒绝"/"看不到"这类词太泛，会跟"客户不存在""参数校验失败"这类
//     完全无关的拒绝撞上（实测踩坑：`crm-opportunity` 的
//     `TestCreateOpportunity_真实客户不存在时拒绝` 只是校验错误，第一版
//     只用单词表会把它误判成"已经测过数据权限边界"）。⚠️ `别人`/`他人`
//     是反过来的实测踩坑：`infra-notification` 真写了一条货真价实的
//     边界测试（`TestListMyRecords_别人的通知看不到...`），但名字里没有
//     "owner"/"越权"这类术语词，第一版判定成"没测过"——按这个组件自己
//     惯用的大白话补上限定词，比强迫所有组件的测试名都塞术语词更自然。
var (
	strongSignals   = []string{"forbidden"}
	scopeQualifiers = []string{"授权", "范围", "越权", "scope", "owner", "warehouse", "legal", "org", "dept", "别人", "他人"}
	denialWords     = []string{"拒绝", "看不到", "查不到", "没有"}
)

// DataScopeTestScan 检查每个声明了真实 `data_scopes` 维度的组件，测试文件
// 里是否至少有一个名字带越权/拒绝信号的测试函数。
//
// ⚠️ 判据故意是「组件级至少一条」，不是「每个维度各一条」——实测发现按
// 维度关键字（比如要求测试名必须出现 "owner"/"legal_entity"）精度不够：
// `infra-workflow` 的 `TestGetTaskDetail_范围外ErrForbidden` 等测试明显在
// 验证边界，但名字里从不出现具体维度名（用的是"范围外"/"两维"这类泛化
// 说法），按维度关键字匹配会把一个已经测得不错的组件误判成"缺"。组件级
// 判据换来的代价是查不出"测了 owner 忘了 org"这类更细的疏漏——那部分
// 留给 W-6 人工审查，这里只抓"完全没人测过"这种最严重的情形（建这个
// gate 时 `crm-opportunity`/`infra-notification` 两个组件真实属于这种
// 情况，见总纲 SOP-W-8）。
//
// 只扫 Go（`go/ast` 解析测试函数名）——目前 6 个声明了真实 `data_scopes`
// 的组件全是 Go；同 `ImportScan` 的既有判据：没有真实 Python/TS 样本前
// 不实现对应语言的扫描（`gates/importscan.go` 注释）。
func DataScopeTestScan(root string) ([]DataScopeTestGap, error) {
	componentsDir := filepath.Join(root, "components")
	var gaps []DataScopeTestGap
	for _, compDir := range componentDirs(componentsDir) {
		id := componentID(componentsDir, compDir)
		dims, err := dataScopeDimensions(filepath.Join(compDir, "assembly.yaml"))
		if err != nil {
			return nil, err
		}
		if len(dims) == 0 {
			continue
		}
		names, err := testFunctionNames(compDir)
		if err != nil {
			return nil, err
		}
		if !anyNameHasDataScopeSignal(names) {
			gaps = append(gaps, DataScopeTestGap{Component: id, Dimensions: dims})
		}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Component < gaps[j].Component })
	return gaps, nil
}

// dataScopeDimensions 读 `assembly.yaml` 的 `data_scopes` 段，返回声明的
// 维度名去重集合（`data_scopes: none` 或整段没有 `dimension` 字段都返回
// 空）。不用真正的 YAML 解析——`data_scopes` 是 flow-style 的
// `{ dimension: org, ... }` 列表，跟仓库里其它地方解析 `*.yaml`/
// `component.yaml` 的既有做法一样，正则/文本扫描够用，不为这一处引入
// YAML 依赖（`be-acceptance` 目前零第三方依赖）。
func dataScopeDimensions(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "data_scopes:") {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, nil
	}
	var block strings.Builder
	block.WriteString(lines[start])
	block.WriteByte('\n')
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		// 下一个顶层键：顶格开头（既不是空白缩进也不是续行）。
		if !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			break
		}
		block.WriteString(l)
		block.WriteByte('\n')
	}

	seen := map[string]bool{}
	var dims []string
	for _, m := range dimensionRe.FindAllStringSubmatch(block.String(), -1) {
		d := m[1]
		if !seen[d] {
			seen[d] = true
			dims = append(dims, d)
		}
	}
	return dims, nil
}

// testFunctionNames 收集一个组件目录下所有 `_test.go` 文件里的顶层测试
// 函数名（跳过方法，只要裸函数；跳过 `gen/` 生成代码目录）。
func testFunctionNames(compDir string) ([]string, error) {
	var names []string
	err := filepath.WalkDir(compDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "gen" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("解析 %s: %w", path, perr)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Test") {
				names = append(names, fn.Name.Name)
			}
		}
		return nil
	})
	return names, err
}

func anyNameHasDataScopeSignal(names []string) bool {
	for _, n := range names {
		if hasDataScopeSignal(n) {
			return true
		}
	}
	return false
}

func hasDataScopeSignal(name string) bool {
	low := strings.ToLower(name)
	for _, s := range strongSignals {
		if strings.Contains(low, s) {
			return true
		}
	}
	hasQualifier := false
	for _, q := range scopeQualifiers {
		if strings.Contains(low, strings.ToLower(q)) {
			hasQualifier = true
			break
		}
	}
	if !hasQualifier {
		return false
	}
	for _, d := range denialWords {
		if strings.Contains(low, d) {
			return true
		}
	}
	return false
}

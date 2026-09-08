package gates

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// textHit 是一次正则命中：某组件的某个文件某一行。
type textHit struct {
	Component string
	File      string
	Line      int
}

// scanTextForPattern 是 Python/TS 门禁共用的轻量文本扫描——Go 有 go/ast
// 免费可用，Python/TS 在 be-acceptance（一个纯 Go 工具）里没有对应的语法
// 树，只能逐行正则匹配。目录不存在（组件没有这个面）不是错误，直接
// 返回空。跳过 dir 本身的子目录（不递归）与测试文件，和 Go 版扫描器
// 的范围保持一致。
func scanTextForPattern(dir, component, ext string, pattern *regexp.Regexp) ([]textHit, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // 目录不存在很正常：组件可能没有这个面
	}
	var hits []textHit
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) || isTestFile(e.Name(), ext) {
			continue
		}
		filePath := filepath.Join(dir, e.Name())
		lines, err := grepLines(filePath, pattern)
		if err != nil {
			return nil, err
		}
		for _, ln := range lines {
			hits = append(hits, textHit{Component: component, File: filePath, Line: ln})
		}
	}
	return hits, nil
}

// isTestFile 认 Python 的 test_*.py/*_test.py 与 TS 的 *.test.ts/*.spec.ts——
// 两种语言生态各自的测试文件命名约定都不止一种写法。
func isTestFile(name, ext string) bool {
	base := strings.TrimSuffix(name, ext)
	return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test") ||
		strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".spec")
}

// grepLines 返回文件里匹配 pattern 的行号（1-based）。
func grepLines(path string, pattern *regexp.Regexp) ([]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []int
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		if pattern.MatchString(sc.Text()) {
			lines = append(lines, n)
		}
	}
	return lines, sc.Err()
}

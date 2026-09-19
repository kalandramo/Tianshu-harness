package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// grepTool 实现 grep（正则内容检索）。
type grepTool struct {
	baseTool
	Cwd string
	// MaxResults 是最大匹配行数（默认 100）。
	MaxResults int
	// MaxFileBytes 是单文件扫描上限（跳过超大文件）。
	MaxFileBytes int64
}

// Grep 构造 grep 工具。
func Grep(cwd string) Tool {
	t := &grepTool{Cwd: cwd, MaxResults: 100, MaxFileBytes: 8 << 20}
	t.def = contract.Definition{
		Name: "grep",
		Description: `用正则或字面量模式搜索文件内容。

- 用 grep 在源码中查找函数、类、模式或关键字
- 优先用本工具而不是 bash grep/rg——更快，且遵守 .gitignore
- 结果按文件分组并带行号
- pattern 可以是正则（默认）或字面量字符串（literal=true）
- context_lines 附带上下文行（设 2-3 可直接看到周边代码）`,
		InputSchema: objSchema(map[string]any{
			"pattern":       strProp("要搜索的正则或字面量模式"),
			"path":          strProp("要搜索的目录或文件（默认：cwd）"),
			"glob":          strProp("文件过滤，如 \"*.ts\" 或 \"*.{ts,tsx}\""),
			"literal":       boolProp("把 pattern 按字面量处理，不当正则（默认 false）"),
			"context_lines": intProp("每个匹配前后附带的上下文行数（默认 0）"),
			"max_results":   intProp("最大匹配行数（默认 100）"),
		}, "pattern"),
	}
	t.enabled = true
	t.concurrent = true
	return t
}

func (t *grepTool) Timeout(*CallParams) time.Duration { return 60 * time.Second }

func (t *grepTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	pattern := strArg(p.Input, "pattern")
	if pattern == "" {
		return contract.Result{Content: "grep 需要 pattern 参数", IsError: true}, nil
	}
	root := strArg(p.Input, "path")
	if root == "" {
		root = t.Cwd
	} else {
		vr := pathsafe.Validate(t.Cwd, root, pathsafe.ModeRead, nil)
		if !vr.OK {
			return contract.Result{Content: vr.Error, IsError: true}, nil
		}
		root = vr.Path
	}

	literal := boolArg(p.Input, "literal")
	ctxLines := intArg(p.Input, "context_lines", 0)
	maxResults := intArg(p.Input, "max_results", t.MaxResults)
	globFilter := strArg(p.Input, "glob")

	// 编译模式
	var re *regexp.Regexp
	var err error
	if literal {
		re, err = regexp.Compile(regexp.QuoteMeta(pattern))
	} else {
		re, err = regexp.Compile(pattern)
	}
	if err != nil {
		return contract.Result{
			Content: fmt.Sprintf("正则编译失败：%v（如需字面量匹配请设 literal=true）", err),
			IsError: true,
		}, nil
	}

	type fileMatches struct {
		path  string
		lines []string
	}
	var results []fileMatches
	total := 0

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			base := d.Name()
			if base == "node_modules" || base == ".git" || base == "dist" || base == "vendor" || base == ".rivet" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		// glob 过滤
		if globFilter != "" && !matchAnyGlob(rel, globFilter) {
			return nil
		}

		info, ierr := d.Info()
		if ierr != nil || info.Size() > t.MaxFileBytes {
			return nil
		}

		fm, n := scanFileForPattern(path, rel, re, ctxLines, maxResults-total)
		if n > 0 {
			results = append(results, fm)
			total += n
		}
		if total >= maxResults {
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return contract.Result{Content: fmt.Sprintf("搜索失败：%v", walkErr), IsError: true}, nil
	}

	if total == 0 {
		return contract.Result{
			Content: fmt.Sprintf("无匹配（pattern=%s）。\n\n注意：无匹配**不等于**不存在——"+
				"可能被 .gitignore 排除、模式过窄、或文件是二进制。若要确认缺失，换更宽的模式或直接 glob 文件名。", pattern),
		}, nil
	}

	var sb strings.Builder
	for _, fm := range results {
		sb.WriteString(fm.path)
		sb.WriteString(":\n")
		for _, l := range fm.lines {
			sb.WriteString("  ")
			sb.WriteString(l)
			sb.WriteString("\n")
		}
	}
	if total >= maxResults {
		fmt.Fprintf(&sb, "\n[已达上限 %d 条——结果被截断，请收窄 pattern 或 glob]\n", maxResults)
	}
	return contract.Result{Content: strings.TrimRight(sb.String(), "\n")}, nil
}

// scanFileForPattern 扫描单文件，返回匹配行（含上下文）。
func scanFileForPattern(path, rel string, re *regexp.Regexp, ctxLines, budget int) (struct {
	path  string
	lines []string
}, int) {
	type result = struct {
		path  string
		lines []string
	}
	out := result{path: rel}
	if budget <= 0 {
		return out, 0
	}

	f, err := os.Open(path)
	if err != nil {
		return out, 0
	}
	defer func() { _ = f.Close() }()

	var all []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		all = append(all, scanner.Text())
	}
	if scanner.Err() != nil {
		return out, 0
	}

	// 跳过疑似二进制（含 NUL 的文本行在 Scanner 里通常已断，这里再查一次）
	count := 0
	emitted := map[int]bool{}
	for i, line := range all {
		if !re.MatchString(line) {
			continue
		}
		lo := i - ctxLines
		if lo < 0 {
			lo = 0
		}
		hi := i + ctxLines
		if hi >= len(all) {
			hi = len(all) - 1
		}
		for j := lo; j <= hi; j++ {
			if emitted[j] {
				continue
			}
			emitted[j] = true
			marker := "  "
			if j == i {
				marker = "> "
			}
			out.lines = append(out.lines, fmt.Sprintf("%s%d: %s", marker, j+1, all[j]))
		}
		count++
		if count >= budget {
			break
		}
	}
	return out, count
}

// matchAnyGlob 判断路径是否匹配 glob 过滤（支持 {a,b} 多选一）。
func matchAnyGlob(rel, globFilter string) bool {
	// 展开 {a,b} 形式
	if strings.Contains(globFilter, "{") {
		for _, expanded := range expandBraces(globFilter) {
			if matchGlob(rel, expanded, strings.Contains(expanded, "**")) {
				return true
			}
		}
		return false
	}
	return matchGlob(rel, globFilter, strings.Contains(globFilter, "**"))
}

// expandBraces 展开 {a,b} 为多个模式。
func expandBraces(pattern string) []string {
	open := strings.Index(pattern, "{")
	if open < 0 {
		return []string{pattern}
	}
	closeIdx := strings.Index(pattern[open:], "}")
	if closeIdx < 0 {
		return []string{pattern}
	}
	closeIdx += open
	prefix := pattern[:open]
	suffix := pattern[closeIdx+1:]
	var out []string
	for _, alt := range strings.Split(pattern[open+1:closeIdx], ",") {
		out = append(out, prefix+alt+suffix)
	}
	return out
}

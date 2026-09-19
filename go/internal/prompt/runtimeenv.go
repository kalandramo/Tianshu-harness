package prompt

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// RuntimeEnvDeps 是 detectRuntimeEnvBlock 的可注入依赖。
//
// TS 侧这两个依赖本就是可注入的（测试传 fake probe），故此处参数化后
// 探测逻辑成为纯函数——可逐字节对账。
type RuntimeEnvDeps struct {
	// ReadFile 读 cwd 下的文件。exists=false 对应 TS readIfExists 的 null。
	// **注意**：exists=true 且 content=="" 是"文件存在但为空"，与"不存在"
	// 语义不同——TS 的 `??`（null 合并）与 truthiness 判定对这两者行为不同。
	ReadFile func(name string) (content string, exists bool)
	// Probe 执行版本命令。exists=false 对应 TS 的 null（命令失败/不存在）。
	Probe func(command string) (output string, exists bool)
}

// RealRuntimeEnvDeps 用真实文件系统与子进程构造依赖（生产路径）。
func RealRuntimeEnvDeps(cwd string) RuntimeEnvDeps {
	return RuntimeEnvDeps{
		ReadFile: func(name string) (string, bool) {
			b, err := os.ReadFile(filepath.Join(cwd, name))
			if err != nil {
				return "", false
			}
			return string(b), true
		},
		Probe: func(command string) (string, bool) {
			out, err := exec.Command(command, "--version").Output()
			if err != nil {
				return "", false
			}
			s := strings.TrimSpace(string(out))
			if s == "" {
				return "", false
			}
			return s, true
		},
	}
}

// runtimeLine 对账 TS 的 RuntimeLine。
type runtimeLine struct {
	name           string
	actual         string
	declared       string
	declaredSource string
}

// TS 侧的字面量与正则（逐一对应）。
var (
	pythonMarkers = []string{"setup.py", "pyproject.toml", "requirements.txt", "setup.cfg", "tox.ini", "Pipfile"}
	rustChannelRe = regexp.MustCompile(`channel\s*=\s*["']([^"']+)["']`)
	goVersionRe   = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)`)
	versionNumRe  = regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?)`)
	// isDated 的版本解析——**要求 major.minor 形态**（含小数点）。
	// 故 declared "16"（无点）不匹配、不判为 dated。这是 TS 的真实行为。
	datedVersionRe = regexp.MustCompile(`(\d+)\.(\d+)`)
)

// DetectRuntimeEnvBlock 复刻 runtime-env.ts:171 的 detectRuntimeEnvBlock。
//
// 探测四种运行时（python / node / rust / go），拼接为 <runtime-env> 块。
// 全部不成立时返回 ""（对应 TS 的 null）。
//
// 顺序固定为 python → node → rust → go（对账 buildBlock）。
func DetectRuntimeEnvBlock(deps RuntimeEnvDeps) string {
	lines := []runtimeLine{}
	if l, ok := detectPython(deps); ok {
		lines = append(lines, l)
	}
	if l, ok := detectNode(deps); ok {
		lines = append(lines, l)
	}
	if l, ok := detectRust(deps); ok {
		lines = append(lines, l)
	}
	if l, ok := detectGo(deps); ok {
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		return ""
	}

	rendered := make([]string, len(lines))
	for i, l := range lines {
		rendered[i] = renderRuntimeLine(l)
	}
	datedNames := []string{}
	for _, l := range lines {
		if isDated(l) {
			datedNames = append(datedNames, l.name)
		}
	}
	caution := ""
	if len(datedNames) > 0 {
		caution = "\n注意：目标环境 " + strings.Join(datedNames, "/") +
			" 低于当前常识版本。版本敏感构造（enum 类属性、typing/match 语法、walrus、可选链等）以上述版本为准，动手前确认目标版本支持，不凭训练常识假设。"
	}
	return "<runtime-env>\n" + strings.Join(rendered, "\n") + caution + "\n</runtime-env>"
}

// truthy 对账 JS 的字符串 truthiness（"" 与不存在均为 false）。
func truthy(s string, exists bool) bool { return exists && s != "" }

func detectPython(deps RuntimeEnvDeps) (runtimeLine, bool) {
	hasProject := false
	for _, m := range pythonMarkers {
		if _, ok := deps.ReadFile(m); ok {
			hasProject = true
			break
		}
	}
	pinned, pinnedExists := deps.ReadFile(".python-version")
	if !hasProject && !truthy(pinned, pinnedExists) {
		return runtimeLine{}, false
	}

	line := runtimeLine{name: "python"}
	if truthy(pinned, pinnedExists) {
		line.declared = firstLine(pinned)
		line.declaredSource = ".python-version"
	} else {
		// python_requires（setup.py / setup.cfg）、requires-python（pyproject.toml）
		type src struct {
			file string
			re   *regexp.Regexp
		}
		for _, s := range []src{
			{"setup.py", regexp.MustCompile(`python_requires\s*=\s*["']([^"']+)["']`)},
			{"setup.cfg", regexp.MustCompile(`python_requires\s*=\s*(\S+)`)},
			{"pyproject.toml", regexp.MustCompile(`requires-python\s*=\s*["']([^"']+)["']`)},
		} {
			content, ok := deps.ReadFile(s.file)
			if !ok {
				continue
			}
			if m := s.re.FindStringSubmatch(content); m != nil {
				line.declared = m[1]
				line.declaredSource = s.file
				break
			}
		}
	}

	// probed = probe('python3') ?? probe('python')
	probed := ""
	if out, ok := deps.Probe("python3"); ok {
		probed = out
	} else if out, ok := deps.Probe("python"); ok {
		probed = out
	}
	if probed != "" {
		if v := extractVersionNumber(probed); v != "" {
			line.actual = v
		}
	}
	if line.actual == "" && line.declared == "" {
		return runtimeLine{}, false
	}
	return line, true
}

func detectNode(deps RuntimeEnvDeps) (runtimeLine, bool) {
	pkgRaw, pkgExists := deps.ReadFile("package.json")
	nvmrc, nvmrcExists := deps.ReadFile(".nvmrc")
	if !pkgExists && !nvmrcExists {
		return runtimeLine{}, false
	}

	line := runtimeLine{name: "node"}
	if pkgExists {
		// JSON.parse 失败则忽略（TS 的 catch 分支）
		var pkg struct {
			Engines struct {
				Node string `json:"node"`
			} `json:"engines"`
		}
		if err := json.Unmarshal([]byte(pkgRaw), &pkg); err == nil && pkg.Engines.Node != "" {
			line.declared = pkg.Engines.Node
			line.declaredSource = "package.json engines"
		}
	}
	if line.declared == "" && nvmrcExists {
		line.declared = firstLine(nvmrc)
		line.declaredSource = ".nvmrc"
	}
	if out, ok := deps.Probe("node"); ok {
		if v := extractVersionNumber(out); v != "" {
			line.actual = v
		}
	}
	if line.actual == "" && line.declared == "" {
		return runtimeLine{}, false
	}
	return line, true
}

func detectRust(deps RuntimeEnvDeps) (runtimeLine, bool) {
	// TS: readIfExists('rust-toolchain') ?? readIfExists('rust-toolchain.toml')
	// **注意**：?? 是 null 合并（不是 truthiness）——文件存在但为空时取空串，
	// 不会回退到 .toml。这是与 detectPython 的 pinned 判定不同之处。
	toolchain, ok := deps.ReadFile("rust-toolchain")
	if !ok {
		toolchain, ok = deps.ReadFile("rust-toolchain.toml")
	}
	if !ok {
		return runtimeLine{}, false
	}
	declared := ""
	if m := rustChannelRe.FindStringSubmatch(toolchain); m != nil {
		declared = m[1]
	} else {
		declared = firstLine(toolchain)
	}
	if declared == "" {
		return runtimeLine{}, false
	}
	return runtimeLine{name: "rust", declared: declared, declaredSource: "rust-toolchain"}, true
}

func detectGo(deps RuntimeEnvDeps) (runtimeLine, bool) {
	goMod, ok := deps.ReadFile("go.mod")
	if !ok {
		return runtimeLine{}, false
	}
	m := goVersionRe.FindStringSubmatch(goMod)
	if m == nil {
		return runtimeLine{}, false
	}
	return runtimeLine{name: "go", declared: m[1], declaredSource: "go.mod"}, true
}

// renderRuntimeLine 对账 TS 的 renderLine。
func renderRuntimeLine(l runtimeLine) string {
	parts := []string{l.name + ":"}
	if l.actual != "" {
		parts = append(parts, l.actual)
	}
	if l.declared != "" {
		if l.actual != "" {
			parts = append(parts, "(declared "+l.declared+" via "+l.declaredSource+")")
		} else {
			parts = append(parts, "declared "+l.declared+" via "+l.declaredSource)
		}
	}
	return strings.Join(parts, " ")
}

// isDated 对账 TS 的 isDated —— 版本敏感提醒。
//
// **关键细节**：版本解析用 /(\d+)\.(\d+)/，要求 major.minor 形态（含小数点）。
// 故 declared "16"（无点）不匹配 → 不判为 dated；而 actual "16.20.0" 匹配 → dated。
func isDated(l runtimeLine) bool {
	v := l.actual
	if v == "" {
		v = l.declared
	}
	if v == "" {
		return false
	}
	m := datedVersionRe.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	major := atoiSafe(m[1])
	minor := atoiSafe(m[2])
	if l.name == "python" {
		return major < 3 || (major == 3 && minor < 9)
	}
	if l.name == "node" {
		return major < 18
	}
	return false
}

// firstLine 对账 TS 的 firstLine（首行 trim）。
func firstLine(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return ""
	}
	return trimSpaceUnicode(lines[0])
}

// extractVersionNumber 对账 TS 的 extractVersionNumber。
func extractVersionNumber(raw string) string {
	m := versionNumRe.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return m[1]
}

// atoiSafe 解析十进制（非数字返回 0）。
func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

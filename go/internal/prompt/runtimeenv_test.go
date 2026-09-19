package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// runtimeEnvOracle 是 TS 侧真实 detectRuntimeEnvBlock 的产出。
// 生成命令：npx tsx go/testdata/runtimeenv/gen-oracle.ts
type runtimeEnvOracle struct {
	Cases map[string]struct {
		Note  string            `json:"note"`
		Files map[string]string `json:"files"`
		Probe map[string]string `json:"probe"`
		Out   *string           `json:"out"`
	} `json:"cases"`
}

func loadRuntimeEnvOracle(t *testing.T) runtimeEnvOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "runtimeenv", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/runtimeenv/gen-oracle.ts", path, err)
	}
	var o runtimeEnvOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestDetectRuntimeEnvBlockParity —— 运行时环境探测与 TS 逐字节相同。
//
// 用 oracle 的 files/probe 构造注入依赖（不碰真实文件系统与子进程），
// 使对账不依赖宿主真实环境（python3/node 版本会变）。
func TestDetectRuntimeEnvBlockParity(t *testing.T) {
	o := loadRuntimeEnvOracle(t)
	if len(o.Cases) == 0 {
		t.Fatal("oracle 无用例")
	}

	for name, c := range o.Cases {
		t.Run(name, func(t *testing.T) {
			deps := RuntimeEnvDeps{
				ReadFile: func(n string) (string, bool) {
					v, ok := c.Files[n]
					return v, ok
				},
				Probe: func(cmd string) (string, bool) {
					v, ok := c.Probe[cmd]
					return v, ok
				},
			}
			got := DetectRuntimeEnvBlock(deps)

			want := ""
			if c.Out != nil {
				want = *c.Out
			}
			if got != want {
				t.Errorf("不等价\n  Go =%q\n  TS =%q", got, want)
			}
		})
	}
}

// TestRuntimeEnvPythonBranches —— python 检测的四条来源分支。
func TestRuntimeEnvPythonBranches(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]string
		probe    map[string]string
		wantPart string
		wantNil  bool
	}{
		{"pinnedOnly", map[string]string{".python-version": "3.11.0\n"}, nil, "declared 3.11.0 via .python-version", false},
		{"setupPy", map[string]string{"setup.py": "python_requires='>=3.5'"}, nil, "declared >=3.5 via setup.py", false},
		{"setupCfg", map[string]string{"setup.cfg": "python_requires = >=3.8"}, nil, "declared >=3.8 via setup.cfg", false},
		{"pyproject", map[string]string{"pyproject.toml": `requires-python = ">=3.11"`}, nil, "declared >=3.11 via pyproject.toml", false},
		{"markerNoVersion", map[string]string{"requirements.txt": "x"}, nil, "", true},
		{"probedWins", map[string]string{".python-version": "3.6.9\n"}, map[string]string{"python3": "Python 3.6.9"}, "python: 3.6.9", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps := RuntimeEnvDeps{
				ReadFile: func(n string) (string, bool) { v, ok := c.files[n]; return v, ok },
				Probe:    func(cmd string) (string, bool) { v, ok := c.probe[cmd]; return v, ok },
			}
			got := DetectRuntimeEnvBlock(deps)
			if c.wantNil {
				if got != "" {
					t.Errorf("应返回空，得到 %q", got)
				}
				return
			}
			if !contains(got, c.wantPart) {
				t.Errorf("应含 %q，实际 %q", c.wantPart, got)
			}
		})
	}
}

// TestRuntimeEnvRustNullCoalescing —— rust 的 `??` 是 null 合并而非 truthiness。
//
// 文件存在但为空时取空串，**不**回退到 .toml。这与 python 的 pinned 判定
// （用 truthiness）不同——是易错点。
func TestRuntimeEnvRustNullCoalescing(t *testing.T) {
	// rust-toolchain 存在但为空 + rust-toolchain.toml 有内容
	files := map[string]string{
		"rust-toolchain":      "",
		"rust-toolchain.toml": `channel = "1.75.0"`,
	}
	deps := RuntimeEnvDeps{
		ReadFile: func(n string) (string, bool) { v, ok := files[n]; return v, ok },
		Probe:    func(string) (string, bool) { return "", false },
	}
	got := DetectRuntimeEnvBlock(deps)
	// 空文件 → declared 为 ""（firstLine("")）→ 返回空块，**不**读 .toml
	if got != "" {
		t.Errorf("rust-toolchain 存在但为空时应返回空（null 合并语义），实际 %q", got)
	}
}

// TestRuntimeEnvDatedCaution —— isDated 的版本解析要求 major.minor 形态。
//
// 故 declared "16"（无小数点）不判为 dated，而 actual "16.20.0" 会。
func TestRuntimeEnvDatedCaution(t *testing.T) {
	// 场景 1：node declared "16"（无点）→ 不 dated → 无 caution
	deps1 := RuntimeEnvDeps{
		ReadFile: func(n string) (string, bool) {
			if n == "package.json" {
				return `{"engines":{"node":"16"}}`, true
			}
			return "", false
		},
		Probe: func(string) (string, bool) { return "", false },
	}
	if got := DetectRuntimeEnvBlock(deps1); contains(got, "注意：") {
		t.Errorf("declared \"16\" 无小数点不应判为 dated，实际 %q", got)
	}

	// 场景 2：node actual "16.20.0" → dated → 有 caution
	deps2 := RuntimeEnvDeps{
		ReadFile: func(n string) (string, bool) {
			if n == "package.json" {
				return `{"engines":{"node":"16"}}`, true
			}
			return "", false
		},
		Probe: func(cmd string) (string, bool) {
			if cmd == "node" {
				return "v16.20.0", true
			}
			return "", false
		},
	}
	if got := DetectRuntimeEnvBlock(deps2); !contains(got, "注意：") {
		t.Errorf("actual 16.20.0 应判为 dated，实际 %q", got)
	}
}

// TestRuntimeEnvMalformedPackageJson —— 非法 JSON 应被忽略（不 panic）。
func TestRuntimeEnvMalformedPackageJson(t *testing.T) {
	deps := RuntimeEnvDeps{
		ReadFile: func(n string) (string, bool) {
			if n == "package.json" {
				return "{ not json", true
			}
			return "", false
		},
		Probe: func(string) (string, bool) { return "", false },
	}
	if got := DetectRuntimeEnvBlock(deps); got != "" {
		t.Errorf("非法 package.json 应忽略并返回空，实际 %q", got)
	}
}

// TestRuntimeEnvOrder —— 四种运行时的顺序固定为 python → node → rust → go。
func TestRuntimeEnvOrder(t *testing.T) {
	files := map[string]string{
		"go.mod":              "module x\n\ngo 1.22\n",
		"rust-toolchain.toml": `channel = "1.78.0"`,
		"package.json":        `{"engines":{"node":">=20"}}`,
		".python-version":     "3.12.0\n",
	}
	deps := RuntimeEnvDeps{
		ReadFile: func(n string) (string, bool) { v, ok := files[n]; return v, ok },
		Probe:    func(string) (string, bool) { return "", false },
	}
	got := DetectRuntimeEnvBlock(deps)

	idxPython := indexOf(got, "python:")
	idxNode := indexOf(got, "node:")
	idxRust := indexOf(got, "rust:")
	idxGo := indexOf(got, "go:")
	if idxPython < 0 || idxNode < 0 || idxRust < 0 || idxGo < 0 {
		t.Fatalf("四种运行时都应出现，实际 %q", got)
	}
	if !(idxPython < idxNode && idxNode < idxRust && idxRust < idxGo) {
		t.Errorf("顺序应为 python→node→rust→go，实际位置 %d/%d/%d/%d", idxPython, idxNode, idxRust, idxGo)
	}
}

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// gittool_oracle_test.go —— commit-audit / sensitive-file-detector 的差分对账。
//
// oracle 由 `testdata/gittool/gen-oracle.ts` 真跑 TS 产出。

const gittoolOraclePath = "../../testdata/gittool/oracle.json"

type gittoolOracle struct {
	ExtractTaskTags []struct {
		Message string   `json:"message"`
		Tags    []string `json:"tags"`
	} `json:"extractTaskTags"`
	AuditCommitTagScope []struct {
		Message   string            `json:"message"`
		FileCount int               `json:"fileCount"`
		Result    CommitAuditResult `json:"result"`
	} `json:"auditCommitTagScope"`
	DetectSensitiveFile []struct {
		Path        string  `json:"path"`
		Sensitive   bool    `json:"sensitive"`
		PatternName *string `json:"patternName"`
	} `json:"detectSensitiveFile"`
	DetectSensitiveGitAdd []struct {
		Command string   `json:"command"`
		Files   []string `json:"files"`
	} `json:"detectSensitiveGitAdd"`
	AggregateMarker string `json:"aggregateMarker"`
}

func loadGittoolOracle(t *testing.T) *gittoolOracle {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(gittoolOraclePath))
	if err != nil {
		t.Fatalf("读取 oracle 失败（需先跑 gen-oracle.ts）：%v", err)
	}
	var o gittoolOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return &o
}

// TestOracleExtractTaskTags —— 任务标签提取逐例对账。
func TestOracleExtractTaskTags(t *testing.T) {
	o := loadGittoolOracle(t)
	if len(o.ExtractTaskTags) == 0 {
		t.Fatal("oracle 无 extractTaskTags 用例")
	}
	for _, c := range o.ExtractTaskTags {
		got := ExtractTaskTags(c.Message)
		if len(got) != len(c.Tags) {
			t.Errorf("ExtractTaskTags(%q)\n  TS: %v\n  Go: %v", c.Message, c.Tags, got)
			continue
		}
		for i := range got {
			if got[i] != c.Tags[i] {
				t.Errorf("ExtractTaskTags(%q)\n  TS: %v\n  Go: %v", c.Message, c.Tags, got)
				break
			}
		}
	}
	t.Logf("extractTaskTags 对账 %d 例", len(o.ExtractTaskTags))
}

// TestOracleAuditCommitTagScope —— 标签审计逐例对账（含警告文案）。
func TestOracleAuditCommitTagScope(t *testing.T) {
	o := loadGittoolOracle(t)
	if len(o.AuditCommitTagScope) == 0 {
		t.Fatal("oracle 无 auditCommitTagScope 用例")
	}
	for _, c := range o.AuditCommitTagScope {
		files := make([]string, c.FileCount)
		for i := range files {
			files[i] = "f" + itoaPlain(i) + ".ts"
		}
		got := AuditCommitTagScope(c.Message, files)
		if got.OK != c.Result.OK {
			t.Errorf("audit(%q, %d files) OK TS=%v Go=%v", c.Message, c.FileCount, c.Result.OK, got.OK)
		}
		if got.Message != c.Result.Message {
			t.Errorf("audit(%q, %d files) message\n  TS: %q\n  Go: %q", c.Message, c.FileCount, c.Result.Message, got.Message)
		}
	}
	t.Logf("auditCommitTagScope 对账 %d 例", len(o.AuditCommitTagScope))
}

// TestOracleDetectSensitiveFile —— 敏感文件检测逐例对账（模式 + 白名单）。
func TestOracleDetectSensitiveFile(t *testing.T) {
	o := loadGittoolOracle(t)
	if len(o.DetectSensitiveFile) == 0 {
		t.Fatal("oracle 无 detectSensitiveFile 用例")
	}
	for _, c := range o.DetectSensitiveFile {
		got := DetectSensitiveFile(c.Path)
		if got.Sensitive != c.Sensitive {
			t.Errorf("DetectSensitiveFile(%q) sensitive TS=%v Go=%v", c.Path, c.Sensitive, got.Sensitive)
			continue
		}
		wantName := ""
		if c.PatternName != nil {
			wantName = *c.PatternName
		}
		if got.PatternName != wantName {
			t.Errorf("DetectSensitiveFile(%q) patternName TS=%q Go=%q", c.Path, wantName, got.PatternName)
		}
	}
	t.Logf("detectSensitiveFile 对账 %d 例", len(o.DetectSensitiveFile))
}

// TestOracleDetectSensitiveGitAdd —— 命令文本检测逐例对账。
func TestOracleDetectSensitiveGitAdd(t *testing.T) {
	o := loadGittoolOracle(t)
	if len(o.DetectSensitiveGitAdd) == 0 {
		t.Fatal("oracle 无 detectSensitiveGitAdd 用例")
	}
	for _, c := range o.DetectSensitiveGitAdd {
		got := DetectSensitiveGitAdd(c.Command)
		if len(got) != len(c.Files) {
			t.Errorf("DetectSensitiveGitAdd(%q)\n  TS: %v\n  Go: %v", c.Command, c.Files, got)
			continue
		}
		for i := range got {
			if got[i] != c.Files[i] {
				t.Errorf("DetectSensitiveGitAdd(%q)\n  TS: %v\n  Go: %v", c.Command, c.Files, got)
				break
			}
		}
	}
	t.Logf("detectSensitiveGitAdd 对账 %d 例", len(o.DetectSensitiveGitAdd))
}

// TestAggregateMarkerMatches —— 聚合哨兵字面量必须一致。
func TestAggregateMarkerMatches(t *testing.T) {
	o := loadGittoolOracle(t)
	if AggregateAddMarker != o.AggregateMarker {
		t.Errorf("哨兵 TS=%q Go=%q", o.AggregateMarker, AggregateAddMarker)
	}
}

// ── 反证（防过度拦截 / 防漏拦）──

// TestSensitiveWhitelistTakesPriority —— **白名单优先**：`.env.example` 放行。
//
// 对账 TS 注释：「白名单优先。归一化与剥尾两种形态都放行」。
func TestSensitiveWhitelistTakesPriority(t *testing.T) {
	allowed := []string{
		".env.example", ".env.template", ".env.sample",
		"src/foo.test.ts", "a/bar.spec.tsx",
		"README.md", "docs/security.md",
	}
	for _, p := range allowed {
		if DetectSensitiveFile(p).Sensitive {
			t.Errorf("%q 应被白名单放行", p)
		}
	}
}

// TestSensitiveNoOverBlockSourceFiles —— **反证**：合法源码不得被拦。
//
// 对账 TS：`secret.ts` / `token.js` / `credentials.ts` / `auth/token-manager.ts`
// 都是源码，**不拦**（只匹配数据文件扩展名）。
func TestSensitiveNoOverBlockSourceFiles(t *testing.T) {
	allowed := []string{
		"secret.ts", "token.js", "credentials.ts", "auth/token-manager.ts",
		"src/index.ts", "package.json", "normal.txt",
	}
	for _, p := range allowed {
		if DetectSensitiveFile(p).Sensitive {
			t.Errorf("%q 是源码/普通文件，不应被拦", p)
		}
	}
}

// TestSensitiveTrailingArtifacts —— 尾部修饰（`/`、`.`、空格）等同无修饰。
//
// 对账 TS `stripTrailingArtifacts`：Win32 打开文件时自动剥掉尾部点与空格。
func TestSensitiveTrailingArtifacts(t *testing.T) {
	for _, p := range []string{".env/", ".env.", ".env ", ".env\t"} {
		if !DetectSensitiveFile(p).Sensitive {
			t.Errorf("%q 应等同 .env（尾部修饰不影响匹配）", p)
		}
	}
}

// TestSensitiveCaseInsensitive —— 大小写不敏感。
func TestSensitiveCaseInsensitive(t *testing.T) {
	for _, p := range []string{".ENV", "CREDENTIALS.JSON", "ID_RSA", "Cert.PEM"} {
		if !DetectSensitiveFile(p).Sensitive {
			t.Errorf("%q 应命中（大小写不敏感）", p)
		}
	}
}

// TestGitAddAggregateFailClosed —— 聚合形态（`.`/`-A`/`--all`）返回哨兵。
//
// 对账 TS：无法静态枚举缓存的文件 → fail-closed 交由调用方处置。
func TestGitAddAggregateFailClosed(t *testing.T) {
	for _, cmd := range []string{"git add .", "git add -A", "git add --all"} {
		got := DetectSensitiveGitAdd(cmd)
		found := false
		for _, f := range got {
			if f == AggregateAddMarker {
				found = true
			}
		}
		if !found {
			t.Errorf("%q 应返回聚合哨兵，实得 %v", cmd, got)
		}
	}
}

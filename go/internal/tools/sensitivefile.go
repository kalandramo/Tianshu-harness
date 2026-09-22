package tools

import (
	"regexp"
	"unicode/utf8"
)

// sensitivefile.go —— 敏感文件检测（fail-closed 工具层拦截）。
//
// 对账 TS `src/tools/sensitive-file-detector.ts`（187 行）。
//
// # 与 `pathsafe` 既有实现的关系（**重要**）
//
// Go 侧 `pathsafe.go` **已有** `defaultSensitiveDetector` + `sensitivePatterns`
// ——但那是**子串匹配**（`my_secret.txt`、`api_token.txt`、`credentials-guide.md`
// 都命中，注释说「宁可误报也不放过」）。而 TS 的实现是**精确正则 + 白名单**：
//
//   - `.env.example` / `.env.template` / `.env.sample` → **放行**（模板无真凭证）
//   - `*.md` / `*.test.ts` / `*.spec.ts` → **放行**（文档与测试源码）
//   - `secret.json` 命中，但 `secret.ts` **不命中**（只匹配数据文件扩展名）
//
// 两者语义**不同**，不可互相替代。本文件实现 **TS 语义**供 `git commit` 的
// 暂存门禁使用（TS 的 `git.ts` 正是 import 本模块）。
//
// `pathsafe` 的版本继续服务于路径校验（读/写拦截），其更宽的子串匹配在那里
// 是**有意的**（读 `.env` 必须拦）。两套并存，各服务其调用点。

// SensitiveFileResult 是检测结果。
//
// 对账 TS `SensitiveFileResult`（sensitive-file-detector.ts:105-109）。
type SensitiveFileResult struct {
	Sensitive   bool
	PatternName string
	Path        string
}

// sensitiveFilePattern 是「模式名 + 正则」对。
//
// 对账 TS `SENSITIVE_FILE_PATTERNS`（sensitive-file-detector.ts:36-99）。
type sensitiveFilePattern struct {
	name string
	re   *regexp.Regexp
}

// sensitiveFilePatterns 是敏感文件名模式表。
//
// **顺序逐字对账 TS**——匹配是首个命中即返回，顺序影响 `patternName`。
var sensitiveFilePatterns = []sensitiveFilePattern{
	// .env（但不是 .env.example/.template/.sample）
	// 匹配 `.env` 本身与 `.env.local` / `.env.production` 等变体
	{".env (real)", regexp.MustCompile(`\.env(?:\.(?:local|production|staging|development|prod|dev))?$`)},
	// credentials 文件（云服务凭证）
	{"credentials file", regexp.MustCompile(`(^|/)credentials\.(?:json|yaml|yml|xml|ini|conf)$`)},
	// SSH 私钥命名约定
	{"SSH private key", regexp.MustCompile(`(^|/)id_(?:rsa|ed25519|ecdsa|dsa)$`)},
	// TLS/SSL 私钥通用扩展名
	{"PKI private key (.pem/.key)", regexp.MustCompile(`\.(?:pem|key)$`)},
	// npm/PyPI 凭证文件
	{"package manager credentials", regexp.MustCompile(`(^|/)(?:\.npmrc|\.pypirc)$`)},
	// 通用 secret/token 文件名。
	//
	// **注意**：只匹配 `.json/.yaml/.yml/.ini/.env` 扩展名，**不匹配 `.ts`/`.js`**
	// ——合法源码（`auth/token-manager.ts`）不被拦截。
	{"secrets/token file", regexp.MustCompile(`(^|/)(?:secret[s]?|token[s]?|auth[_-]?token[s]?)\.(?:json|yaml|yml|ini|env)$`)},
	// 无扩展名 credentials（basename 精确匹配）——~/.cargo/credentials 等。
	// **带扩展名的源码（credentials.ts）不受影响**。
	{"credentials (extensionless)", regexp.MustCompile(`(^|/)credentials$`)},
	// .netrc（FTP/HTTP 明文凭证）/ .git-credentials（明文 PAT）
	{".netrc / .git-credentials", regexp.MustCompile(`(^|/)(?:\.netrc|\.git-credentials)$`)},
	// Android debug.keystore——APK 签名密钥
	{"Android debug keystore", regexp.MustCompile(`(^|/)debug\.keystore$`)},
}

// sensitiveWhitelistPatterns 是白名单——即使匹配敏感模式也不拦截。
//
// 对账 TS `WHITELIST_PATTERNS`（sensitive-file-detector.ts:102-110）。
var sensitiveWhitelistPatterns = []*regexp.Regexp{
	// .env 模板文件（无真实凭证）
	regexp.MustCompile(`\.env\.(?:example|template|sample)$`),
	// 测试文件
	regexp.MustCompile(`\.(?:test|spec)\.(?:ts|tsx|js|jsx)$`),
	// 文档
	regexp.MustCompile(`\.md$`),
}

// normalizeForMatch 归一化：反斜杠→正斜杠、小写化。
//
// 对账 TS `normalizeForMatch`（sensitive-file-detector.ts:112-114）。
func normalizeForMatch(inputPath string) string {
	out := make([]byte, 0, len(inputPath))
	for i := 0; i < len(inputPath); i++ {
		c := inputPath[i]
		if c == '\\' {
			c = '/'
		} else if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

// stripTrailingArtifacts 剥尾部空白/点/分隔符。
//
// 对账 TS `stripTrailingArtifacts`（sensitive-file-detector.ts:117-119）：
// `p.replace(/[\s./]+$/, ”)`——`.env/`、`.env.`、`.env ` 都是 `.env` 的
// 可寻址形态（Win32 打开文件时自动剥掉尾部点与空格）。**只影响匹配**。
func stripTrailingArtifacts(p string) string {
	end := len(p)
	for end > 0 {
		c := p[end-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '.' || c == '/' {
			end--
			continue
		}
		break
	}
	return p[:end]
}

// DetectSensitiveFile 检测路径是否为敏感文件。
//
// 对账 TS `detectSensitiveFile`（sensitive-file-detector.ts:128-142）。
//
// **白名单优先**：先检查白名单，两种形态（归一化 + 剥尾）都放行——`.env.example`
// 不得因剥尾而失去白名单资格。
func DetectSensitiveFile(inputPath string) SensitiveFileResult {
	normalized := normalizeForMatch(inputPath)
	stripped := stripTrailingArtifacts(normalized)

	for _, re := range sensitiveWhitelistPatterns {
		if re.MatchString(normalized) || re.MatchString(stripped) {
			return SensitiveFileResult{Sensitive: false, Path: inputPath}
		}
	}
	for _, p := range sensitiveFilePatterns {
		if p.re.MatchString(stripped) {
			return SensitiveFileResult{Sensitive: true, PatternName: p.name, Path: inputPath}
		}
	}
	return SensitiveFileResult{Sensitive: false, Path: inputPath}
}

// AggregateAddMarker 是聚合形态哨兵。
//
// 对账 TS `AGGREGATE_ADD_MARKER`（sensitive-file-detector.ts:151）。
//
// `git add .` / `-A` / `--all` 时被暂存的文件无法从命令文本静态枚举，
// 检测器返回该标记项，由调用方决定处置（审批门 / 风险理由）。
const AggregateAddMarker = "__aggregate_add__"

// gitAddRe 匹配 `git add <args>`（大小写不敏感——PowerShell/cmd 命令名不区分）。
//
// 对账 TS `gitAddRe`（sensitive-file-detector.ts:164）：`/git\s+add\s+(.+)/gi`
//
// **`g` 标志 + `exec` 循环** → Go 用 `FindAllStringSubmatch`。
// **`i` 标志** → 加 `(?i)`。
// **`.` 不匹配换行**（JS 默认）→ Go 的 `.` 同样不匹配 `\n`，语义一致。
var gitAddRe = regexp.MustCompile(`(?i)git\s+add\s+(.+)`)

// DetectSensitiveGitAdd 检测 bash 命令文本里的 `git add` 是否含敏感文件。
//
// 对账 TS `detectSensitiveGitAdd`（sensitive-file-detector.ts:164-186）。
//
// 返回匹配到的敏感文件名数组 + 可能的聚合哨兵（可能为空）。
func DetectSensitiveGitAdd(command string) []string {
	var sensitiveFiles []string
	sawAggregate := false

	for _, m := range gitAddRe.FindAllStringSubmatch(command, -1) {
		args := trimSpaceJS(m[1])
		files := jsWhitespaceSplit(args)
		for _, f := range files {
			if f == "." || f == "./" {
				sawAggregate = true
				continue
			}
			if f == "-A" || f == "-a" || f == "--all" {
				sawAggregate = true
				continue
			}
			if len(f) > 0 && f[0] == '-' {
				continue
			}
			if DetectSensitiveFile(f).Sensitive {
				sensitiveFiles = append(sensitiveFiles, f)
			}
		}
	}

	if sawAggregate {
		sensitiveFiles = append(sensitiveFiles, AggregateAddMarker)
	}
	return sensitiveFiles
}

// trimSpaceJS 复刻 JS 的 `String.prototype.trim()`。
//
// JS 的 trim 去的是 Unicode 空白（含 `\v`、`\f`、NBSP、U+FEFF 等）。
func trimSpaceJS(s string) string {
	return trimUnicodeSpace(s)
}

// jsWhitespaceSplit 复刻 JS 的 `args.split(/\s+/)`。
//
// **与 `strings.Fields` 的差异**：`\s` 在 JS 里含 `\v`/`\f`/NBSP/U+FEFF；
// `strings.Fields` 按 `unicode.IsSpace`（不含 NBSP/U+FEFF）。此处显式处理。
func jsWhitespaceSplit(s string) []string {
	var out []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		if isJSWhitespace(r) {
			flush()
			continue
		}
		cur = append(cur, []byte(string(r))...)
	}
	flush()
	return out
}

// isJSWhitespace 报告 rune 是否为 JS 正则 `\s` 的成员。
//
// JS `\s` = `[\f\n\r\t\v\u0020\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff]`
func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// trimUnicodeSpace 去首尾的 JS 空白。
func trimUnicodeSpace(s string) string {
	start := 0
	for start < len(s) {
		r, size := utf8.DecodeRuneInString(s[start:])
		if !isJSWhitespace(r) {
			break
		}
		start += size
	}
	end := len(s)
	for end > start {
		r, size := utf8.DecodeLastRuneInString(s[start:end])
		if !isJSWhitespace(r) {
			break
		}
		end -= size
	}
	return s[start:end]
}

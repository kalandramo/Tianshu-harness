// Package trust 提供项目级配置/hooks 的信任门。
//
// 对账 src/config/project-trust.ts。**SECURITY.md 的信任边界**：仓库内容
// （含项目内 `.rivet/hooks.json` 与 `.rivet-config.json`）**不能单独构成
// 执行动作的授权**。项目在用户显式授信（TUI `/trust`、CLI `--trust` 或
// `RIVET_TRUST_PROJECT=1`）之前：
//
//   - 项目级 hooks **不执行**
//   - 项目级配置中的**安全敏感键被剥离**——fail-closed
//
// 授信决策持久化在 `<rivetHome>/project-trust.json`（按 **realpath** 键控，
// **永不写进仓库目录**——否则克隆一个仓库就等于授信它）。
package trust

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// envOverride 是环境变量覆盖名。
//
// 优先级**高于**信任文件：'1' 视为已授信（CI/无头场景），'0' 强制未授信
// （审计）。其他值忽略，回落文件判定。
const envOverride = "RIVET_TRUST_PROJECT"

// ProjectConfigFileName 是项目配置文件名。
const ProjectConfigFileName = ".rivet-config.json"

// Store 是信任存储。
type Store struct {
	// Trusted 是 realpath(项目目录) → 授信时间（ISO 字符串）。
	Trusted map[string]string `json:"trusted"`
	// Dismissed 是 realpath(项目目录) → 关闭启动授信提示的时间。
	Dismissed map[string]string `json:"dismissed"`
}

// RivetHome 返回数据根目录。
//
// 对账 rivetHome：`RIVET_HOME` 优先，否则 `~/.rivet`。
func RivetHome() string {
	if h := os.Getenv("RIVET_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".rivet"
	}
	return filepath.Join(home, ".rivet")
}

// storePath 返回信任文件路径。
func storePath() string {
	return filepath.Join(RivetHome(), "project-trust.json")
}

// canonicalProjectDir 返回项目的规范化目录（realpath）。
//
// **为什么必须 realpath**：符号链接会让同一物理目录有多个路径——不归一化
// 就存在「授信 A 路径、B 路径绕过」的漏洞。realpath 失败时回落 `resolve`。
func canonicalProjectDir(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// readStore 读信任文件。缺失/坏文件按**未授信**处理——fail-closed。
func readStore() Store {
	raw, err := os.ReadFile(storePath())
	if err != nil {
		return Store{Trusted: map[string]string{}, Dismissed: map[string]string{}}
	}
	var s Store
	if err := json.Unmarshal(raw, &s); err != nil {
		return Store{Trusted: map[string]string{}, Dismissed: map[string]string{}}
	}
	if s.Trusted == nil {
		s.Trusted = map[string]string{}
	}
	if s.Dismissed == nil {
		s.Dismissed = map[string]string{}
	}
	return s
}

// writeStore 原子写信任文件（0o600——用户私有）。
//
// 对账 writeFileAtomicSync：临时文件 + rename（rename 保留 mode）。
func writeStore(s Store) error {
	dir := RivetHome()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := storePath() + ".rivet-atomic-" + randHex8() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, storePath()); err != nil {
		_ = os.Remove(tmp) // 清理失败忽略
		return err
	}
	return nil
}

// randHex8 生成 8 位十六进制随机串（对账 randomUUID().slice(0, 8)）。
func randHex8() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// IsProjectTrusted 报告项目目录是否已被用户授信。
//
// 对账 isProjectTrusted：**env 覆盖优先**，其次信任文件。
func IsProjectTrusted(cwd string) bool {
	switch os.Getenv(envOverride) {
	case "1":
		return true
	case "0":
		return false
	}
	_, ok := readStore().Trusted[canonicalProjectDir(cwd)]
	return ok
}

// TrustProject 授信当前项目（幂等）。
//
// 对账 trustProject：**同时清除「不再提示」标记**——重新授信即重新参与
// 启动提示语义。
func TrustProject(cwd string) error {
	key := canonicalProjectDir(cwd)
	store := readStore()
	_, hadDismissed := store.Dismissed[key]
	if hadDismissed {
		delete(store.Dismissed, key)
	}
	if _, already := store.Trusted[key]; already {
		if hadDismissed {
			return writeStore(store)
		}
		return nil
	}
	store.Trusted[key] = isoMillis(time.Now())
	return writeStore(store)
}

// UntrustProject 撤销授信（幂等；未授信时为 no-op）。
func UntrustProject(cwd string) error {
	key := canonicalProjectDir(cwd)
	store := readStore()
	if _, ok := store.Trusted[key]; !ok {
		return nil
	}
	delete(store.Trusted, key)
	return writeStore(store)
}

// DismissTrustPrompt 关闭当前项目的启动授信提示（幂等）。
//
// **不授信**——安全键仍被剥离。
func DismissTrustPrompt(cwd string) error {
	key := canonicalProjectDir(cwd)
	store := readStore()
	if _, ok := store.Dismissed[key]; ok {
		return nil
	}
	store.Dismissed[key] = isoMillis(time.Now())
	return writeStore(store)
}

// IsTrustPromptDismissed 报告当前项目是否已关闭启动授信提示。
func IsTrustPromptDismissed(cwd string) bool {
	_, ok := readStore().Dismissed[canonicalProjectDir(cwd)]
	return ok
}

// ListTrustedProjects 列出已授信项目（realpath 数组）。
func ListTrustedProjects() []string {
	store := readStore()
	out := make([]string, 0, len(store.Trusted))
	for k := range store.Trusted {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isoMillis 格式化 ISO 8601（**固定 3 位毫秒**，对账 JS toISOString）。
//
// 与 recovery 包的同类函数同源——Go 的 RFC3339Nano 会省略尾随零。
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// untrustedTopLevelKeys 是未授信时从项目层配置剥离的顶层键。
//
// **安全依据**（对账 TS 注释）：任一键都能把 SECURITY.md 声明的审批/边界/
// 出口控制整体旁路——写盘授权、bash 预授权、静默 YOLO、假 shell、MCP 拉进程、
// baseUrl+key 重定向、statusline 命令执行、verify 声明命令执行、搜索 key 外发、
// 镜像路由安装源、启停已装插件、**MCP 子进程出口改向**、**web_fetch 正文抽取
// 改向**。
// untrustedTopLevelKeyOrder 是顶层敏感键的**声明序**。
//
// oracle 锁定 `findSensitiveProjectKeys` 的输出顺序——Go 的 map 遍历无序，
// 必须按显式序输出才能与 TS 字节一致。
var untrustedTopLevelKeyOrder = []string{
	"permissions", "mcp", "hooks", "env", "provider", "providers", "search",
	"verify", "plugins", "mirrors", "network", "fetch",
}

var untrustedTopLevelKeys = map[string]bool{
	"permissions": true, "mcp": true, "hooks": true, "env": true,
	"provider": true, "providers": true, "search": true, "verify": true,
	"plugins": true, "mirrors": true, "network": true, "fetch": true,
}

// untrustedNestedKeys 是未授信时剥离的嵌套键（点路径相对项目层配置根）。
//
// `agent.permissions` 是 schema 的**真实位置**（allow/deny 规则、bash 预授权
// 白名单、additionalRead/WriteDirs 常驻目录授权都在这里，启动即生效、零审批）；
// 顶层 `permissions` 实际不存在——保留在集合里仅作纵深。
var untrustedNestedKeys = [][2]string{
	{"agent", "approval"},
	{"agent", "unsandboxed"},
	{"agent", "permissions"},
	{"ui", "statusLine"},
	{"skills", "importFromClaude"},
}

// StripUntrustedProjectKeys 返回剥离敏感键后的浅拷贝（原对象不被修改）。
//
// 仅外观/工具选择等**非授权**键保留。
func StripUntrustedProjectKeys(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if untrustedTopLevelKeys[k] {
			continue
		}
		out[k] = v
	}
	for _, pair := range untrustedNestedKeys {
		parent, child := pair[0], pair[1]
		node, ok := out[parent]
		if !ok {
			continue
		}
		m, ok := node.(map[string]any)
		if !ok {
			continue
		}
		clone := make(map[string]any, len(m))
		for k, v := range m {
			if k == child {
				continue
			}
			clone[k] = v
		}
		out[parent] = clone
	}
	return out
}

// FindSensitiveProjectKeys 列出配置中实际存在、未授信时会被剥离的敏感键。
//
// 嵌套键报**点路径**（如 `agent.permissions`）。
func FindSensitiveProjectKeys(raw map[string]any) []string {
	// **顶层键按 TS 的集合声明序**遍历（不是 Go 的 map 随机序，也不是字典序）
	// ——oracle 锁定输出顺序：`permissions, mcp, hooks, env, provider, ...`。
	// TS 用 `Object.keys(raw)` 的插入序，但键集是固定集合，故按声明序等价。
	var found []string
	for _, k := range untrustedTopLevelKeyOrder {
		if _, has := raw[k]; has {
			found = append(found, k)
		}
	}
	// 嵌套键按**声明序**（与 untrustedNestedKeys 同序）
	for _, pair := range untrustedNestedKeys {
		parent, child := pair[0], pair[1]
		node, ok := raw[parent]
		if !ok {
			continue
		}
		m, ok := node.(map[string]any)
		if !ok {
			continue
		}
		if _, has := m[child]; has {
			found = append(found, parent+"."+child)
		}
	}
	return found
}

// ProjectTrustStakes 是启动授信提示的「赌注」检测结果。
type ProjectTrustStakes struct {
	// SensitiveKeys 是配置中实际会被剥离的敏感键（点路径）。
	SensitiveKeys []string
	// HasHooks 报告是否存在项目级 hooks（`.rivet/hooks.json`）。
	HasHooks bool
}

// DetectProjectTrustStakes 检测项目里有没有「未授信就会失效」的东西。
//
// 配置文件读失败/无敏感键且无 hooks → **无赌注**，不该打扰用户。
func DetectProjectTrustStakes(cwd string) ProjectTrustStakes {
	var stakes ProjectTrustStakes
	raw, err := os.ReadFile(filepath.Join(cwd, ProjectConfigFileName))
	if err == nil {
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err == nil {
			stakes.SensitiveKeys = FindSensitiveProjectKeys(parsed)
		}
		// 坏 JSON → 无配置侧赌注（对账 TS 的 catch）
	}
	_, statErr := os.Stat(filepath.Join(cwd, ".rivet", "hooks.json"))
	stakes.HasHooks = statErr == nil
	return stakes
}

// noticed 是单次进程内的提示去重集合。
//
// 对账 TS 的 `noticed`：hooks 每事件读取、config 可能热重载——避免刷屏。
var (
	noticedMu sync.Mutex
	noticed   = map[string]bool{}
)

// NotifyUntrustedOnce 单次进程内提示一次「项目未授信」。
//
// 对账 notifyUntrustedOnce：`kind` 为 "hooks" 或 "config"。
func NotifyUntrustedOnce(kind, projectDir string, strippedKeys []string) {
	key := kind + ":" + projectDir
	noticedMu.Lock()
	if noticed[key] {
		noticedMu.Unlock()
		return
	}
	noticed[key] = true
	noticedMu.Unlock()

	how := "TUI 执行 /trust 授信（或启动加 --trust / 设 RIVET_TRUST_PROJECT=1）"
	keyList := "permissions/mcp/hooks/providers/env/plugins/mirrors/network/fetch/ui.statusLine/agent.approval 等"
	if len(strippedKeys) > 0 {
		keyList = strings.Join(strippedKeys, "/")
	}
	var what string
	if kind == "hooks" {
		what = "检测到项目 hooks（" + filepath.Join(projectDir, ".rivet", "hooks.json") +
			"），项目未授信，已跳过执行"
	} else {
		what = "检测到项目配置（" + filepath.Join(projectDir, ProjectConfigFileName) +
			"），项目未授信，其中安全敏感键（" + keyList + "）已忽略"
	}
	_, _ = os.Stderr.WriteString("[rivet] " + what + "——" + how +
		"。信任决策存于 " + storePath() + "，绝不写回仓库。\n")
}

// ResetNoticedForTests 清空提示去重（测试钩子）。
func ResetNoticedForTests() {
	noticedMu.Lock()
	defer noticedMu.Unlock()
	noticed = map[string]bool{}
}

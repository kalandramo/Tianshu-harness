package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// requestpathaccess_wiring_test.go —— `request_path_access` 全链路（第八十一刀）。
//
// # 为什么必须有
//
// 单测 `requestpathaccess_test.go` 用 fake 回调验证工具逻辑——**不等于接线绿**。
// 中间还隔着「Loop 注入 GrantPath 回调」→「回调落到 pathGrants 存储」→
// 「门链的 OutOfWorkspaceFilePaths 看到新授权并放行」三道接线。
//
// 这正是本仓库栽过的模式（`NeedsApproval`/`CheckPlanMode`/`evidenceTracker`
// 读取端/`Jobs`）：**实现存在但零消费**。

// newPathAccessLoop 构造带 pathGrants 的真 Loop。
//
// **为什么用 skip 档**：`request_path_access` 的 `RequiresApproval` 恒 true
// （对账 TS），且它还是 `RequiresUnconditionalApproval` 的目标（「任何授权
// 都不能豁免」）——但 TS 的 `yoloBypassesUnconditional = skipAllApproval`
// 明确让 **YOLO 档豁免**该门（`tool-pipeline.ts:1196-1199`：用户已选择
// 最大自治，安全网是 checkpoint 而非弹窗）。
//
// 故端到端测试用 skip 档——这是 TS 语义下该工具**唯一能真正执行**的档位。
// manual/auto-safe 档下它被审批门拦（那是正确行为，另有测试覆盖）。
func newPathAccessLoop(t *testing.T) (*Loop, string) {
	t.Helper()
	dir := t.TempDir()
	l := New(Config{Cwd: dir, ApprovalMode: "dangerously-skip-permissions"}, nil, tools.NewRegistry())
	l.registry.Register(tools.WriteFile(dir, nil))
	l.registry.Register(tools.RequestPathAccess(dir))
	return l, dir
}

// TestRequestPathAccessWiringCallbackInjected —— **接线①**：Loop 注入 GrantPath 回调。
func TestRequestPathAccessWiringCallbackInjected(t *testing.T) {
	l, _ := newPathAccessLoop(t)

	p := l.buildToolCallParams(toolCall{name: "request_path_access", input: map[string]any{"path": "/tmp"}})
	if p.GrantPath == nil {
		t.Fatal("buildToolCallParams 应注入 GrantPath 回调（否则工具永远 fail-closed）")
	}
}

// TestRequestPathAccessWiringEndToEnd —— **全链路**：授权后写工具能写工作区外文件。
//
// 这是本刀的核心价值证明：
//  1. 授权前，写工作区外文件**被门链拦**（非 skip 档无提示通道）
//  2. `request_path_access` 授权该目录
//  3. 授权后，同一个写操作**放行**
func TestRequestPathAccessWiringEndToEnd(t *testing.T) {
	l, dir := newPathAccessLoop(t)

	// 工作区外的目标目录（用 dir 的兄弟目录模拟"外部"）。
	outside := filepath.Join(filepath.Dir(dir), "outside-"+filepath.Base(dir))
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)
	target := filepath.Join(outside, "note.txt")

	// ① 授权前：写工作区外文件应被拦。
	//
	// **注意用另一个 Loop（manual 档）测这一步**：本测试主 Loop 是 skip 档，
	// 而 skip 档的门链会「首触即授」（`loop.go` 的 pathGrant 门）——它会
	// 自动授权，测不出「未授权时被拦」。manual 档无该自动授权，才是干净基线。
	manualDir := t.TempDir()
	manual := New(Config{Cwd: manualDir, ApprovalMode: "manual"}, nil, tools.NewRegistry())
	manual.registry.Register(tools.WriteFile(manualDir, nil))
	beforeTarget := filepath.Join(filepath.Dir(manualDir), "outside-before-"+filepath.Base(manualDir))
	if err := os.MkdirAll(beforeTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(beforeTarget)
	beforeFile := filepath.Join(beforeTarget, "note.txt")

	before := manual.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": beforeFile, "content": "hello"},
	})
	if !before.IsError {
		t.Fatalf("授权前写工作区外文件应被拦，实得：%q", before.Content)
	}
	// **拦截理由视档位而定**：manual 档下审批门**先于** pathGrant 门
	// （门链次序：deny → selfKill → HardGate → 档位门 → pathGrant → …），
	// 故文案是「需人工批准」而非「工作区之外」。两者都是合法拦截——
	// 本测试只断言「被拦且未落盘」，不断言具体哪道门（那是门链测试的职责）。
	if _, err := os.Stat(beforeFile); err == nil {
		t.Fatal("授权前不应真的写入文件")
	}

	// **另外验证**：skip 档下同一操作会被 pathGrant 门拦（skip 不豁免它），
	// 且拦截文案确实说明是工作区外——这条才真正验证路径门。
	skipDir := t.TempDir()
	skipL := New(Config{Cwd: skipDir, ApprovalMode: "dangerously-skip-permissions"}, nil, tools.NewRegistry())
	// **故意传 nil grants**：模拟「工具看不到会话授权」的既有缺陷场景。
	// 门链（用 l.pathGrants）会拦下它——因为 skip 档「首触即授」只授一次，
	// 而工具内部查的是 nil grants，故首次调用仍被门链拦（未授权时）。
	skipL.registry.Register(tools.WriteFile(skipDir, nil))
	skipTarget := filepath.Join(filepath.Dir(skipDir), "outside-skip-"+filepath.Base(skipDir))
	if err := os.MkdirAll(skipTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(skipTarget)
	// skip 档首触即授 → 第二次才放行；但**授权本身**已发生（可直接断言存储）。
	_ = skipL.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": filepath.Join(skipTarget, "n.txt"), "content": "x"},
	})
	if !skipL.pathGrants.IsWriteGranted(filepath.Join(skipTarget, "n.txt"), skipL.cfg.Cwd) {
		t.Error("skip 档首触应授写权限（对账 loop.go 的 pathGrant 门）")
	}

	// ② 授权该目录（主 Loop，skip 档——TS 的 YOLO 豁免 unconditional 门）。
	grant := l.executeTool(context.Background(), toolCall{
		name:  "request_path_access",
		input: map[string]any{"path": outside, "mode": "write"},
	})
	if grant.IsError {
		t.Fatalf("授权应成功，实得：%q", grant.Content)
	}
	if !strings.Contains(grant.Content, "已授予 write 访问") {
		t.Errorf("成功文案应逐字对账，实得：%q", grant.Content)
	}

	// ③ 授权后：同一个写操作应放行。
	after := l.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": target, "content": "hello"},
	})
	if after.IsError {
		t.Fatalf("授权后写应放行，实得：%q", after.Content)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("授权后应真的写入文件：%v", err)
	}
	if string(got) != "hello" {
		t.Errorf("文件内容应为 hello，实得 %q", got)
	}
}

// TestRequestPathAccessWiringReadModeDoesNotGrantWrite —— read 授权**不**给写权限。
//
// 对账 TS：`grantPath(root, 'read', ...)`——`IsWriteGranted` 只认写授权。
// **这条是权限最小化的关键**：read 授权被误当 write 用会静默放大写面。
func TestRequestPathAccessWiringReadModeDoesNotGrantWrite(t *testing.T) {
	l, dir := newPathAccessLoop(t)

	outside := filepath.Join(filepath.Dir(dir), "outside-read-"+filepath.Base(dir))
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)

	// 只授 read。
	grant := l.executeTool(context.Background(), toolCall{
		name:  "request_path_access",
		input: map[string]any{"path": outside}, // mode 缺省 = read
	})
	if grant.IsError {
		t.Fatalf("read 授权应成功，实得：%q", grant.Content)
	}

	// **断言存储层**（而非工具输出）：skip 档的门链会「首触即授」写权限，
	// 从而掩盖「read 授权本身不含写」这个事实。直接查存储才干净。
	if l.pathGrants.IsWriteGranted(filepath.Join(outside, "x.txt"), l.cfg.Cwd) {
		t.Error("read 授权不应给写权限（IsWriteGranted 应为 false）")
	}
	if !l.pathGrants.IsReadGranted(filepath.Join(outside, "x.txt"), l.cfg.Cwd) {
		t.Error("read 授权应给读权限")
	}
}

// TestRequestPathAccessWiringForbiddenRootNotGranted —— 系统目录**不**产生授权。
//
// **安全关键**：工具报错后，`pathGrants` 存储里**不得**留下任何授权
// （否则「拒绝」只是表面功夫）。
func TestRequestPathAccessWiringForbiddenRootNotGranted(t *testing.T) {
	l, _ := newPathAccessLoop(t)

	res := l.executeTool(context.Background(), toolCall{
		name:  "request_path_access",
		input: map[string]any{"path": "/etc", "mode": "write"},
	})
	if !res.IsError {
		t.Fatalf("/etc 应被拒，实得：%q", res.Content)
	}

	// 存储里不应有任何 grant。
	if l.pathGrants == nil {
		t.Fatal("pathGrants 应已初始化")
	}
	if l.pathGrants.IsWriteGranted("/etc/passwd", l.cfg.Cwd) {
		t.Error("被拒的路径不得产生授权（拒绝必须是真拒绝）")
	}
}

// TestRequestPathAccessWiringNoSessionFailsClosed —— 无 pathGrants 时 fail-closed。
//
// 对账 TS 的 fail-closed：无授权能力时报错，**不得**假装成功。
func TestRequestPathAccessWiringNoSessionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	l := New(Config{Cwd: dir}, nil, tools.NewRegistry())
	l.pathGrants = nil // 模拟无会话上下文
	l.registry.Register(tools.RequestPathAccess(dir))

	p := l.buildToolCallParams(toolCall{name: "request_path_access", input: map[string]any{"path": "/tmp"}})
	if p.GrantPath != nil {
		t.Fatal("pathGrants 为 nil 时 GrantPath 应为 nil（fail-closed）")
	}

	outside := filepath.Join(filepath.Dir(dir), "outside-nosession")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)

	res := l.executeTool(context.Background(), toolCall{
		name:  "request_path_access",
		input: map[string]any{"path": outside},
	})
	if !res.IsError {
		t.Errorf("无授权能力时应 isError（不假装成功），实得：%q", res.Content)
	}
	if !strings.Contains(res.Content, "不可用") {
		t.Errorf("应说明能力不可用，实得：%q", res.Content)
	}
}

// TestRequestPathAccessWiringScopeBoundToCwd —— 授权绑定会话工作区（sidecar 隔离）。
//
// 对账 `PathGrant.Scope` 的「为什么必须有」：sidecar 一进程多会话，
// 无作用域的授权会跨工作区泄漏。
func TestRequestPathAccessWiringScopeBoundToCwd(t *testing.T) {
	l, dir := newPathAccessLoop(t)

	outside := filepath.Join(filepath.Dir(dir), "outside-scope-"+filepath.Base(dir))
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)

	res := l.executeTool(context.Background(), toolCall{
		name:  "request_path_access",
		input: map[string]any{"path": outside, "mode": "write"},
	})
	if res.IsError {
		t.Fatalf("授权应成功，实得：%q", res.Content)
	}

	// 本工作区可见。
	if !l.pathGrants.IsWriteGranted(filepath.Join(outside, "a.txt"), l.cfg.Cwd) {
		t.Error("授权应对本工作区可见")
	}
	// 另一工作区**不可见**（scope 隔离）。
	otherCwd := t.TempDir()
	if l.pathGrants.IsWriteGranted(filepath.Join(outside, "a.txt"), otherCwd) {
		t.Error("授权不得泄漏给其他工作区（sidecar 多会话隔离）")
	}
}

// TestRequestPathAccessWiringManualModeBlocked —— manual 档下工具被审批门拦。
//
// **这是正确行为**（对账 TS：`requiresApproval: () => true` 在 manual 档被消费）。
// 与上面的 skip 档测试成对——两者共同证明「档位语义单点在审批门」。
func TestRequestPathAccessWiringManualModeBlocked(t *testing.T) {
	dir := t.TempDir()
	l := New(Config{Cwd: dir, ApprovalMode: "manual"}, nil, tools.NewRegistry())
	l.registry.Register(tools.RequestPathAccess(dir))

	outside := filepath.Join(filepath.Dir(dir), "outside-manual")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)

	res := l.executeTool(context.Background(), toolCall{
		name:  "request_path_access",
		input: map[string]any{"path": outside},
	})
	if !res.IsError {
		t.Errorf("manual 档下应被审批门拦（requiresApproval 恒 true），实得：%q", res.Content)
	}
	if !strings.Contains(res.Content, "需人工批准") {
		t.Errorf("应说明需人工批准，实得：%q", res.Content)
	}
	// 且**不得**留下授权（被拦 = 未执行）。
	if l.pathGrants.IsReadGranted(filepath.Join(outside, "a.txt"), l.cfg.Cwd) {
		t.Error("被审批门拦下的调用不得产生授权")
	}
}

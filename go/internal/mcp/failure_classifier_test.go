package mcp

import (
	"errors"
	"strings"
	"testing"
)

// failure_classifier_test.go —— 错误分类（V9）。
//
// 对账 `src/mcp/failure-classifier.ts`：8 类 + `retryable` + `suggestion`。
//
// # 本文件钉的关键语义
//
// ① **stdio 的 `-32000 Connection closed` 不是网络瞬断**——是子进程启动后
//    立刻退出；盲目重试无意义（TS 注释明写）。根因有三种，唯一能分开它们的
//    证据是 **stderr**，故必须走 `classifyStdioExit` 细分。
// ② **认不出来就老实说认不出来**——TS 注释：「**不编根因**：谎报比不报更坏，
//    用户会照着错的方向修半天」。

// TestClassifyStdioEnvProblem —— stderr 指向 PATH 缺失 → process_env。
func TestClassifyStdioEnvProblem(t *testing.T) {
	got := ClassifyMcpError(
		errors.New("MCP error -32000: Connection closed"),
		ErrorContext{
			Transport: ErrorTransportStdio,
			Stderr:    "spawn cmd ENOENT",
		})
	if got.Class != ClassProcessEnv {
		t.Errorf("PATH 缺失应归 process_env，实得 %q", got.Class)
	}
	if got.Retryable {
		t.Error("环境问题不可重试（TS 注释：retrying will not help）")
	}
	if got.Suggestion == "" {
		t.Error("必须有可执行建议")
	}
}

// TestClassifyStdioPackageNotFound —— stderr 指向包不存在 → process_install。
func TestClassifyStdioPackageNotFound(t *testing.T) {
	got := ClassifyMcpError(
		errors.New("-32000: Connection closed"),
		ErrorContext{Transport: ErrorTransportStdio, Stderr: "npm error 404 Not Found"})
	if got.Class != ClassProcessInstall {
		t.Errorf("包不存在应归 process_install，实得 %q", got.Class)
	}
}

// TestClassifyStdioCacheCorrupt —— stderr 指向缓存损坏 → process_install。
func TestClassifyStdioCacheCorrupt(t *testing.T) {
	got := ClassifyMcpError(
		errors.New("-32000: Connection closed"),
		ErrorContext{Transport: ErrorTransportStdio, Stderr: "ENOTEMPTY: directory not empty"})
	if got.Class != ClassProcessInstall {
		t.Errorf("缓存损坏应归 process_install，实得 %q", got.Class)
	}
}

// TestClassifyStdioUnknownStderrFallsBack —— ★ 认不出来就说认不出来。
//
// TS 注释明写「不编根因」。这是**诚实性**要求——比猜一个错根因更有价值。
func TestClassifyStdioUnknownStderrFallsBack(t *testing.T) {
	got := ClassifyMcpError(
		errors.New("-32000: Connection closed"),
		ErrorContext{Transport: ErrorTransportStdio, Stderr: "some unrelated noise"})
	if got.Class != ClassProcess {
		t.Errorf("认不出的 stderr 应回落到 process（不编根因），实得 %q", got.Class)
	}
	if got.Retryable {
		t.Error("子进程退出不可重试")
	}
}

// TestClassifyStdioWithoutStderrFallsBackToGeneric —— 无 stderr → 通用提示。
func TestClassifyStdioWithoutStderrFallsBackToGeneric(t *testing.T) {
	got := ClassifyMcpError(
		errors.New("-32000: Connection closed"),
		ErrorContext{Transport: ErrorTransportStdio})
	if got.Class != ClassProcess {
		t.Errorf("无 stderr 应回落 process，实得 %q", got.Class)
	}
}

// TestClassifyRemoteConnectionClosedIsNetwork —— ★ remote 的同类错误**是**网络。
//
// 同一条 `-32000 Connection closed`，stdio 与 remote 分类**不同**——
// 这是本分类器最容易写错的地方（把 transport 判据漏掉就会全归一类）。
func TestClassifyRemoteConnectionClosedIsNetwork(t *testing.T) {
	got := ClassifyMcpError(
		errors.New("-32000: Connection closed"),
		ErrorContext{Transport: ErrorTransportRemote})
	if got.Class != ClassNetwork {
		t.Errorf("remote 的 connection closed 应归 network，实得 %q", got.Class)
	}
	if !got.Retryable {
		t.Error("网络瞬断可重试（对账 TS retryable: true）")
	}
}

// TestClassifyConfigErrors —— ENOENT / 无效 JSON → config。
func TestClassifyConfigErrors(t *testing.T) {
	for _, msg := range []string{"spawn ENOENT", "invalid json in config"} {
		got := ClassifyMcpError(errors.New(msg), ErrorContext{})
		if got.Class != ClassConfig {
			t.Errorf("%q 应归 config，实得 %q", msg, got.Class)
		}
	}
}

// TestClassifyAuthErrors —— 401/403/oauth → auth。
func TestClassifyAuthErrors(t *testing.T) {
	for _, msg := range []string{"401 Unauthorized", "403 Forbidden", "invalid api key"} {
		got := ClassifyMcpError(errors.New(msg), ErrorContext{})
		if got.Class != ClassAuth {
			t.Errorf("%q 应归 auth，实得 %q", msg, got.Class)
		}
	}
}

// TestClassifyNetworkErrors —— ECONNREFUSED 等 → network（可重试）。
func TestClassifyNetworkErrors(t *testing.T) {
	for _, msg := range []string{"ECONNREFUSED", "ETIMEDOUT", "socket hang up", "fetch failed"} {
		got := ClassifyMcpError(errors.New(msg), ErrorContext{})
		if got.Class != ClassNetwork {
			t.Errorf("%q 应归 network，实得 %q", msg, got.Class)
		}
		if !got.Retryable {
			t.Errorf("%q 应可重试", msg)
		}
	}
}

// TestClassifyProtocolErrors —— 入参/schema 不符 → protocol。
func TestClassifyProtocolErrors(t *testing.T) {
	for _, msg := range []string{"InvalidParams", "malformed response", "parse error"} {
		got := ClassifyMcpError(errors.New(msg), ErrorContext{})
		if got.Class != ClassProtocol {
			t.Errorf("%q 应归 protocol，实得 %q", msg, got.Class)
		}
	}
}

// TestClassifyDefaultIsToolError —— 兜底 tool_error。
func TestClassifyDefaultIsToolError(t *testing.T) {
	got := ClassifyMcpError(errors.New("tool blew up"), ErrorContext{})
	if got.Class != ClassToolError {
		t.Errorf("未命中任何规则应归 tool_error，实得 %q", got.Class)
	}
}

// TestClassListIsSingleSourceOfTruth —— ★ class 清单是单一真相源。
//
// 对账 `failure-classifier.ts` 的 `MCP_ERROR_CLASSES` 注释：那份清单原先
// 是一串字面量写在 UI 里，新增 class 时漏改**不会报错**，只会静默退化成
// 显示英文建议（本地化悄悄失效）。故此清单必须可枚举。
func TestClassListIsSingleSourceOfTruth(t *testing.T) {
	if len(ErrorClasses) != 8 {
		t.Fatalf("应有 8 个 class，实得 %d：%v", len(ErrorClasses), ErrorClasses)
	}
	want := []ErrorClass{
		ClassConfig, ClassAuth, ClassNetwork, ClassProtocol,
		ClassProcess, ClassProcessEnv, ClassProcessInstall, ClassToolError,
	}
	for i, w := range want {
		if ErrorClasses[i] != w {
			t.Errorf("第 %d 项应为 %q，实得 %q", i, w, ErrorClasses[i])
		}
	}
}

// TestClassifyNilErrorDoesNotPanic —— nil error 的健壮性。
func TestClassifyNilErrorDoesNotPanic(t *testing.T) {
	got := ClassifyMcpError(nil, ErrorContext{})
	if got.Class == "" {
		t.Error("nil error 也该给出一个 class（不 panic）")
	}
	if !strings.Contains(string(got.Class), "tool_error") {
		t.Errorf("nil error 应兜底 tool_error，实得 %q", got.Class)
	}
}

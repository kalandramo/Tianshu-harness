package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNegativeFactGuardWiring —— **端到端接线测试**（用户级判据）。
//
// 判据：工具产出的**有损 + 负向断言**内容，在模型收到的**请求体**里必须
// 以 `[⚠ VERIFICATION_REQUIRED]` 开头。
//
// **为什么必须有它**：单元测试只证明 `GuardLossyToolResult` 函数本身正确；
// 若 loop 没调用它（悬空实现），单元测试照样全绿而模型永远看不到标记。
// 这正是本项目反复踩过的「type-without-consumer」缺口。
//
// 手法：bash 工具跑一条**产生有损输出 + 负向词**的命令（输出超限被截断，
// 且含 "empty"），再用 `scriptedServer` 捕获请求体。
func TestNegativeFactGuardWiring(t *testing.T) {
	root := t.TempDir()

	// 命令产出：超 200 行（触发 `[output truncated: last N of M lines shown]`）
	// + 含负向词 "empty"。
	//
	// **阈值出处**：`internal/tools/modeloutput.go:37` 的 `modelMaxLines = 200`
	// ——超过即走截断分支。首版用 30000 字符单行，**不触发**（bash 单流上限
	// 是 8MB，且单行不触发行数截断），导致接线测试假红。由探针实测定位。
	cmd := `seq 1 300; echo "empty"`

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": cmd}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	if err := l.Run(context.TODO(), "跑一条产生有损输出的命令"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	bodies := sc.handlerBodies()
	if len(bodies) < 2 {
		t.Fatalf("应有至少 2 个请求体（工具轮 + 终答轮），得到 %d", len(bodies))
	}

	// 第 2 个请求体含第 1 轮的工具结果——标记应在此可见。
	// 用「含 A 且不含 B」组合断言，避免侥幸通过：
	//   A = [⚠ VERIFICATION_REQUIRED]（守卫标记）
	//   B = "Do NOT conclude absence/emptiness"（TS 收尾文案）
	found := false
	for i, b := range bodies {
		if strings.Contains(b, verificationRequiredMarker) {
			t.Logf("请求体[%d] 含守卫标记", i)
			if !strings.Contains(b, "Do NOT conclude absence/emptiness") {
				t.Errorf("请求体[%d] 含标记但缺收尾文案——注入不完整", i)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("**守卫未接线**：没有任何请求体含 %s。"+
			"检查 loop.go 是否在 appendAndPersist 前调用 GuardLossyToolResult。"+
			"共 %d 个请求体。", verificationRequiredMarker, len(bodies))
	}
}

// TestNegativeFactGuardSilentOnCleanOutput —— 反面对照：无损的正常输出
// **不得**被注入标记（否则每轮都加噪声，污染前缀缓存）。
func TestNegativeFactGuardSilentOnCleanOutput(t *testing.T) {
	root := t.TempDir()

	// 正常小输出，无截断标记、无负向词。
	cmd := `echo "all good"`

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": cmd}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	if err := l.Run(context.TODO(), "跑一条正常命令"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	for i, b := range sc.handlerBodies() {
		if strings.Contains(b, verificationRequiredMarker) {
			t.Errorf("请求体[%d] 不该含守卫标记（输出无损）", i)
		}
	}
}

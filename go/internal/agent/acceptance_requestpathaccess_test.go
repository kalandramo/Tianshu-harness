package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// TestAcceptance81RequestPathAccess —— 用户级验收（第八十一刀）。
//
// # 与 requestpathaccess_wiring_test.go 的区别（关键）
//
// 那个文件用手工 `registry.Register(tools.WriteFile(dir, nil))` 装配——
// **不覆盖生产装配路径**。而本刀修的缺陷（`main.go` 未传 `Grants`）**只在
// 生产路径上**：`tools.NewDefaultRegistry` 装配的写工具。
//
// 本测试走**生产装配**（`NewDefaultRegistry` + `agent.New`），模拟用户可
// 观察的完整链路。
//
// # 用户动作 → 可观察结果
//
//	① 授权前写工作区外文件 → 被拦（文件未落盘）
//	② 调 request_path_access 授权 → 成功消息
//	③ 授权后写 → **文件真的落盘，内容正确**
//	④ 授权 /etc → 被拒（安全闸）
func TestAcceptance81RequestPathAccess(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{workspace, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(outside, "note.txt")

	// **生产装配路径**：与 cmd/tianshu/main.go 的 buildLoop 同形。
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: workspace})
	l := New(Config{
		Cwd:          workspace,
		SessionID:    "acceptance81",
		ApprovalMode: "dangerously-skip-permissions",
	}, nil, reg)

	// ① 授权前：skip 档门链「首触即授」——会授权并放行。
	//    **这本身是本刀修的缺陷的验证点**：修前，门链授了权但工具内部
	//    用 nil grants 拒绝，故此处**不会**落盘。
	_ = l.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": target, "content": "first"},
	})
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("① skip 档首触即授后文件应落盘（这正是被修缺陷的验证点）：%v", err)
	}
	t.Logf("① 首次写已落盘（skip 档首触即授 + 工具内部看到会话授权）")

	// ② 授权（模拟模型主动申请）。
	res := l.executeTool(context.Background(), toolCall{
		name:  "request_path_access",
		input: map[string]any{"path": outside, "mode": "write"},
	})
	if res.IsError {
		t.Fatalf("② 授权应成功，实得：%q", res.Content)
	}
	t.Logf("② 授权成功：%q", res.Content)

	// ③ 授权后写（新文件）。
	target2 := filepath.Join(outside, "after-grant.txt")
	res3 := l.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": target2, "content": "written-after-grant"},
	})
	if res3.IsError {
		t.Fatalf("③ 授权后写应成功，实得：%q", res3.Content)
	}
	got, err := os.ReadFile(target2)
	if err != nil {
		t.Fatalf("③ 文件应落盘：%v", err)
	}
	if string(got) != "written-after-grant" {
		t.Fatalf("③ 内容应正确，实得 %q", got)
	}
	t.Logf("③ 授权后写：文件已落盘，内容=%q", got)

	// ④ 安全闸：系统目录被拒。
	res4 := l.executeTool(context.Background(), toolCall{
		name:  "request_path_access",
		input: map[string]any{"path": "/etc", "mode": "write"},
	})
	if !res4.IsError {
		t.Fatalf("④ /etc 应被拒，实得：%q", res4.Content)
	}
	t.Logf("④ /etc 被拒：%q", res4.Content)
}

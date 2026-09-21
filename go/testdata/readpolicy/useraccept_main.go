// useraccept_main.go —— readpolicy 用户级验收：用**真实仓库文件路径**跑
// Go 侧判定，与 TS 原实现逐字段比对。
//
// 运行：go run testdata/readpolicy/useraccept_main.go <golden.json>
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

type goldenCase struct {
	FilePath         string `json:"filePath"`
	SizeBytes        int    `json:"sizeBytes"`
	HasExplicitRange bool   `json:"hasExplicitRange"`
	Kind             string `json:"kind"`
	Action           string `json:"action"`
	Reason           string `json:"reason"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: go run useraccept_main.go <golden.json>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "读 golden 失败: %v\n", err)
		os.Exit(2)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		fmt.Fprintf(os.Stderr, "解析 golden 失败: %v\n", err)
		os.Exit(2)
	}

	pass, fail := 0, 0
	for _, c := range cases {
		got := tools.DecideReadPolicy(tools.ReadPolicyInput{
			FilePath: c.FilePath, SizeBytes: c.SizeBytes, HasExplicitRange: c.HasExplicitRange,
		})
		ok := string(got.Kind) == c.Kind && string(got.Action) == c.Action && got.Reason == c.Reason
		if ok {
			pass++
			fmt.Printf("  ✓ %s (%d B) → %s/%s\n", c.FilePath, c.SizeBytes, got.Kind, got.Action)
		} else {
			fail++
			fmt.Printf("  ✗ %s (%d B)\n    期望 %s/%s %q\n    实得 %s/%s %q\n",
				c.FilePath, c.SizeBytes, c.Kind, c.Action, c.Reason, got.Kind, got.Action, got.Reason)
		}
	}
	fmt.Printf("\n结果：%d 通过 / %d 失败 / 共 %d\n", pass, fail, len(cases))
	if fail > 0 {
		os.Exit(1)
	}
}

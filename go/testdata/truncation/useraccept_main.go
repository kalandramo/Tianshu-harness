// useraccept_main.go —— 用户级验收：用**真实仓库文件**跑 Go 侧截断，
// 与 TS 原实现（真实文件输入）逐字节比对。
//
// 运行：go run testdata/truncation/useraccept_main.go <golden.json>
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

type goldenCase struct {
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	Content     string `json:"content"`
	FilePath    string `json:"filePath"`
	MaxChars    int    `json:"maxChars"`
	KeepHead    int    `json:"keepHead"`
	KeepTail    int    `json:"keepTail"`
	SkelLines   int    `json:"skelLines"`
	SkelChars   int    `json:"skelChars"`
	ResultBytes string `json:"resultBytes"`
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
		var got string
		switch c.Kind {
		case "truncate":
			got = tools.TruncateContent(c.Content, c.MaxChars, c.KeepHead, c.KeepTail)
		case "partial":
			got = tools.BuildPartialView(c.Content, c.FilePath, c.MaxChars, nil)
		case "skeleton":
			got = tools.BuildPartialView(c.Content, c.FilePath, c.MaxChars,
				&tools.SkeletonSource{Lines: c.SkelLines, Chars: c.SkelChars})
		default:
			fmt.Fprintf(os.Stderr, "未知 kind: %s\n", c.Kind)
			os.Exit(2)
		}
		gotHex := hex.EncodeToString([]byte(got))
		if gotHex == c.ResultBytes {
			pass++
			fmt.Printf("  ✓ %s (%s)\n", c.Label, c.Kind)
		} else {
			fail++
			fmt.Printf("  ✗ %s (%s) 字节不符\n", c.Label, c.Kind)
			fmt.Printf("    期望(len=%d): %s...\n", len(c.ResultBytes), clip(c.ResultBytes, 120))
			fmt.Printf("    实得(len=%d): %s...\n", len(gotHex), clip(gotHex, 120))
		}
	}
	fmt.Printf("\n结果：%d 通过 / %d 失败 / 共 %d\n", pass, fail, len(cases))
	if fail > 0 {
		os.Exit(1)
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

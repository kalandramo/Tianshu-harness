// useraccept_main.go —— read_file 截断的用户级验收：真实仓库文件端到端。
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

func main() {
	root := os.Args[1]
	files := os.Args[2:]
	pass, fail := 0, 0
	for _, f := range files {
		for _, win := range []int{0, 128000, 1000000} {
			tool := tools.ReadFile(root, nil)
			p := &tools.CallParams{
				Input: map[string]any{"file_path": f},
				Cwd:   root, ApprovalMode: "dangerously-skip-permissions",
				ContextWindow: win,
			}
			r, err := tool.Execute(context.Background(), p)
			if err != nil {
				fmt.Printf("  ✗ %s win=%d 错误: %v\n", f, win, err)
				fail++
				continue
			}
			sum := hex.EncodeToString([]byte(r.Content))
			trunc := "no"
			if r.Lossiness != nil {
				trunc = "yes"
			}
			// **量纲**：TS 的 `.length` 是 UTF-16 code unit，Go 的 `len()` 是字节
			// ——必须用 UTF16Len 才能与 TS 对齐（否则含中文的文件会显示不同长度）。
			fmt.Printf("%s\t%d\t%s\t%d\t%s\n", f, win, trunc, tools.UTF16Len(r.Content), sum[:16])
			pass++
		}
	}
	fmt.Printf("\n结果：%d 通过 / %d 失败\n", pass, fail)
	_ = filepath.Base
}

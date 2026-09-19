package tools

import (
	"path/filepath"
	"runtime"
	"strings"
)

// EOL 是行尾风格。
//
// 对账 src/tools/line-endings.ts。模型始终输出 LF；直接写入会破坏
// Windows 消费者（cmd.exe 误解析 LF-only 的 .bat/.cmd），且把 LF 文本
// 拼进 CRLF 文件会产生混合 EOL。
type EOL string

const (
	EOLLF   EOL = "lf"
	EOLCRLF EOL = "crlf"
)

// requiredEOL 返回扩展名强制的 EOL（无则为空）。
//
// 对账 REQUIRED_EOL 表：.bat/.cmd 必须 CRLF——即便在 macOS/Linux 上
// 为 Windows 目标编写。
func requiredEOL(filePath string) EOL {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".bat", ".cmd":
		return EOLCRLF
	}
	return ""
}

// detectEOL 统计 CRLF 与裸 LF，返回主导风格（无换行则返回空）。
//
// 对账 detectEol：逐字符扫描，'\n' 前是 '\r' 记 crlf，否则记 lf；
// **crlf > lf 才返回 crlf**（平局归 lf）。
func detectEOL(text string) EOL {
	crlf, lf := 0, 0
	for i := 0; i < len(text); i++ {
		if text[i] != '\n' {
			continue
		}
		if i > 0 && text[i-1] == '\r' {
			crlf++
		} else {
			lf++
		}
	}
	if crlf == 0 && lf == 0 {
		return ""
	}
	if crlf > lf {
		return EOLCRLF
	}
	return EOLLF
}

// toLF 把任意 CRLF/CR/LF 混合折叠为 LF。
//
// 顺序敏感：先 \r\n 再裸 \r（反序会把 \r\n 拆成 \n\n）。
func toLF(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// applyEOL 把文本转为目标 EOL。applyEOL(x, EOLLF) 等价于 toLF(x)。
func applyEOL(text string, eol EOL) string {
	lf := toLF(text)
	if eol == EOLCRLF {
		return strings.ReplaceAll(lf, "\n", "\r\n")
	}
	return lf
}

// targetEOL 是目标平台的新文件默认 EOL（Windows → CRLF，其他 → LF）。
//
// 对账 src/platform.ts:getTargetEol。注意这是**目标平台**语义——
// 跨平台交叉编写时 TS 侧可被覆盖，Go 侧暂用运行时平台。
func targetEOL() EOL {
	if runtime.GOOS == "windows" {
		return EOLCRLF
	}
	return EOLLF
}

// chooseEOL 解析要写入的 EOL。
//
// 优先级（对账 chooseEol）：扩展名强制 > 文件既有 > 目标平台默认。
func chooseEOL(filePath string, existing EOL) EOL {
	if req := requiredEOL(filePath); req != "" {
		return req
	}
	if existing != "" {
		return existing
	}
	return targetEOL()
}

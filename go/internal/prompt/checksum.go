package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
)

// 会话 JSONL 的完整性机制（对账 src/agent/checksum.ts）。
//
// 行格式：`{json}|{checksum}`，checksum 是 jsonLine 的 SHA-256 前 8 字节
// （16 个 hex 字符）。用 SHA-256 而非 CRC32 的理由（TS 注释）：CRC32 只有
// 2^32 空间、碰撞率高；SHA-256 前 8 字节有 2^64 空间，而 JSONL 每行通常
// <1KB，性能差异可忽略。
//
// **legacy 兼容是核心**：旧格式行（无校验和）必须能读。三条 legacy 判定
// （对账 verifyAndExtract）：
//  1. 无 `|` 分隔符
//  2. `|` 后不是 16 位小写 hex
//  3. `|` 前的部分不是合法 JSON（说明 `|` 是 JSON 内容的一部分）
// 这三条让"JSON 内容里含 `|`"的行不会被误判为带校验和。

var checksumFormatRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ComputeLineChecksum 复刻 checksum.ts 的 computeLineChecksum ——
// SHA-256 十六进制的前 16 字符（= 前 8 字节）。
func ComputeLineChecksum(jsonLine string) string {
	sum := sha256.Sum256([]byte(jsonLine))
	return hex.EncodeToString(sum[:])[:16]
}

// AppendChecksum 复刻 appendChecksum —— 返回 `{json}|{checksum}`。
func AppendChecksum(jsonLine string) string {
	return jsonLine + "|" + ComputeLineChecksum(jsonLine)
}

// VerifyResult 是 verifyAndExtract 的结果。
type VerifyResult struct {
	Valid    bool
	JSON     string
	IsLegacy bool
	Error    string
}

// VerifyAndExtract 复刻 checksum.ts 的 verifyAndExtract —— 验证并提取 JSON 行。
//
// 语义（对账 TS 逐行）：
//   - trim 后为空 → invalid（"Empty line"）
//   - 无 `|` → legacy（valid=true，原样返回）
//   - `|` 后非 16 位小写 hex → legacy（可能是 JSON 内容含 `|`）
//   - `|` 前不是合法 JSON → legacy（同上）
//   - 校验和不匹配 → invalid（错误消息含 expected/got）
//   - 否则 → valid，返回 jsonPart
//
// 注意用 **lastIndexOf** 取最后一个 `|`（对账 TS）。
func VerifyAndExtract(line string) VerifyResult {
	trimmed := trimSpaceUnicode(line)
	if trimmed == "" {
		return VerifyResult{Valid: false, JSON: "", IsLegacy: false, Error: "Empty line"}
	}

	lastPipe := strings.LastIndex(trimmed, "|")
	if lastPipe == -1 {
		return VerifyResult{Valid: true, JSON: trimmed, IsLegacy: true}
	}

	jsonPart := trimmed[:lastPipe]
	storedChecksum := trimmed[lastPipe+1:]

	if !checksumFormatRe.MatchString(storedChecksum) {
		return VerifyResult{Valid: true, JSON: trimmed, IsLegacy: true}
	}

	// jsonPart 不是合法 JSON → legacy（`|` 是内容的一部分）
	if !json.Valid([]byte(jsonPart)) {
		return VerifyResult{Valid: true, JSON: trimmed, IsLegacy: true}
	}

	computed := ComputeLineChecksum(jsonPart)
	if computed != storedChecksum {
		return VerifyResult{
			Valid:    false,
			JSON:     jsonPart,
			IsLegacy: false,
			Error:    "Checksum mismatch: expected " + computed + ", got " + storedChecksum,
		}
	}

	return VerifyResult{Valid: true, JSON: jsonPart, IsLegacy: false}
}

// VerifyLinesResult 是 verifyLines 的结果。
type VerifyLinesResult struct {
	ValidLines   []string
	InvalidCount int
	LegacyCount  int
}

// VerifyLines 复刻 checksum.ts 的 verifyLines —— 批量验证 JSONL 行。
//
// 无效行被跳过（计入 InvalidCount），legacy 行计入 LegacyCount 但仍进
// ValidLines。
func VerifyLines(lines []string) VerifyLinesResult {
	validLines := []string{}
	invalidCount := 0
	legacyCount := 0

	for _, line := range lines {
		r := VerifyAndExtract(line)
		if r.Valid {
			validLines = append(validLines, r.JSON)
			if r.IsLegacy {
				legacyCount++
			}
		} else {
			invalidCount++
		}
	}

	return VerifyLinesResult{ValidLines: validLines, InvalidCount: invalidCount, LegacyCount: legacyCount}
}

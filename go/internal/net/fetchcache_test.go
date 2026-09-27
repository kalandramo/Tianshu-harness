package net

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fetchcache_test.go —— web_fetch 的 maxAge 文件缓存（第九十刀 · W3-4a）。
//
// 对账 TS `src/tools/web-fetch/fetch-cache.ts`（154 行）。
//
// # 语义要点
//
//   - **best-effort**：读写失败都降级（读→miss，写→忽略），绝不因缓存报错
//   - `maxAgeMs == 0` → **禁读仍写**（TS 注释明说）
//   - key = sha256(normalizeCacheUrl(url) + "\n" + variant)
//   - 写满 20 次触发一次过期清扫（节流）

// ── URL 规范化 ──────────────────────────────────────────────────────────

// TestNormalizeCacheURL —— 小写 host / 去默认端口 / 去非 SPA hash / 保留 query。
func TestNormalizeCacheURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://Example.COM/Path", "https://example.com/Path"},
		{"https://example.com:443/a", "https://example.com/a"},
		{"http://example.com:80/a", "http://example.com/a"},
		{"https://example.com:8443/a", "https://example.com:8443/a"},       // 非默认端口保留
		{"https://example.com/a#sec", "https://example.com/a"},             // 普通 hash 去掉
		{"https://example.com/a#/route", "https://example.com/a#/route"},   // SPA hash 保留
		{"https://example.com/a#!/route", "https://example.com/a#!/route"}, // SPA hash 保留
		{"https://example.com/a?q=1", "https://example.com/a?q=1"},         // query 保留
	}
	for _, c := range cases {
		if got := normalizeCacheURL(c.in); got != c.want {
			t.Errorf("normalizeCacheURL(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	// 非法 URL → 原样返回
	if got := normalizeCacheURL("not a url\n"); got != "not a url\n" {
		t.Errorf("非法 URL 应原样返回，实得 %q", got)
	}
}

// ── 读写 ────────────────────────────────────────────────────────────────

// TestFetchCacheWriteRead —— 写入后能读回。
func TestFetchCacheWriteRead(t *testing.T) {
	dir := t.TempDir()
	c := NewFetchCache(dir, FetchCacheOptions{})

	if err := c.Write("https://example.com/a", "e1", FetchCacheEntry{
		URL: "https://example.com/a", Markdown: "# 内容", Status: 200, Via: "",
	}); err != nil {
		t.Fatalf("写入不应报错：%v", err)
	}

	got, err := c.Read("https://example.com/a", "e1")
	if err != nil {
		t.Fatalf("读取不应报错：%v", err)
	}
	if got == nil {
		t.Fatal("应命中缓存")
	}
	if got.Markdown != "# 内容" || got.Status != 200 {
		t.Errorf("内容不符：%+v", got)
	}
	if got.FetchedAt == 0 {
		t.Error("应记录 fetchedAt")
	}
}

// TestFetchCacheVariantIsolation —— 不同 variant 互不干扰。
//
// 对账 TS：variant = `e${extractMainContent ? 1 : 0}`——主内容开关不同时
// 缓存必须隔离（否则开着提取会读到全量的旧缓存）。
func TestFetchCacheVariantIsolation(t *testing.T) {
	dir := t.TempDir()
	c := NewFetchCache(dir, FetchCacheOptions{})

	_ = c.Write("https://example.com/a", "e1", FetchCacheEntry{Markdown: "提取版", Status: 200})
	_ = c.Write("https://example.com/a", "e0", FetchCacheEntry{Markdown: "全量版", Status: 200})

	e1, _ := c.Read("https://example.com/a", "e1")
	e0, _ := c.Read("https://example.com/a", "e0")
	if e1 == nil || e1.Markdown != "提取版" {
		t.Errorf("e1 variant 应为「提取版」，实得 %+v", e1)
	}
	if e0 == nil || e0.Markdown != "全量版" {
		t.Errorf("e0 variant 应为「全量版」，实得 %+v", e0)
	}
}

// TestFetchCacheURLNormalizationShared —— 规范化后相同 URL 共享缓存。
func TestFetchCacheURLNormalizationShared(t *testing.T) {
	dir := t.TempDir()
	c := NewFetchCache(dir, FetchCacheOptions{})

	_ = c.Write("https://Example.COM:443/a", "e1", FetchCacheEntry{Markdown: "共享", Status: 200})
	got, _ := c.Read("https://example.com/a", "e1")
	if got == nil || got.Markdown != "共享" {
		t.Errorf("规范化后应共享缓存，实得 %+v", got)
	}
}

// ── TTL ─────────────────────────────────────────────────────────────────

// TestFetchCacheExpiry —— 过期条目读不到。
func TestFetchCacheExpiry(t *testing.T) {
	dir := t.TempDir()
	nowMs := int64(1_000_000)
	clock := func() int64 { return nowMs }
	c := NewFetchCache(dir, FetchCacheOptions{MaxAgeMs: Int64Ptr(1000), Now: clock})

	_ = c.Write("https://example.com/a", "e1", FetchCacheEntry{Markdown: "旧", Status: 200})

	// 未过期
	if got, _ := c.Read("https://example.com/a", "e1"); got == nil {
		t.Fatal("未过期应命中")
	}
	// 过期（推进时钟超过 maxAge）
	nowMs += 2000
	if got, _ := c.Read("https://example.com/a", "e1"); got != nil {
		t.Errorf("过期后应 miss，实得 %+v", got)
	}
}

// TestFetchCacheZeroMaxAgeDisablesRead —— **maxAge=0 禁读仍写**。
//
// 对账 TS 注释：`if (this.maxAgeMs === 0) return undefined // maxAge:0 禁读`。
// 但写路径不受影响（下次 maxAge 非 0 时能读到）。
func TestFetchCacheZeroMaxAgeDisablesRead(t *testing.T) {
	dir := t.TempDir()
	// **关键**：固定时钟，让 fetchedAt == now（否则单调时钟下 TTL 判定会兜住）
	fixed := int64(1_700_000_000_000)
	clock := func() int64 { return fixed }
	c := NewFetchCache(dir, FetchCacheOptions{MaxAgeMs: Int64Ptr(0), Now: clock})

	_ = c.Write("https://example.com/a", "e1", FetchCacheEntry{Markdown: "写了", Status: 200})
	// 读应恒 miss
	if got, _ := c.Read("https://example.com/a", "e1"); got != nil {
		t.Errorf("maxAge=0 应禁读，实得 %+v", got)
	}
	// 但文件**确实写了**——用**同一时钟**的另一实例（非 0 maxAge）验证。
	// （若用真实时钟，条目 fetchedAt 是固定值会「过期」，测不出写入。）
	c2 := NewFetchCache(dir, FetchCacheOptions{Now: clock})
	got, _ := c2.Read("https://example.com/a", "e1")
	if got == nil || got.Markdown != "写了" {
		t.Errorf("maxAge=0 仍应写入，实得 %+v", got)
	}
}

// ── 健壮性（best-effort）─────────────────────────────────────────────────

// TestFetchCacheMissingFile —— 不存在 → miss（不报错）。
func TestFetchCacheMissingFile(t *testing.T) {
	c := NewFetchCache(t.TempDir(), FetchCacheOptions{})
	got, err := c.Read("https://example.com/never", "e1")
	if err != nil {
		t.Errorf("读 miss 不应报错：%v", err)
	}
	if got != nil {
		t.Errorf("应 miss，实得 %+v", got)
	}
}

// TestFetchCacheCorruptedEntry —— 损坏 JSON → miss（不报错）。
func TestFetchCacheCorruptedEntry(t *testing.T) {
	dir := t.TempDir()
	c := NewFetchCache(dir, FetchCacheOptions{})
	key := c.cacheKey("https://example.com/bad", "e1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, key+".json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := c.Read("https://example.com/bad", "e1")
	if err != nil {
		t.Errorf("损坏条目不应报错：%v", err)
	}
	if got != nil {
		t.Errorf("损坏条目应 miss，实得 %+v", got)
	}
}

// TestFetchCacheMissingFetchedAt —— 缺 fetchedAt 字段 → miss（对账 TS 的类型检查）。
func TestFetchCacheMissingFetchedAt(t *testing.T) {
	dir := t.TempDir()
	c := NewFetchCache(dir, FetchCacheOptions{})
	key := c.cacheKey("https://example.com/x", "e1")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, key+".json"), []byte(`{"url":"x","markdown":"y","status":200}`), 0o644)

	if got, _ := c.Read("https://example.com/x", "e1"); got != nil {
		t.Errorf("缺 fetchedAt 应 miss，实得 %+v", got)
	}
}

// TestFetchCacheWriteToUnwritableDir —— 写失败静默降级（best-effort）。
func TestFetchCacheWriteToUnwritableDir(t *testing.T) {
	// 用一个「父路径是文件」的目录 → mkdir 必失败
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewFetchCache(filepath.Join(blocker, "sub"), FetchCacheOptions{})
	// 不应 panic / 不应返回错误（TS 的 try/catch 吞掉）
	if err := c.Write("https://example.com/a", "e1", FetchCacheEntry{Markdown: "x", Status: 200}); err != nil {
		t.Errorf("写失败应静默降级，实得 %v", err)
	}
}

// ── 清扫 ────────────────────────────────────────────────────────────────

// TestFetchCacheSweep —— 清扫删除过期与损坏条目。
func TestFetchCacheSweep(t *testing.T) {
	dir := t.TempDir()
	nowMs := int64(1_000_000)
	clock := func() int64 { return nowMs }
	c := NewFetchCache(dir, FetchCacheOptions{MaxAgeMs: Int64Ptr(1000), Now: clock})

	_ = c.Write("https://example.com/old", "e1", FetchCacheEntry{Markdown: "旧", Status: 200})
	nowMs += 5000 // 过期
	_ = c.Write("https://example.com/new", "e1", FetchCacheEntry{Markdown: "新", Status: 200})

	// 手工放一个损坏文件
	_ = os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{bad"), 0o644)

	if err := c.Sweep(); err != nil {
		t.Fatalf("清扫不应报错：%v", err)
	}

	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// 只剩「新」那一条
	if len(names) != 1 {
		t.Errorf("清扫后应只剩 1 个文件，实得 %v", names)
	}
	for _, n := range names {
		if n == "corrupt.json" {
			t.Error("损坏文件应被清理")
		}
	}
}

// TestFetchCacheSweepMissingDir —— 目录不存在 → 静默返回。
func TestFetchCacheSweepMissingDir(t *testing.T) {
	c := NewFetchCache(filepath.Join(t.TempDir(), "never-created"), FetchCacheOptions{})
	if err := c.Sweep(); err != nil {
		t.Errorf("目录不存在时清扫应静默，实得 %v", err)
	}
}

// ── formatCacheAge ──────────────────────────────────────────────────────

// TestFormatCacheAge —— 逐字对账 TS。
func TestFormatCacheAge(t *testing.T) {
	now := int64(10_000_000_000)
	cases := []struct {
		agoMs int64
		want  string
	}{
		{0, "1 分钟"},      // 最小 1 分钟
		{30_000, "1 分钟"}, // <1min 归 1
		{5 * 60_000, "5 分钟"},
		{59 * 60_000, "59 分钟"},
		{60 * 60_000, "1 小时"},
		{90 * 60_000, "2 小时"}, // Math.round(90/60)=2
		{23 * 3600_000, "23 小时"},
		{24 * 3600_000, "1 天"},
		{48 * 3600_000, "2 天"},
		{72 * 3600_000, "3 天"},
	}
	for _, c := range cases {
		got := FormatCacheAge(now-c.agoMs, now)
		if got != c.want {
			t.Errorf("FormatCacheAge(ago=%dms) = %q，期望 %q", c.agoMs, got, c.want)
		}
	}
}

// TestFetchCacheDir —— 缓存目录推导。
func TestFetchCacheDir(t *testing.T) {
	got := FetchCacheDir("/work")
	want := filepath.Join("/work", ".rivet", "cache", "web-fetch")
	if got != want {
		t.Errorf("目录应为 %q，实得 %q", want, got)
	}
}

// TestDefaultCacheMaxAge —— 常量对账（2 天）。
func TestDefaultCacheMaxAge(t *testing.T) {
	if defaultCacheMaxAgeMs != 2*24*60*60*1000 {
		t.Errorf("默认 maxAge 应为 2 天，实得 %d", defaultCacheMaxAgeMs)
	}
}

// TestFetchCacheWriteIsAtomicEnough —— 写入产生合法 JSON（可读回）。
func TestFetchCacheWriteIsAtomicEnough(t *testing.T) {
	dir := t.TempDir()
	c := NewFetchCache(dir, FetchCacheOptions{})
	md := strings.Repeat("内容", 1000)
	_ = c.Write("https://example.com/big", "e1", FetchCacheEntry{Markdown: md, Status: 200})

	got, _ := c.Read("https://example.com/big", "e1")
	if got == nil || got.Markdown != md {
		t.Errorf("大内容应完整往返，实得长度 %d（期望 %d）", len(got.Markdown), len(md))
	}
}

// 确保 time 被引用（FetchCacheOptions.Now 的类型）。
var _ = time.Now

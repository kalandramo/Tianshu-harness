package net

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fetchcache.go —— web_fetch 的 maxAge 文件缓存（第九十刀 · W3-4a）。
//
// 对账 TS `src/tools/web-fetch/fetch-cache.ts`（154 行）。
//
// # 语义要点
//
//   - **best-effort**：读写失败都降级（读→miss，写→忽略），绝不因缓存报错
//   - `MaxAgeMs == 0` → **禁读仍写**（对账 TS 注释 `maxAge:0 禁读`）
//   - key = sha256(normalizeCacheURL(url) + "\n" + variant)
//   - 写满 20 次触发一次过期清扫（节流——避免每次写都全目录扫描）

// defaultCacheMaxAgeMs 对账 TS `DEFAULT_CACHE_MAX_AGE_MS`（2 天）。
const defaultCacheMaxAgeMs int64 = 2 * 24 * 60 * 60 * 1000

// sweepEveryNWrites 对账 TS `SWEEP_EVERY_N_WRITES`。
const sweepEveryNWrites = 20

// FetchCacheEntry 对账 TS `FetchCacheEntry`。
type FetchCacheEntry struct {
	URL       string `json:"url"`
	Markdown  string `json:"markdown"`
	Via       string `json:"via"`
	Status    int    `json:"status"`
	FetchedAt int64  `json:"fetchedAt"`
}

// FetchCacheOptions 对账 TS `FetchCacheOptions`。
type FetchCacheOptions struct {
	// MaxAgeMs：读取有效期（默认 2 天；**0 = 禁读仍写**）。
	//
	// **用指针**（与 httpfetch 的 Options 同型）：Go 零值无法区分
	// 「未设置」（用默认 2 天）与「显式 0」（禁读）。
	MaxAgeMs *int64
	// Now：可注入的时钟（测试用）。nil → time.Now。
	Now func() int64
}

// FetchCache 对账 TS `FetchCache` 类。
type FetchCache struct {
	dir              string
	maxAgeMs         int64
	now              func() int64
	writesSinceSweep int
}

// NewFetchCache 创建缓存实例。
//
// `opts.MaxAgeMs == nil` → 默认 2 天；`Int64Ptr(0)` → 禁读仍写。
func NewFetchCache(dir string, opts FetchCacheOptions) *FetchCache {
	maxAge := defaultCacheMaxAgeMs
	if opts.MaxAgeMs != nil {
		maxAge = *opts.MaxAgeMs
	}
	now := opts.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &FetchCache{dir: dir, maxAgeMs: maxAge, now: now}
}

// Int64Ptr 返回 *int64（供设置 FetchCacheOptions.MaxAgeMs）。
func Int64Ptr(v int64) *int64 { return &v }

// normalizeCacheURL 对账 TS `normalizeCacheURL`。
//
// 小写 host、去默认端口、去**非 SPA** hash（保留 `#/` 与 `#!/` 路由）、保留 query。
func normalizeCacheURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Host = strings.ToLower(u.Host)
	// 去默认端口（对账 TS：`u.port === '' ` 的写法等价于去 Host 里的默认端口）。
	host := u.Hostname()
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		u.Host = host
	} else if port != "" {
		u.Host = host + ":" + port
	} else {
		u.Host = host
	}
	// hash：只保留 SPA 路由形式（`#/...` 与 `#!/...`）。
	//
	// **注意 Go 的解析行为**：`url.Parse("https://x/a#!/r")` 得到 `Fragment = "!/r"`
	// （`#` 后的内容剥掉 `#`）。故判断要看**字面前缀**是否 `!/`（对应 `#!/`）
	// 或 `/`（对应 `#/`）。
	if u.Fragment != "" {
		keep := strings.HasPrefix(u.Fragment, "/") || strings.HasPrefix(u.Fragment, "!/")
		if !keep {
			u.Fragment = ""
			u.RawFragment = ""
		}
	}
	return u.String()
}

// cacheKey 对账 TS `key`——sha256(normalizeCacheUrl(url) + "\n" + variant)。
func (c *FetchCache) cacheKey(rawURL, variant string) string {
	sum := sha256.Sum256([]byte(normalizeCacheURL(rawURL) + "\n" + variant))
	return hex.EncodeToString(sum[:])
}

// Read 对账 TS `read`——miss/损坏/过期都返回 nil（best-effort，不报错）。
func (c *FetchCache) Read(rawURL, variant string) (*FetchCacheEntry, error) {
	if c.maxAgeMs == 0 {
		return nil, nil // maxAge:0 禁读
	}
	path := filepath.Join(c.dir, c.cacheKey(rawURL, variant)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var entry FetchCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, nil
	}
	// 对账 TS 的类型检查：缺 fetchedAt 视为无效。
	// JSON 里没有 fetchedAt 字段时 unmarshal 得 0——与 TS 的 `typeof !== 'number'` 等价。
	if entry.FetchedAt == 0 {
		return nil, nil
	}
	if entry.FetchedAt+c.maxAgeMs < c.now() {
		return nil, nil // 过期
	}
	return &entry, nil
}

// Write 对账 TS `write`——失败静默降级。
func (c *FetchCache) Write(rawURL, variant string, entry FetchCacheEntry) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return nil // best-effort
	}
	entry.FetchedAt = c.now()
	data, err := json.Marshal(entry)
	if err != nil {
		return nil
	}
	path := filepath.Join(c.dir, c.cacheKey(rawURL, variant)+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil // best-effort
	}
	c.writesSinceSweep++
	if c.writesSinceSweep >= sweepEveryNWrites {
		c.writesSinceSweep = 0
		_ = c.Sweep()
	}
	return nil
}

// Sweep 对账 TS `sweep`——清理过期与损坏条目。
func (c *FetchCache) Sweep() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil // 目录不存在 → 静默
	}
	now := c.now()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(c.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var entry FetchCacheEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			_ = os.Remove(path) // 损坏条目一并清理
			continue
		}
		if entry.FetchedAt == 0 || entry.FetchedAt+c.maxAgeMs < now {
			_ = os.Remove(path)
		}
	}
	return nil
}

// FetchCacheDir 对账 TS `fetchCacheDir`——`<cwd>/.rivet/cache/web-fetch`。
func FetchCacheDir(cwd string) string {
	return filepath.Join(cwd, ".rivet", "cache", "web-fetch")
}

// FormatCacheAge 对账 TS `formatCacheAge`。
//
// 最小 1 分钟；<60 分钟用「N 分钟」；<24 小时用「N 小时」；否则「N 天」。
func FormatCacheAge(fetchedAt, now int64) string {
	minutes := (now - fetchedAt) / 60_000
	if minutes < 1 {
		minutes = 1
	}
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	hours := (minutes + 30) / 60 // 对账 JS Math.round
	if hours < 24 {
		return fmt.Sprintf("%d 小时", hours)
	}
	return fmt.Sprintf("%d 天", (hours+12)/24) // 对账 JS Math.round
}

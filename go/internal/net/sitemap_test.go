package net

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// sitemap_test.go —— sitemap 阶梯发现源（第九十五刀 · W3-5b）。
//
// 对账 TS `src/tools/web-crawl/sitemap.ts`（96 行）。

func TestExtractLocs(t *testing.T) {
	xml := `<urlset>
<url><loc>https://example.com/a</loc></url>
<url><loc>  https://example.com/b  </loc></url>
</urlset>`
	got := extractLocs(xml)
	if len(got) != 2 {
		t.Fatalf("应提取 2 条，实得 %d：%v", len(got), got)
	}
	if got[0] != "https://example.com/a" || got[1] != "https://example.com/b" {
		t.Errorf("应 trim 空白，实得 %v", got)
	}
	// 大小写不敏感 + 跨行匹配
	upper := "<urlset><LOC>\nhttps://x.com/a\n</LOC></urlset>"
	if got := extractLocs(upper); len(got) != 1 || got[0] != "https://x.com/a" {
		t.Errorf("应支持大写与跨行，实得 %v", got)
	}
	// 空/无效
	if got := extractLocs("<html>no loc</html>"); len(got) != 0 {
		t.Errorf("无 loc 应返回空，实得 %v", got)
	}
}

func TestCollectSitemapURLsRobotsFirst(t *testing.T) {
	responses := map[string]string{
		"https://example.com/robots.txt":         "User-agent: *\nSitemap: https://example.com/custom-sitemap.xml\n",
		"https://example.com/custom-sitemap.xml": `<urlset><url><loc>https://example.com/from-robots</loc></url></urlset>`,
	}
	doer := func(req *http.Request) (*http.Response, error) {
		body, ok := responses[req.URL.String()]
		if !ok {
			return cannedResponse(404, "text/plain", ""), nil
		}
		return cannedResponse(200, "application/xml", body), nil
	}
	seed, _ := url.Parse("https://example.com/")
	got := CollectSitemapURLs(seed, Deps{Lookup: publicLookup, Doer: doer}, Options{})

	if len(got.URLs) != 1 || got.URLs[0] != "https://example.com/from-robots" {
		t.Errorf("应取 robots 声明的 sitemap，实得 %v", got.URLs)
	}
	// robots 应被尝试过
	found := false
	for _, h := range got.SitemapsHit {
		if strings.Contains(h, "robots") || h == "https://example.com/custom-sitemap.xml" {
			found = true
		}
	}
	if !found {
		t.Errorf("SitemapsHit 应含 robots 声明的地址，实得 %v", got.SitemapsHit)
	}
}

func TestCollectSitemapURLsSeedDirFallback(t *testing.T) {
	// robots 不存在 → 回退种子目录 sitemap.xml
	responses := map[string]string{
		"https://example.com/docs/sitemap.xml": `<urlset><url><loc>https://example.com/docs/a</loc></url></urlset>`,
	}
	doer := func(req *http.Request) (*http.Response, error) {
		body, ok := responses[req.URL.String()]
		if !ok {
			return cannedResponse(404, "text/plain", ""), nil
		}
		return cannedResponse(200, "application/xml", body), nil
	}
	seed, _ := url.Parse("https://example.com/docs/guide")
	got := CollectSitemapURLs(seed, Deps{Lookup: publicLookup, Doer: doer}, Options{})

	if len(got.URLs) != 1 || got.URLs[0] != "https://example.com/docs/a" {
		t.Errorf("应回退种子目录 sitemap，实得 %v", got.URLs)
	}
}

func TestCollectSitemapIndexRecursion(t *testing.T) {
	responses := map[string]string{
		"https://example.com/sitemap.xml": `<sitemapindex>
<sitemap><loc>https://example.com/sub1.xml</loc></sitemap>
</sitemapindex>`,
		"https://example.com/sub1.xml": `<urlset><url><loc>https://example.com/leaf</loc></url></urlset>`,
	}
	doer := func(req *http.Request) (*http.Response, error) {
		body, ok := responses[req.URL.String()]
		if !ok {
			return cannedResponse(404, "text/plain", ""), nil
		}
		return cannedResponse(200, "application/xml", body), nil
	}
	seed, _ := url.Parse("https://example.com/")
	got := CollectSitemapURLs(seed, Deps{Lookup: publicLookup, Doer: doer}, Options{})

	if len(got.URLs) != 1 || got.URLs[0] != "https://example.com/leaf" {
		t.Errorf("sitemapindex 应递归，实得 %v", got.URLs)
	}
}

func TestCollectSitemapSkipsGz(t *testing.T) {
	fetchCount := 0
	doer := func(req *http.Request) (*http.Response, error) {
		fetchCount++
		if strings.HasSuffix(req.URL.Path, ".gz") {
			t.Errorf("不应抓取 .gz sitemap：%s", req.URL.Path)
		}
		return cannedResponse(404, "text/plain", ""), nil
	}
	seed, _ := url.Parse("https://example.com/")
	_ = CollectSitemapURLs(seed, Deps{Lookup: publicLookup, Doer: doer}, Options{})
	// **种子在根目录时**：目录候选 = origin 根候选（`/sitemap.xml`），
	// `dedupStrings` 去重（对账 TS 的 `[...new Set(candidates)]`）——
	// 故只有 robots + sitemap.xml **两个**候选，而非三个。
	// （探针实测确认。）
	if fetchCount != 2 {
		t.Errorf("根目录种子应尝试 robots + sitemap.xml 共 2 次，实得 %d 次", fetchCount)
	}

	// 对照：种子在子目录时，目录候选与根候选**不同** → 3 个候选
	fetchCount = 0
	seed2, _ := url.Parse("https://example.com/docs/guide")
	_ = CollectSitemapURLs(seed2, Deps{Lookup: publicLookup, Doer: doer}, Options{})
	if fetchCount != 3 {
		t.Errorf("子目录种子应尝试 3 次（robots + /docs/sitemap.xml + /sitemap.xml），实得 %d 次", fetchCount)
	}
}

func TestCollectSitemapNoNetworkFailure(t *testing.T) {
	// Doer 全部失败 → 不 panic，返回空
	doer := func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	}
	seed, _ := url.Parse("https://example.com/")
	got := CollectSitemapURLs(seed, Deps{Lookup: publicLookup, Doer: doer}, Options{})
	if len(got.URLs) != 0 {
		t.Errorf("全失败应返回空，实得 %v", got.URLs)
	}
}

func TestDedupStrings(t *testing.T) {
	got := dedupStrings([]string{"a", "b", "a", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("应去重为 %d 条，实得 %d：%v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] 应为 %q，实得 %q（应保序）", i, want[i], got[i])
		}
	}
}

func TestSitemapConstants(t *testing.T) {
	if maxSitemaps != 25 {
		t.Errorf("MAX_SITEMAPS 应为 25，实得 %d", maxSitemaps)
	}
	if maxSitemapURLs != 500 {
		t.Errorf("MAX_SITEMAP_URLS 应为 500，实得 %d", maxSitemapURLs)
	}
}

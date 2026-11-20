// Package update 检查有没有新版本：查 GitHub 上发布的最新正式版（不含草稿和预发布版），和本机版本比较。
// 只提示，不下载也不安装。
//
// 查询发给 api.github.com，GitHub 能看到本机的 IP 地址和 User-Agent（"Portide/版本"），除此之外不发送任何东西。
// 结果缓存在数据目录里，一天最多查一次；用户可以在设置里关掉，或设置环境变量 PORTIDE_NO_UPDATE_CHECK=1。
package update

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"portide/core/i18n"
	"portide/core/version"
)

// Repo 是发布安装包的 GitHub 仓库（"owner/name"），由构建脚本用 -ldflags "-X portide/core/update.Repo=…" 写入；
// 发布流程（.github/workflows/release.yml）里取当前仓库。为空时（例如本地自己编译）不检查更新。
var Repo string

// apiBase 是 GitHub API 的地址，测试时换成本机的测试服务器。
var apiBase = "https://api.github.com"

// Interval 是两次自动检查之间的最短间隔。
const Interval = 24 * time.Hour

// cacheFile 是数据目录里缓存检查结果的文件。
const cacheFile = "update-check.json"

// Info 是一次检查的结果。
type Info struct {
	Current   string    `json:"current"`   // 本机版本
	Latest    string    `json:"latest"`    // 最新正式版的版本号；还没查过时为空
	URL       string    `json:"url"`       // 最新版的发布页面
	Newer     bool      `json:"newer"`     // 最新版比本机新
	CheckedAt time.Time `json:"checkedAt"` // 上次查询成功的时间；还没查过时为零值
}

// Enabled 报告这个构建能不能检查更新，以及环境变量有没有关掉它。设置里的开关由调用方另外判断。
func Enabled() bool {
	return Repo != "" && os.Getenv("PORTIDE_NO_UPDATE_CHECK") == ""
}

// Checker 检查更新并把结果缓存在数据目录里；方法可以并发调用。
type Checker struct {
	dir    string
	client *http.Client
	mu     sync.Mutex
}

// NewChecker 返回一个把结果缓存在 dataDir 里的 Checker。
func NewChecker(dataDir string) *Checker {
	return &Checker{dir: dataDir, client: &http.Client{Timeout: 15 * time.Second}}
}

// Cached 返回上次检查的结果，不联网。
func (c *Checker) Cached() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.load()
}

// Check 返回最新版本：上次成功查询不到 Interval 时直接用缓存，force 为 true 时总是联网查询。
// 查询失败时返回错误和缓存的结果。
func (c *Checker) Check(ctx context.Context, force bool) (Info, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info := c.load()
	if !force && !info.CheckedAt.IsZero() && time.Since(info.CheckedAt) < Interval {
		return info, nil
	}
	if !Enabled() {
		return info, i18n.New("检查更新已关闭")
	}
	latest, url, err := fetchLatest(ctx, c.client, Repo)
	if err != nil {
		return info, err
	}
	info = Info{Latest: latest, URL: url, CheckedAt: time.Now().UTC()}
	if b, err := json.Marshal(info); err == nil {
		_ = os.WriteFile(filepath.Join(c.dir, cacheFile), b, 0o600)
	}
	return c.finish(info), nil
}

func (c *Checker) load() Info {
	var info Info
	if b, err := os.ReadFile(filepath.Join(c.dir, cacheFile)); err == nil {
		_ = json.Unmarshal(b, &info)
	}
	return c.finish(info)
}

// finish 填上本机版本并重新比较（缓存可能是旧版本写下的）。
func (c *Checker) finish(info Info) Info {
	info.Current = version.Version
	info.Newer = info.Latest != "" && Newer(info.Latest, version.Version)
	return info
}

// fetchLatest 查询 repo 最新的正式版，返回去掉 "v" 前缀的版本号和发布页面。
func fetchLatest(ctx context.Context, client *http.Client, repo string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Portide/"+version.Version)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", i18n.New("检查更新失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", i18n.New("检查更新失败：GitHub 返回 %s", resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", "", err
	}
	v := strings.TrimPrefix(rel.TagName, "v")
	if _, ok := parse(v); !ok {
		return "", "", i18n.New("检查更新失败：无法识别的版本标签 %q", rel.TagName)
	}
	if !strings.HasPrefix(rel.HTMLURL, "https://") {
		return "", "", i18n.New("检查更新失败：无法识别的发布页面 %q", rel.HTMLURL)
	}
	return v, rel.HTMLURL, nil
}

// semver 是解析后的语义化版本（不含构建元数据）。
type semver struct {
	core [3]int
	pre  []string
}

func parse(v string) (semver, bool) {
	var s semver
	v, _, _ = strings.Cut(v, "+")
	v, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return s, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return s, false
		}
		s.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return s, false
		}
		s.pre = strings.Split(pre, ".")
	}
	return s, true
}

// Newer 报告版本 a 是否比 b 新（语义化版本的比较规则：0.6.0-beta.1 < 0.6.0）。无法解析的版本号一律不算新。
func Newer(a, b string) bool {
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	return compare(x, y) > 0
}

func compare(x, y semver) int {
	for i := range 3 {
		if x.core[i] != y.core[i] {
			return cmpInt(x.core[i], y.core[i])
		}
	}
	switch {
	case len(x.pre) == 0 && len(y.pre) == 0:
		return 0
	case len(x.pre) == 0:
		return 1
	case len(y.pre) == 0:
		return -1
	}
	for i := 0; i < len(x.pre) && i < len(y.pre); i++ {
		a, b := x.pre[i], y.pre[i]
		na, errA := strconv.Atoi(a)
		nb, errB := strconv.Atoi(b)
		switch {
		case errA == nil && errB == nil:
			if na != nb {
				return cmpInt(na, nb)
			}
		case errA == nil:
			return -1 // 数字标识比字母标识小
		case errB == nil:
			return 1
		case a != b:
			return strings.Compare(a, b)
		}
	}
	return cmpInt(len(x.pre), len(y.pre))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

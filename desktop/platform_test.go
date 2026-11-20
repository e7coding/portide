package main

import (
	"testing"

	"portide/core/update"
)

func TestAllowedURL(t *testing.T) {
	for link, want := range map[string]bool{
		"https://example.com/a?b=1": true, "http://192.168.1.5:8080": true, "HTTPS://Example.com": true,
		"file:///etc/passwd": false, "javascript:alert(1)": false, "ftp://x": false, "https://": false, "example.com": false,
	} {
		if got := allowedURL(link); got != want {
			t.Errorf("allowedURL(%q) = %v, want %v", link, got, want)
		}
	}
}

// 设置里关掉更新检查时，自动检查只读缓存、不联网；本地构建（没有写入发布仓库）点"检查更新"会报错。
func TestCheckUpdateRespectsPrefs(t *testing.T) {
	dir := t.TempDir()
	p := &Platform{prefs: newPrefsStore(dir, false), updates: update.NewChecker(dir)}
	if err := p.SetUpdateCheck(false); err != nil {
		t.Fatal(err)
	}
	info, err := p.CheckUpdate(false)
	if err != nil || info.Current == "" || info.Newer || !info.CheckedAt.IsZero() {
		t.Fatalf("info = %+v, err = %v", info, err)
	}
	if update.Repo == "" {
		if _, err := p.CheckUpdate(true); err == nil {
			t.Fatal("a build without a release repo can't check")
		}
	}
}

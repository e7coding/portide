package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"portide/core/version"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.6.0", "0.5.0", true},
		{"0.5.0", "0.5.0", false},
		{"0.5.1", "0.5.0", true},
		{"1.0.0", "0.9.9", true},
		{"0.10.0", "0.9.0", true},
		{"0.6.0", "0.6.0-beta.1", true},
		{"0.6.0-beta.1", "0.6.0", false},
		{"0.6.0-beta.2", "0.6.0-beta.1", true},
		{"0.6.0-beta.10", "0.6.0-beta.2", true},
		{"0.6.0-rc.1", "0.6.0-beta.9", true},
		{"0.6.0-beta.1.1", "0.6.0-beta.1", true},
		{"0.6.0-beta", "0.6.0-1", true},
		{"0.6.0+build.5", "0.6.0", false},
		{"junk", "0.5.0", false},
		{"0.6", "0.5.0", false},
		{"0.6.0-", "0.5.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// fakeGitHub 起一个本进程内的测试服务器，回答 releases/latest。
func fakeGitHub(t *testing.T, tag string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/repos/acme/portide/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if ua := r.Header.Get("User-Agent"); ua != "Portide/"+version.Version {
			t.Errorf("User-Agent = %q", ua)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://github.com/acme/portide/releases/tag/` + tag + `","body":"ignored"}`))
	}))
	t.Cleanup(srv.Close)
	oldBase, oldRepo := apiBase, Repo
	apiBase, Repo = srv.URL, "acme/portide"
	t.Cleanup(func() { apiBase, Repo = oldBase, oldRepo })
	t.Setenv("PORTIDE_NO_UPDATE_CHECK", "")
	return &hits
}

func TestCheckFindsNewerAndCaches(t *testing.T) {
	hits := fakeGitHub(t, "v99.0.0")
	c := NewChecker(t.TempDir())
	if got := c.Cached(); got.Latest != "" || got.Newer || got.Current != version.Version {
		t.Fatalf("before any check: %+v", got)
	}
	info, err := c.Check(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if info.Latest != "99.0.0" || !info.Newer || info.URL != "https://github.com/acme/portide/releases/tag/v99.0.0" || info.CheckedAt.IsZero() {
		t.Fatalf("info = %+v", info)
	}
	// 一天之内再查直接用缓存；新的 Checker 也能读到缓存。
	if _, err := NewChecker(c.dir).Check(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1 (cached)", hits.Load())
	}
	if _, err := c.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2 (forced)", hits.Load())
	}
	if got := c.Cached(); !got.Newer || got.Latest != "99.0.0" {
		t.Fatalf("cached = %+v", got)
	}
}

func TestCheckSameVersionIsNotNewer(t *testing.T) {
	fakeGitHub(t, "v"+version.Version)
	info, err := NewChecker(t.TempDir()).Check(context.Background(), true)
	if err != nil || info.Newer {
		t.Fatalf("info = %+v, err = %v", info, err)
	}
}

func TestCheckRejectsBadTag(t *testing.T) {
	fakeGitHub(t, "nightly")
	c := NewChecker(t.TempDir())
	if _, err := c.Check(context.Background(), true); err == nil {
		t.Fatal("want an error for a non-semver tag")
	}
	if got := c.Cached(); !got.CheckedAt.IsZero() {
		t.Fatalf("a failed check must not be cached: %+v", got)
	}
}

func TestStaleCacheIsRechecked(t *testing.T) {
	hits := fakeGitHub(t, "v99.0.0")
	c := NewChecker(t.TempDir())
	if _, err := c.Check(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	// 把缓存改成两天前的。
	old := c.Cached()
	old.CheckedAt = time.Now().Add(-2 * Interval)
	writeCache(t, c, old)
	if _, err := c.Check(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestDisabled(t *testing.T) {
	hits := fakeGitHub(t, "v99.0.0")
	t.Setenv("PORTIDE_NO_UPDATE_CHECK", "1")
	if Enabled() {
		t.Fatal("PORTIDE_NO_UPDATE_CHECK should disable the check")
	}
	if _, err := NewChecker(t.TempDir()).Check(context.Background(), true); err == nil {
		t.Fatal("want an error when disabled")
	}
	t.Setenv("PORTIDE_NO_UPDATE_CHECK", "")
	Repo = ""
	if Enabled() {
		t.Fatal("a build without a repo can't check")
	}
	if hits.Load() != 0 {
		t.Fatalf("hits = %d, want 0", hits.Load())
	}
}

func writeCache(t *testing.T, c *Checker, info Info) {
	t.Helper()
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, cacheFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

package transfer

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanPath(t *testing.T) {
	ok := map[string]string{
		"a.txt":             "a.txt",
		"照片/2026/猫.jpg":     "照片/2026/猫.jpg",
		"a//b/./c":          "a/b/c",
		`a\..\b`:            `a_.._b`, // 反斜杠不是分隔符，替换后只是普通名字
		"what?.txt":         "what_.txt",
		"ab:c|d*e\"f<g>h":   "ab_c_d_e_f_g_h",
		"a:b?.txt":          "a_b_.txt",
		"tab\there":         "tab_here",
		"trailing. . ":      "trailing",
		"...":               "_",
		"CON":               "_CON",
		"con.txt":           "_con.txt",
		"Com1.log":          "_Com1.log",
		"lpt9":              "_lpt9",
		"COM¹":              "_COM¹",
		"console":           "console",
		"dir/nul/x":         "dir/_nul/x",
		"bad\xffutf8":       "bad_utf8",
		".bashrc":           ".bashrc",
		"emoji 😀/文件 (1).md": "emoji 😀/文件 (1).md",
	}
	for in, want := range ok {
		got, err := CleanPath(in)
		if err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "/", "/etc/passwd", `\\server\share`, "..", "a/../b", "../a", "C:", "c:/windows", `C:\Windows\x`, "./.", "./C:/windows/x", strings.Repeat("a/", 65) + "b"} {
		if got, err := CleanPath(in); err == nil {
			t.Errorf("CleanPath(%q) = %q, want error", in, got)
		}
	}
}

func TestCleanPathTruncatesKeepingExtension(t *testing.T) {
	long := strings.Repeat("长", 200) + ".jpeg" // 600+ 字节
	got, err := CleanPath(long)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxComponentBytes || !strings.HasSuffix(got, ".jpeg") || !utf8.ValidString(got) {
		t.Fatalf("got %q (%d bytes)", got, len(got))
	}
}

func TestUniqueName(t *testing.T) {
	taken := map[string]bool{"a.txt": true, "a (1).txt": true, "photos": true, "x/b": true}
	is := func(p string) bool { return taken[p] }
	for _, c := range []struct {
		in    string
		isDir bool
		want  string
	}{
		{"new.txt", false, "new.txt"},
		{"a.txt", false, "a (2).txt"},
		{"photos", true, "photos (1)"},
		{"x/b", false, "x/b (1)"},
		{"v1.2", true, "v1.2"},
	} {
		got, err := uniqueName(c.in, c.isDir, is)
		if err != nil || got != c.want {
			t.Errorf("uniqueName(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

// 扩展名很长的名字重名时不能 panic，改名后仍不超过 255 字节。
func TestUniqueNameLongExtension(t *testing.T) {
	name := "a." + strings.Repeat("x", 253)
	got, err := uniqueName(name, false, func(p string) bool { return p == name })
	if err != nil || got == name || len(got) > maxComponentBytes {
		t.Fatalf("uniqueName = %q (%d bytes), %v", got, len(got), err)
	}
}

func TestResolvePaths(t *testing.T) {
	existing := map[string]bool{"photos": true, "a.txt": true}
	paths := []string{"photos", "photos/1.jpg", "photos/sub", "photos/sub/2.jpg", "a.txt", "B.txt", "b.txt", "x", "x/y"}
	isDir := []bool{true, false, true, false, false, false, false, false, false}
	got, err := resolvePaths(paths, isDir, func(p string) bool { return existing[p] })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"photos (1)", "photos (1)/1.jpg", "photos (1)/sub", "photos (1)/sub/2.jpg", "a (1).txt", "B.txt", "b (1).txt", "x", "x (1)/y"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d %q → %q, want %q", i, paths[i], got[i], want[i])
		}
	}
}

// FuzzCleanPath 检查清理结果始终安全（设计方案 11：路径清理必须做 fuzz 测试）。
func FuzzCleanPath(f *testing.F) {
	for _, s := range []string{"a.txt", "../x", "a/b/../../c", `C:\x`, "CON.txt", "a/./b//c", "😀/. .", "\x00", "nul .txt"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := CleanPath(in)
		if err != nil {
			return
		}
		if !utf8.ValidString(out) || len(out) > maxPathBytes {
			t.Fatalf("CleanPath(%q) = %q: invalid UTF-8 or too long", in, out)
		}
		if !filepath.IsLocal(filepath.FromSlash(out)) {
			t.Fatalf("CleanPath(%q) = %q: not a local path", in, out)
		}
		for _, c := range strings.Split(out, "/") {
			if c == "" || c == "." || c == ".." || len(c) > maxComponentBytes ||
				strings.HasSuffix(c, ".") || strings.HasSuffix(c, " ") || isReservedName(c) ||
				strings.ContainsAny(c, "<>:\"\\|?*\x7f") || strings.IndexFunc(c, func(r rune) bool { return r < 0x20 }) >= 0 {
				t.Fatalf("CleanPath(%q) = %q: bad component %q", in, out, c)
			}
		}
		if again, err := CleanPath(out); err != nil || again != out {
			t.Fatalf("CleanPath not idempotent: %q → %q → %q, %v", in, out, again, err)
		}
	})
}

// FuzzUniqueName 检查任何清理后的路径重名时都能得到不同、且每段不超过 255 字节的新名字。
func FuzzUniqueName(f *testing.F) {
	f.Add("a.txt", false)
	f.Add("a."+strings.Repeat("x", 253), false)
	f.Add("照片", true)
	f.Fuzz(func(t *testing.T, in string, isDir bool) {
		p, err := CleanPath(in)
		if err != nil {
			return
		}
		got, err := uniqueName(p, isDir, func(s string) bool { return s == p })
		if err != nil || got == p {
			t.Fatalf("uniqueName(%q) = %q, %v", p, got, err)
		}
		for _, c := range strings.Split(got, "/") {
			if len(c) > maxComponentBytes || !utf8.ValidString(c) {
				t.Fatalf("uniqueName(%q) = %q: bad component", p, got)
			}
		}
	})
}

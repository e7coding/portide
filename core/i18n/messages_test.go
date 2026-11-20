package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// translated 是已经翻译、受检查的目录（相对仓库根目录）：给用户看的中文（包括命令行输出和日志）都要包在 T / Tf / New 里。
var translated = []string{"app", "desktop", "mobile", "cmd", "core/appdir", "core/transfer", "core/node", "core/relay", "core/relayserver", "core/update"}

// partial 是只翻译了一部分的目录：这里只检查 T / Tf / New 的键都有译文，不检查漏包的中文。
var partial = []string{}

var verbs = regexp.MustCompile(`%[-+# 0]*[0-9.]*[a-zA-Z%]`)

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

type scan struct {
	keys      map[string]string // 键 → 位置
	leftovers []string
}

func scanDir(t *testing.T, root, dir string, strict bool, s *scan) {
	t.Helper()
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != filepath.Join(root, dir) && (strings.HasPrefix(d.Name(), ".") || d.Name() == "frontend" || d.Name() == "build" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		ignored := map[int]bool{}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.Contains(c.Text, "i18n-ignore") {
					ignored[fset.Position(c.Pos()).Line] = true
				}
			}
		}
		allowed := map[*ast.BasicLit]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Field:
				if n.Tag != nil {
					allowed[n.Tag] = true
				}
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "i18n" && (sel.Sel.Name == "T" || sel.Sel.Name == "Tf" || sel.Sel.Name == "New") && len(n.Args) > 0 {
					if lit, ok := n.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						key, _ := strconv.Unquote(lit.Value)
						s.keys[key] = fset.Position(lit.Pos()).String()
						allowed[lit] = true
					}
				}
			}
			return true
		})
		if !strict {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || allowed[lit] {
				return true
			}
			pos := fset.Position(lit.Pos())
			if v, _ := strconv.Unquote(lit.Value); hasHan(v) && !ignored[pos.Line] {
				rel, _ := filepath.Rel(root, pos.Filename)
				s.leftovers = append(s.leftovers, rel+":"+strconv.Itoa(pos.Line)+": "+lit.Value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestMessagesTranslated 检查翻译过的目录：每个 T / Tf / New 的键都有英文译文、占位符一致；给用户看的中文都包在 T / Tf / New 里。
func TestMessagesTranslated(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	s := &scan{keys: map[string]string{}}
	for _, d := range translated {
		scanDir(t, root, d, true, s)
	}
	for _, d := range partial {
		scanDir(t, root, d, false, s)
	}
	for _, l := range s.leftovers {
		t.Errorf("没有包 i18n.T / Tf / New 的中文：%s", l)
	}
	for key, pos := range s.keys {
		tr, ok := en[key]
		if !ok {
			t.Errorf("%s：没有英文译文：%q", pos, key)
			continue
		}
		if a, b := strings.Join(verbs.FindAllString(key, -1), " "), strings.Join(verbs.FindAllString(tr, -1), " "); a != b {
			t.Errorf("%s：译文的占位符 %q 和原文 %q 不一致", pos, b, a)
		}
		if hasHan(tr) {
			t.Errorf("%s：译文里有中文：%q", pos, tr)
		}
	}
	for key := range en {
		if _, ok := s.keys[key]; !ok {
			t.Errorf("用不到的译文：%q", key)
		}
	}
	if len(s.keys) < 100 {
		t.Errorf("只找到 %d 个键，扫描的目录可能不对", len(s.keys))
	}
}

func TestLanguage(t *testing.T) {
	defer SetLanguage(Chinese)
	for tag, want := range map[string]string{"zh-CN": Chinese, "zh_CN.UTF-8": Chinese, "zh-Hans-CN": Chinese, "en-US": English, "fr": English, "": English} {
		if got := Normalize(tag); got != want {
			t.Errorf("Normalize(%q) = %q，应为 %q", tag, got, want)
		}
	}
	err := New("口令不对")
	SetLanguage(English)
	if err.Error() != en["口令不对"] {
		t.Errorf("New 的错误没有按取用时的语言翻译：%q", err.Error())
	}
	SetLanguage(Chinese)
	if err.Error() != "口令不对" {
		t.Errorf("切回中文后应为原文：%q", err.Error())
	}
	if FromSystem(func(k string) string { return map[string]string{"LANG": "C", "LC_ALL": ""}[k] }) != "" {
		t.Error(`LANG=C 应当当作没有设置`)
	}
}

func TestListAndConcat(t *testing.T) {
	defer SetLanguage(Chinese)
	names := []string{"甲", "乙"}
	if got := List(names); got != "甲、乙" {
		t.Errorf("中文 List = %q", got)
	}
	if got := Concat([]string{"「甲」", "「乙」"}); got != "「甲」「乙」" {
		t.Errorf("中文 Concat = %q", got)
	}
	SetLanguage(English)
	if got := List([]string{"Mac", "Phone"}); got != "Mac, Phone" {
		t.Errorf("English List = %q", got)
	}
	if got := Concat([]string{"Mac", "Phone (needs update)"}); got != "Mac, Phone (needs update)" {
		t.Errorf("English Concat = %q", got)
	}
}

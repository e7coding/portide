package relayserver

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// importPath 是本包的导入路径。
const importPath = "portide/core/relayserver"

// allowedImporters 是可以导入本包的目录（相对仓库根目录）：本包自己和中继的启动程序。
// 其余代码都是客户端（Apache-2.0），只要有一个非测试文件导入本包，客户端程序就会链接 AGPL 代码。
var allowedImporters = []string{"core/relayserver", "cmd/portide-relay"}

// TestClientsDoNotImportRelayServer 扫描整个仓库（包括 desktop、mobile 这些单独的模块）的非测试 Go 文件，
// 确认除 allowedImporters 外没有谁导入本包（见 CONTRIBUTING.md）。只看直接导入就够了：
// 客户端要间接依赖本包，中间总有一个非测试文件直接导入它，而 cmd/portide-relay 是 main 包，不能被导入。
func TestClientsDoNotImportRelayServer(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if rel != "." && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" || name == "spikes") {
				return filepath.SkipDir
			}
			for _, a := range allowedImporters {
				if rel == a {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		scanned[rel] = true
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == importPath {
				t.Errorf("%s 导入了 %s：客户端代码不得导入中继服务端（AGPL-3.0），两端共用的协议放进 core/relay", rel, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 防止根目录算错、漏扫了单独的模块时照样通过：每类客户端各挑一个文件，必须扫到。
	for _, f := range []string{"cmd/portide/main.go", "desktop/main.go", "mobile/mobile.go", "core/node/daemon.go"} {
		if !scanned[f] {
			t.Errorf("没有扫描到 %s，仓库根目录 %s 可能不对", f, root)
		}
	}
}

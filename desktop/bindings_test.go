package main

import (
	"hash/fnv"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"portide/app"
)

// 界面按编号调用 Go 方法：编号是"包路径.类型.方法"的 FNV-1a 哈希（Wails 在注册服务时算）。
// 包路径或类型改了名而 frontend/bindings 没有重新生成时，界面的每次调用都会报
// "unknown bound method id"，窗口一直转圈（0.5.0 就是这样：改名后 app.Service 的编号还是按 ferry/app 算的）。
// 重新生成：见 docs/桌面端.md 里的 wails3 generate bindings。
var bindingCall = regexp.MustCompile(`export function (\w+)\([^)]*\)[^{]*\{\s*return \$Call\.ByID\((\d+)`)

func TestBindingIDsMatchServices(t *testing.T) {
	for _, c := range []struct {
		file string
		svc  any
	}{
		{"frontend/bindings/portide/app/service.ts", &app.Service{}},
		{"frontend/bindings/portide/desktop/platform.ts", &Platform{}},
	} {
		src, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		typ := reflect.TypeOf(c.svc).Elem()
		pkg := typ.PkgPath()
		if pkg == "portide/desktop" {
			pkg = "main" // 应用里本包是 main 包；测试二进制里它的包路径才是 portide/desktop
		}
		calls := bindingCall.FindAllStringSubmatch(string(src), -1)
		if len(calls) == 0 {
			t.Fatalf("%s: no bound calls found", c.file)
		}
		for _, m := range calls {
			h := fnv.New32a()
			h.Write([]byte(pkg + "." + typ.Name() + "." + m[1]))
			if got := h.Sum32(); m[2] != strconv.FormatUint(uint64(got), 10) {
				t.Errorf("%s: %s calls id %s, but Go registers %d; regenerate frontend/bindings", c.file, m[1], m[2], got)
			}
		}
	}
}

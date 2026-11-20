// Package fixtures 检查 iOS 测试用的 JSON 样例（ios/PortideTests/Fixtures.json）和 Go 结构体的 JSON 字段是否一致（M4）。
// Swift 的 Codable 缺字段就解码失败，所以两边的字段必须完全对应。
package fixtures

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Check 读取 path，对 types 里的每一项检查样例对象的字段（逐层，包括数组里的对象）和 Go 类型的 JSON 字段完全相同。
func Check(t *testing.T, path string, types map[string]any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("没有 %s（公开仓库不含 iOS 代码）", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for name, v := range types {
		raw, ok := all[name]
		if !ok {
			t.Errorf("%s: no fixture %q", path, name)
			continue
		}
		var obj any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		compare(t, name, reflect.TypeOf(v), obj)
	}
}

func compare(t *testing.T, where string, typ reflect.Type, val any) {
	t.Helper()
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := val.(map[string]any)
		if !ok {
			t.Errorf("%s: want an object, fixture has %T", where, val)
			return
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !f.IsExported() || name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
		}
		if got, want := keys(obj), keysOf(fields); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: fixture fields %v, Go fields %v", where, got, want)
			return
		}
		for name, ft := range fields {
			compare(t, where+"."+name, ft, obj[name])
		}
	case reflect.Slice:
		arr, ok := val.([]any)
		if !ok {
			t.Errorf("%s: want an array, fixture has %T", where, val)
			return
		}
		for i, e := range arr {
			compare(t, where+"["+strconv.Itoa(i)+"]", typ.Elem(), e)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOf(m map[string]reflect.Type) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

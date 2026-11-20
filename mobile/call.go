package mobile

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// Call 按名字调用服务层（portide/app 的 Service）的一个导出方法，例如 Call("Timeline", `["", 200]`)。
// argsJSON 是参数组成的 JSON 数组（没有参数时可以为空串）。返回一个 JSON 对象：成功时是 {"result": 方法的结果}
// （没有结果时为 null），失败时是 {"error": "原因"}，原因已经是给用户看的中文。
//
// 不返回 (string, error)：gomobile 把它生成为返回非空字符串的 Objective-C 方法，Swift 是否把它当成 throws
// 不好确定；一个 JSON 信封在 Swift 里的样子是确定的。
func (a *App) Call(method, argsJSON string) string {
	var env struct {
		Result json.RawMessage `json:"result,omitempty"`
		Error  string          `json:"error,omitempty"`
	}
	out, err := a.call(method, argsJSON)
	if err != nil {
		env.Error = err.Error()
	} else {
		env.Result = json.RawMessage(out)
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func (a *App) call(method, argsJSON string) (string, error) {
	m := reflect.ValueOf(a.svc).MethodByName(method)
	if !m.IsValid() {
		return "", fmt.Errorf("mobile: unknown method %q", method)
	}
	t := m.Type()
	var raw []json.RawMessage
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
			return "", fmt.Errorf("mobile: %s: arguments must be a JSON array: %w", method, err)
		}
	}
	if len(raw) != t.NumIn() {
		return "", fmt.Errorf("mobile: %s takes %d arguments, got %d", method, t.NumIn(), len(raw))
	}
	in := make([]reflect.Value, t.NumIn())
	for i := range in {
		v := reflect.New(t.In(i))
		if err := json.Unmarshal(raw[i], v.Interface()); err != nil {
			return "", fmt.Errorf("mobile: %s: argument %d: %w", method, i+1, err)
		}
		in[i] = v.Elem()
	}
	var result any
	for _, out := range m.Call(in) {
		if out.Type() == errorType {
			if !out.IsNil() {
				return "", out.Interface().(error)
			}
			continue
		}
		if result == nil {
			result = out.Interface()
		}
	}
	b, err := encodeResult(result)
	if err != nil {
		return "", errors.New("mobile: cannot encode the result")
	}
	return string(b), nil
}

// encodeResult 把方法的结果编码成 JSON。空切片在 Go 里可能是 nil、会被编码成 null，Swift 解码数组时要的是 []。
func encodeResult(result any) ([]byte, error) {
	if v := reflect.ValueOf(result); v.Kind() == reflect.Slice && v.IsNil() {
		result = reflect.MakeSlice(v.Type(), 0, 0).Interface()
	}
	return json.Marshal(result)
}

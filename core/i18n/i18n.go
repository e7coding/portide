// Package i18n 把给用户看的中文提示换成当前界面语言。
//
// 中文原文就是键：代码里照旧写中文，外面包一层 T / Tf / New，英文译文放在 en.go 的 en 表里。
// 没有译文的句子原样显示中文；TestMessagesTranslated 保证调用处的每个键都有译文。
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// 支持的界面语言。
const (
	Chinese = "zh"
	English = "en"
)

var lang atomic.Value

func init() { lang.Store(Chinese) }

// SetLanguage 设置界面语言。tag 是 "zh"、"zh-CN"、"en-US" 这样的语言标签；中文以外的语言一律用英文。
func SetLanguage(tag string) { lang.Store(Normalize(tag)) }

// Language 返回当前界面语言（Chinese 或 English）。
func Language() string { return lang.Load().(string) }

// Normalize 把语言标签归到支持的语言：zh 开头的是中文，其余（包括空）是英文。
func Normalize(tag string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(tag)), "zh") {
		return Chinese
	}
	return English
}

// T 返回 zh 在当前语言下的文字。
func T(zh string) string {
	if Language() == English {
		if s, ok := en[zh]; ok {
			return s
		}
	}
	return zh
}

// Tf 先翻译格式串再填参数：Tf("最多 %d 次", n)。译文里的占位符要和原文一致。
func Tf(zh string, args ...any) string { return fmt.Sprintf(T(zh), args...) }

// New 返回一个错误，每次取 Error() 时才按当时的语言翻译，适合包级别的错误变量。
func New(zh string, args ...any) error { return &msgError{zh: zh, args: args} }

type msgError struct {
	zh   string
	args []any
}

func (e *msgError) Error() string {
	if len(e.args) == 0 {
		return T(e.zh)
	}
	return Tf(e.zh, e.args...)
}

// FromSystem 从环境变量（LC_ALL、LC_MESSAGES、LANG）推断命令行的语言，都没设置时返回空。
func FromSystem(getenv func(string) string) string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(k); v != "" && v != "C" && v != "POSIX" {
			return v
		}
	}
	return ""
}

// List 把几项连成一句：中文用顿号（甲、乙），英文用逗号（A, B）。
func List(items []string) string {
	if Language() == English {
		return strings.Join(items, ", ")
	}
	return strings.Join(items, "、")
}

// Concat 把已经各自带了引号的几项连起来（每项用 Tf("「%s」", 名字) 得到）：中文直接相连（「甲」「乙」），英文用逗号。
func Concat(items []string) string {
	if Language() == English {
		return strings.Join(items, ", ")
	}
	return strings.Join(items, "")
}

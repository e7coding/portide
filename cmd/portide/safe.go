package main

import (
	"fmt"
	"strings"
	"unicode"
)

// terminalSafe 把对方可控的内容（文本、设备名、文件名、原因）转成可以直接打印到终端的形式：
// 控制字符（包括 ESC，否则对方能借 ANSI / OSC 序列改写屏幕或标题）和双向排版控制符
// 显示成 \x1b、\u202e 这样的转义；multiline 为 true 时保留换行和制表符。
func terminalSafe(s string, multiline bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case multiline && (r == '\n' || r == '\t'):
			b.WriteRune(r)
		case unicode.IsControl(r) && r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

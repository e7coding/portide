package main

import (
	"os/exec"
	"strings"
)

// platformLanguage 读 macOS 的首选语言（系统设置 › 通用 › 语言与地区里排第一的）。
func platformLanguage() string {
	out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	// 输出形如 (\n    "zh-Hans-CN",\n    "en-CN"\n)
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.Trim(strings.TrimSpace(line), `",`); s != "" && s != "(" && s != ")" {
			return s
		}
	}
	return ""
}

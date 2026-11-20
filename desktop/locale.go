package main

import (
	"os"

	"portide/core/i18n"
)

// resolveLanguage 把设置里的语言（空表示跟随系统）换成界面实际使用的语言（i18n.Chinese 或 i18n.English）。
func resolveLanguage(setting string) string {
	if setting == i18n.Chinese || setting == i18n.English {
		return setting
	}
	return i18n.Normalize(systemLanguage())
}

// systemLanguage 返回系统的界面语言标签，例如 "zh-CN"、"en-US"；取不到时返回空（按英文处理）。
// 先看环境变量（Linux 桌面和从终端启动时有），再按平台问系统。
func systemLanguage() string {
	if v := i18n.FromSystem(os.Getenv); v != "" {
		return v
	}
	return platformLanguage()
}

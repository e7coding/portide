// Package version 是 Portide 的版本号：唯一的来源是同目录下的 VERSION 文件（语义化版本，例如 0.5.0 或 0.6.0-beta.1）。
// Go 程序编译时直接嵌入它；构建脚本（scripts/）、桌面端界面和 iOS 工程也读这个文件，所以发版时只改这一处。
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// Version 是当前版本，例如 "0.5.0"。
var Version = strings.TrimSpace(raw)

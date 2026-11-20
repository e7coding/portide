//go:build !linux || server

package main

// trayAvailable：Windows 和 macOS 总有托盘（菜单栏）；server 模式没有。
func trayAvailable() bool { return !serverMode }

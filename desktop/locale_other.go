//go:build !darwin && !windows

package main

// platformLanguage 在 Linux 上只看环境变量（见 systemLanguage），这里没有别的来源。
func platformLanguage() string { return "" }

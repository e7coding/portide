// Package transfer 实现设备之间的文件、文件夹和文本传输（设计方案 5.1、5.2）：
// 报价与应答、按文件分流、BLAKE3 校验、断点续传和接收端的路径清理。
package transfer

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

// 路径限制。
const (
	maxComponentBytes = 255  // 单个文件名的字节数（大多数文件系统的上限）
	maxPathBytes      = 4096 // 整个相对路径的字节数
	maxDepth          = 64   // 目录层数
)

var errBadPath = errors.New("transfer: unsafe or empty path")

// CleanPath 把对端给出的相对路径（"/" 分隔）清理成可以安全落盘的形式（设计方案 5.2 路径清理）：
//   - 拒绝绝对路径、盘符、".." 和清理后为空的路径；
//   - 去掉空的和 "." 组成部分；
//   - 每个组成部分：非法字符和控制字符换成 "_"，去掉结尾的点和空格，Windows 保留名前加 "_"，超长时截短。
//
// 结果在 Windows、macOS、Linux 上都是合法的相对路径，并且再次清理不会改变。
func CleanPath(p string) (string, error) {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", errBadPath
	}
	var parts []string
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." {
			continue
		}
		if c == ".." {
			return "", errBadPath
		}
		if len(parts) == 0 && isDriveRoot(c) {
			return "", errBadPath // 盘符，例如 "C:" 或 `C:\Windows`
		}
		parts = append(parts, cleanComponent(c))
	}
	if len(parts) == 0 || len(parts) > maxDepth {
		return "", errBadPath
	}
	out := strings.Join(parts, "/")
	if len(out) > maxPathBytes {
		return "", errBadPath
	}
	return out, nil
}

// isDriveRoot 判断路径的第一个组成部分是不是 Windows 盘符（"C:"，或用反斜杠写的 `C:\...`）。
// 像 "a:b.txt" 这样的普通文件名不算：冒号会在清理时换成 "_"。
func isDriveRoot(c string) bool {
	return len(c) >= 2 && c[1] == ':' && isASCIILetter(c[0]) && (len(c) == 2 || c[2] == '\\')
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

// cleanComponent 清理路径中的一个组成部分，结果一定非空。
func cleanComponent(c string) string {
	c = strings.ToValidUTF8(c, "_")
	c = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, c)
	c = truncateName(c, maxComponentBytes)
	c = strings.TrimRight(c, ". ")
	if c == "" {
		return "_"
	}
	if isReservedName(c) {
		c = strings.TrimRight(truncateName("_"+c, maxComponentBytes), ". ")
	}
	return c
}

// isReservedName 判断是否为 Windows 保留的设备名（不区分大小写，带扩展名也算，例如 "con.txt"）。
func isReservedName(c string) bool {
	base := c
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
		return true
	}
	// COM¹ COM² COM³ LPT¹ LPT² LPT³ 也是保留名。
	if strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT") {
		switch base[3:] {
		case "¹", "²", "³":
			return true
		}
	}
	return false
}

// truncateName 把名字截到 limit 字节以内，尽量保留扩展名，并且不截断多字节字符。
func truncateName(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	stem, ext := splitExt(name)
	if len(ext) > 32 || len(ext) >= limit {
		stem, ext = name, ""
	}
	return truncateUTF8(stem, limit-len(ext)) + ext
}

func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// splitExt 把文件名分成主干和扩展名（含点）；以点开头的名字（例如 ".bashrc"）没有扩展名。
func splitExt(name string) (stem, ext string) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return name, ""
	}
	return name[:i], name[i:]
}

// uniqueName 在 taken 报告重名时依次尝试 "名称 (1).扩展名"、"名称 (2).扩展名"……；文件夹不拆扩展名。
func uniqueName(name string, isDir bool, taken func(string) bool) (string, error) {
	if !taken(name) {
		return name, nil
	}
	dir, base := path.Split(name)
	stem, ext := splitExt(base)
	if isDir || len(ext) > 32 {
		stem, ext = base, "" // 与 truncateName 一致：太长的"扩展名"不当扩展名
	}
	for i := 1; i <= 9999; i++ {
		suffix := fmt.Sprintf(" (%d)", i)
		cand := dir + truncateUTF8(stem, maxComponentBytes-len(suffix)-len(ext)) + suffix + ext
		if !taken(cand) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("transfer: no free name for %q", name)
}

// foldKey 是比较路径是否冲突时用的键：Windows 和 macOS 的文件系统默认不区分大小写。
func foldKey(p string) string { return strings.ToLower(p) }

// resolvePaths 把一次报价中已清理的相对路径映射为保存目录下实际使用的路径：
//   - 顶层名字与保存目录里已有的文件或文件夹重名时改名，同一文件夹下的条目跟着这个新名字走；
//   - 同一报价里只差大小写、或者文件和文件夹同名的路径也改名，避免互相覆盖。
//
// existsTop 报告保存目录顶层是否已有这个名字。isDir[i] 表示 paths[i] 是文件夹。
func resolvePaths(paths []string, isDir []bool, existsTop func(string) bool) ([]string, error) {
	dirs := map[string]string{} // 清理后文件夹路径的 foldKey → 实际路径
	taken := map[string]bool{}  // 已占用的实际路径的 foldKey
	isTaken := func(top bool) func(string) bool {
		return func(p string) bool { return taken[foldKey(p)] || (top && existsTop(p)) }
	}
	// resolveDir 返回清理后文件夹路径 p 的实际路径，必要时依次创建各级映射。
	var resolveDir func(p string) (string, error)
	resolveDir = func(p string) (string, error) {
		if r, ok := dirs[foldKey(p)]; ok {
			return r, nil
		}
		parent, base := path.Split(p)
		name := base
		if parent != "" {
			rp, err := resolveDir(strings.TrimSuffix(parent, "/"))
			if err != nil {
				return "", err
			}
			name = rp + "/" + base
		}
		r, err := uniqueName(name, true, isTaken(parent == ""))
		if err != nil {
			return "", err
		}
		dirs[foldKey(p)] = r
		taken[foldKey(r)] = true
		return r, nil
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		if isDir[i] {
			r, err := resolveDir(p)
			if err != nil {
				return nil, err
			}
			out[i] = r
			continue
		}
		parent, base := path.Split(p)
		name := base
		if parent != "" {
			rp, err := resolveDir(strings.TrimSuffix(parent, "/"))
			if err != nil {
				return nil, err
			}
			name = rp + "/" + base
		}
		r, err := uniqueName(name, false, isTaken(parent == ""))
		if err != nil {
			return nil, err
		}
		taken[foldKey(r)] = true
		out[i] = r
	}
	return out, nil
}

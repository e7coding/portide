package app

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"portide/core/i18n"
)

// 首次启动时建议的设备名（M3c 设计 4.5）："用户名 的 笔记本 / 台式机"，类型按主机猜；另外给出主机名作为一键选项。

// nameSuggestions 返回建议的设备名，第一个是默认值。
func nameSuggestions() []string {
	host, _ := os.Hostname()
	return suggestNames(host, userName(), hasBattery())
}

// suggestNames 按主机名和有没有电池猜笔记本还是台式机：Windows 默认的主机名就是 LAPTOP-… 或 DESKTOP-…。
func suggestNames(host, userName string, battery bool) []string {
	laptop := battery
	switch h := strings.ToUpper(host); {
	case strings.HasPrefix(h, "LAPTOP-"), strings.Contains(h, "BOOK"), strings.Contains(h, "THINKPAD"):
		laptop = true
	case strings.HasPrefix(h, "DESKTOP-"):
		laptop = false
	}
	kinds := []string{i18n.T("台式机"), i18n.T("笔记本")}
	if laptop {
		kinds = []string{i18n.T("笔记本"), i18n.T("台式机")}
	}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || validName(s) != nil {
			return
		}
		for _, x := range out {
			if x == s {
				return
			}
		}
		out = append(out, s)
	}
	for _, k := range kinds {
		if userName != "" {
			add(i18n.Tf("%s 的%s", userName, k))
		} else {
			add(k)
		}
	}
	add(host)
	return out
}

// userName 返回当前用户名（去掉 Windows 的域名部分）。
func userName() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	name := u.Username
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// hasBattery 报告这台机器有没有电池（只在 Linux 上能简单判断；别的平台按主机名猜）。
func hasBattery() bool {
	matches, _ := filepath.Glob("/sys/class/power_supply/BAT*")
	return len(matches) > 0
}

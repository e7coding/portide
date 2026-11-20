//go:build linux && !server

package main

import "github.com/godbus/dbus/v5"

// trayAvailable 检查桌面有没有托盘：Linux 的托盘图标要注册到 StatusNotifierWatcher（KDE 自带；
// GNOME 要装 AppIndicator 扩展，Ubuntu 默认装了）。没有的话关闭窗口就退出（M3b 设计 3.2）。
func trayAvailable() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"portide/app"
	"portide/core/i18n"
	"portide/core/store"
)

//go:embed build/icons/64.png
var trayPNG []byte

//go:embed build/icons/tray-active-64.png
var trayActivePNG []byte

//go:embed build/icons/256.png
var appIcon []byte

// trayNoticeFile 记下"已经提示过缩到托盘"（M3b 设计 3.2：只提示一次），放在数据目录里。
const trayNoticeFile = "desktop-tray-notice"

// tray 是托盘图标和菜单（M3c 设计 4.6）：状态行和传输中的第一项（不能点）、打开、发送文件…、
// 暂停全部 / 全部继续、退出。传输中图标右下角带一个绿点。
type tray struct {
	mu     sync.Mutex // refresh 会从变化信号和启动时同时调用：串行化（M3b 遗留的并发刷新问题）
	status *application.MenuItem
	detail *application.MenuItem
	pause  *application.MenuItem
	open   *application.MenuItem
	send   *application.MenuItem
	quit   *application.MenuItem
	menu   *application.Menu
	icon   *application.SystemTray
	active bool // 当前显示的是传输中的图标
	paused bool // 暂停菜单项当前是"全部继续"
}

func newTray(wapp *application.App, svc *app.Service, show, sendFiles func()) *tray {
	t := &tray{menu: wapp.NewMenu()}
	t.status = t.menu.Add(i18n.T("空闲")).SetEnabled(false)
	t.detail = t.menu.Add("").SetEnabled(false).SetHidden(true)
	t.menu.AddSeparator()
	t.open = t.menu.Add(i18n.T("打开 Portide")).OnClick(func(*application.Context) { show() })
	t.send = t.menu.Add(i18n.T("发送文件…")).OnClick(func(*application.Context) { sendFiles() })
	t.pause = t.menu.Add(i18n.T("暂停全部")).SetEnabled(false).OnClick(func(*application.Context) {
		t.mu.Lock()
		resume := t.paused
		t.mu.Unlock()
		if resume {
			_, _ = svc.ResumeAll()
		} else {
			_, _ = svc.PauseAll()
		}
	})
	t.menu.AddSeparator()
	t.quit = t.menu.Add(i18n.T("退出")).OnClick(func(*application.Context) { wapp.Quit() })
	t.icon = wapp.SystemTray.New().SetIcon(trayPNG).SetMenu(t.menu)
	t.icon.SetTooltip("Portide")
	t.icon.OnClick(show)
	return t
}

// relabel 按当前界面语言重写固定的菜单项（状态行和暂停项由 refresh 重写）。
func (t *tray) relabel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.open.SetLabel(i18n.T("打开 Portide"))
	t.send.SetLabel(i18n.T("发送文件…"))
	t.quit.SetLabel(i18n.T("退出"))
	t.menu.Update()
}

// trayView 是托盘按传输列表算出来的显示内容。
type trayView struct {
	status    string // 例如"2 个正在传输 · 2 台设备在线"
	detail    string // 正在传输时：第一项的名字和进度，例如"季度报告.pdf · 45%"
	active    bool   // 有正在传输的
	canPause  bool   // 有能暂停或继续的发送任务
	allPaused bool   // 未结束的发送任务都已暂停：菜单项显示"全部继续"
}

func viewOf(list app.TransferList, online int) trayView {
	var v trayView
	var first *app.TransferItem
	running := 0
	for i, it := range list.Outgoing {
		if !it.Paused {
			running++
		}
		if first == nil && it.Status == store.SendActive && !it.Paused {
			first = &list.Outgoing[i]
		}
	}
	if first == nil && len(list.Incoming) > 0 {
		first = &list.Incoming[0]
	}
	v.canPause = len(list.Outgoing) > 0
	v.allPaused = v.canPause && running == 0
	v.active = list.Active > 0
	switch {
	case v.active:
		v.status = i18n.Tf("%d 个正在传输", list.Active)
	case list.Count() > 0:
		v.status = i18n.Tf("%d 个等待中", list.Count())
	default:
		v.status = i18n.T("空闲")
	}
	if online > 0 {
		v.status += i18n.Tf(" · %d 台设备在线", online)
	} else {
		v.status += i18n.T(" · 没有设备在线")
	}
	if v.active && first != nil {
		v.detail = first.Summary
		if first.BytesTotal > 0 {
			v.detail += fmt.Sprintf(" · %d%%", first.BytesDone*100/first.BytesTotal)
		}
	}
	return v
}

// onlineCount 是在线的其他设备数。
func onlineCount(svc *app.Service) int {
	devs, err := svc.Devices()
	if err != nil {
		return 0
	}
	n := 0
	for _, d := range devs {
		if !d.Self && d.Online {
			n++
		}
	}
	return n
}

// refresh 按当前的传输更新状态行、暂停菜单项和图标。
func (t *tray) refresh(svc *app.Service) {
	list, err := svc.Transfers()
	online := onlineCount(svc)
	t.mu.Lock()
	defer t.mu.Unlock()
	v := trayView{status: i18n.T("Portide 没有运行")}
	if err == nil {
		v = viewOf(list, online)
	}
	t.status.SetLabel(v.status)
	t.detail.SetLabel(v.detail).SetHidden(v.detail == "")
	t.paused = v.allPaused
	if v.allPaused {
		t.pause.SetLabel(i18n.T("全部继续"))
	} else {
		t.pause.SetLabel(i18n.T("暂停全部"))
	}
	t.pause.SetEnabled(v.canPause)
	t.menu.Update()
	if v.active != t.active {
		t.active = v.active
		if v.active {
			t.icon.SetIcon(trayActivePNG)
		} else {
			t.icon.SetIcon(trayPNG)
		}
	}
	t.icon.SetTooltip("Portide · " + v.status)
}

// firstHide 报告这是不是第一次缩到托盘，并记下来。
func firstHide(dataDir string) bool {
	path := filepath.Join(dataDir, trayNoticeFile)
	if _, err := os.Stat(path); err == nil {
		return false
	}
	_ = os.WriteFile(path, nil, 0o600)
	return true
}

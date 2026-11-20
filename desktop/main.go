// Portide 桌面应用（M3a、M3b、M3c）：在进程内运行 Portide 核心，用 Wails v3 显示 React 界面，常驻托盘。
// 对 Wails 的调用都集中在 main 包（main.go、platform.go、tray.go、notify.go），服务层 portide/app 不引用 Wails（M3a 设计 7）。
//
// Linux 上用 GTK3 + webkit2gtk-4.1，构建时要加 -tags gtk3（Wails v3 beta 默认用 GTK4）。
package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"log"
	"os"
	"slices"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"portide/app"
	"portide/core/appdir"
	"portide/core/i18n"
	"portide/core/node"
	"portide/core/store"
	"portide/core/update"
)

// 转发给界面的事件。
const (
	ChangedEvent      = "portide:changed"       // 变化信号，数据是 store.Change* 主题名
	FilesDroppedEvent = "portide:files-dropped" // 文件拖到了窗口上（或托盘里选了文件），数据是 dropPayload
	ShowTimelineEvent = "portide:show-timeline" // 点了通知：界面切到时间线
)

// dropPayload 是 FilesDroppedEvent 的内容：Peer 是落点元素的 data-peer 属性（左侧某台设备的那一行），
// 为空时界面发给当前会话的设备（M3c 设计 4.1）。
type dropPayload struct {
	Paths []string `json:"paths"`
	Peer  string   `json:"peer"`
}

// hiddenFlag：开机启动时只在托盘里启动，不打开窗口（M3b 设计 3.5）。
const hiddenFlag = "--hidden"

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	dir, err := appdir.DataDir("")
	if err != nil {
		log.Fatal(err)
	}
	openLogFile(dir)
	hidden := slices.Contains(os.Args[1:], hiddenFlag)
	withTray := trayAvailable()

	// 核心在 Wails 应用创建之前就可能发出变化信号；那时还没有界面，直接丢掉。
	var current atomic.Pointer[application.App]
	var trayIcon atomic.Pointer[tray]
	var svc *app.Service
	var gate quitGate
	changes := app.NewCoalescer(app.ChangeWindow, gate.wrap(func(topic string) {
		if a := current.Load(); a != nil {
			a.Event.Emit(ChangedEvent, topic)
		}
		if t := trayIcon.Load(); t != nil && (topic == store.ChangeTransfers || topic == store.ChangeDevices) {
			t.refresh(svc)
		}
	}))
	var notes *notifier
	svc = app.New(app.Config{
		DataDir:  dir,
		OnChange: changes.Signal,
		OnEvent: func(e node.Event) {
			if notes != nil {
				notes.send(e)
			}
		},
		Open:   func(path string) error { return current.Load().Browser.OpenFile(path) },
		Reveal: func(path string) error { return current.Load().Env.OpenFileManager(path, true) },
	})
	platform := &Platform{prefs: newPrefsStore(dir, withTray), updates: update.NewChecker(dir)}
	// 先定下界面语言：核心启动时的提示、托盘和通知都要用。
	i18n.SetLanguage(platform.prefs.load().UILanguage)

	services := []application.Service{application.NewService(svc), application.NewService(platform)}
	if !serverMode {
		ns := notifications.New()
		services = append(services, application.NewService(ns))
		notes = &notifier{ns: ns, svc: svc}
	}

	var window *application.WebviewWindow
	winState, winSaved := loadWindowState(dir)
	rememberWindow := func() {
		if window != nil {
			winState = captureWindow(window, winState)
			saveWindowState(dir, winState)
		}
	}
	show := func() {
		if window != nil {
			window.Show()
			window.Restore()
			window.Focus()
		}
	}
	wapp := application.New(application.Options{
		Name:        "Portide",
		Description: "Private file transfer",
		Icon:        appIcon,
		Services:    services,
		Assets:      application.AssetOptions{Handler: application.BundledAssetFileServer(assets)},
		// 同一个数据目录只能有一个桌面应用实例；再次启动时把已有的窗口提到前面。
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID:               "org.portide.desktop." + instanceKey(dir),
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { show() },
		},
		Mac: application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: !withTray},
		// 收尾在主线程里执行（macOS 和 Linux 都是）：先停掉变化信号的转发（转发要回主线程刷新托盘，见 quitGate），
		// 停核心最多等 stopWait，免得哪里卡住时整个应用"无响应"、退不出去。
		OnShutdown: func() {
			gate.begin(changes.Close)
			rememberWindow()
			stopWithin(stopWait, func() { app.Stop(svc) })
		},
	})
	current.Store(wapp)
	platform.app = wapp
	if notes != nil {
		notes.show = func() {
			show()
			wapp.Event.Emit(ShowTimelineEvent)
		}
	}

	window = wapp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:          "Portide",
		Width:          winState.Width,
		Height:         winState.Height,
		MinWidth:       minWidth,
		MinHeight:      minHeight,
		URL:            "/",
		Hidden:         hidden && withTray,
		EnableFileDrop: true,
	})
	window.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		if files := e.Context().DroppedFiles(); len(files) > 0 {
			p := dropPayload{Paths: files}
			if t := e.Context().DropTargetDetails(); t != nil {
				p.Peer = t.Attributes["data-peer"]
			}
			wapp.Event.Emit(FilesDroppedEvent, p)
		}
	})
	if withTray {
		// 有托盘时关闭窗口默认只是隐藏（M3b 设计 3.2），设置里可以改成退出（M3c 设计 4.4）；
		// 第一次隐藏时提示一下程序还在后台，打开"每次提示"时每次都提示。没有托盘时照常关闭即退出。
		window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
			e.Cancel()
			rememberWindow()
			prefs := platform.prefs.load()
			if !prefs.CloseToTray {
				wapp.Quit()
				return
			}
			window.Hide()
			if first := firstHide(dir); notes != nil && (first || prefs.HideHint) {
				notes.hint(i18n.T("Portide 仍在后台运行"), i18n.T("可以从托盘图标打开或退出"))
			}
		})
	}

	wapp.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		if notes != nil {
			notes.start()
		}
		restoreWindow(wapp, window, winState, winSaved)
		if withTray {
			// 托盘的"发送文件…"：选完文件后打开窗口，和拖进窗口一样由界面问发给哪台设备。
			sendFiles := func() {
				paths, err := platform.PickFiles()
				if err != nil || len(paths) == 0 {
					return
				}
				show()
				wapp.Event.Emit(FilesDroppedEvent, dropPayload{Paths: paths})
			}
			t := newTray(wapp, svc, show, sendFiles)
			trayIcon.Store(t)
			relabel := func() {
				t.relabel()
				t.refresh(svc)
			}
			platform.onLanguage.Store(&relabel)
		}
		if hidden {
			// 只在托盘里启动时没有界面去触发启动流程：在这里启动核心。需要输入口令（或者还没有初始化、
			// 启动失败）时核心起不来，照样打开窗口（M3b 设计 3.5）。
			if svc.State().Phase != app.PhaseRunning {
				show()
			}
		}
		if t := trayIcon.Load(); t != nil {
			t.refresh(svc)
		}
	})
	if err := wapp.Run(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}

// stopWait 是退出时等核心停下的最长时间；超过就不再等，进程照样退出（数据库是 WAL 模式，中途退出不会损坏）。
const stopWait = 5 * time.Second

// stopWithin 在后台执行 stop，最多等 d；按时结束时返回 true。
func stopWithin(d time.Duration, stop func()) bool {
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		log.Printf(i18n.T("退出时等了 %v 核心还没停下，不再等待"), d)
		return false
	}
}

// instanceKey 由数据目录算出单实例的标识：不同的数据目录（例如测试用的 PORTIDE_HOME）可以同时运行。
// 以字母开头：Linux 上单实例走 D-Bus，名字的每一段都不能以数字开头。
func instanceKey(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return "d" + hex.EncodeToString(sum[:8])
}

// Package mobile 是 iOS 应用经 gomobile 调用的 Portide 核心入口（M4 设计 3）。Swift 里看到的名字带 Mobile 前缀，
// 例如 MobileNew、MobileApp。
//
// gomobile 只能传字符串、整数、布尔、字节数组和接口，所以服务层（portide/app）的方法统一经 Call 按名字调用，
// 参数和结果都是 JSON；平台能力（Keychain、Bonjour、事件）由 Swift 实现 Platform 接口后传进来。
package mobile

import (
	"encoding/json"
	"errors"

	"portide/app"
	"portide/core/i18n"
	"portide/core/keystore"
	"portide/core/node"
)

// Platform 是 Swift 实现的平台能力。除 KeyGet / KeySet 外的方法都不能阻塞：需要做的事交给主线程或别的队列后立即返回。
type Platform interface {
	// KeyGet 从 Keychain 读一项；不存在时返回空（不是错误）。
	KeyGet(name string) ([]byte, error)
	// KeySet 写一项到 Keychain（kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly，不随 iCloud 同步）。
	KeySet(name string, value []byte) error

	// StartAnnounce 用 Bonjour 发布服务 service（例如 "_portide._udp"），实例名 instance、端口 port，直到 StopAnnounce(id)。
	StartAnnounce(id int64, service, instance string, port int64)
	StopAnnounce(id int64)
	// StartBrowse 浏览服务 service，每解析出一个实例就调用 App.ReportFound(id, …)，直到 StopBrowse(id)。
	StartBrowse(id int64, service string)
	StopBrowse(id int64)

	// OnChange 报告某类数据变了（store.Change* 主题，例如 "transfers"），界面据此重新读取。
	OnChange(topic string)
	// OnEvent 报告值得通知用户的事（JSON，字段见 Event）。
	OnEvent(eventJSON string)
	// Log 输出一行日志。
	Log(line string)
}

// Event 是 OnEvent 的 JSON 内容（对应 node.Event）。
type Event struct {
	Kind      string `json:"kind"` // "received" | "send-failed" | "ask"
	HistoryID int64  `json:"historyId"`
	InboxID   int64  `json:"inboxId"`
	PeerName  string `json:"peerName"`
	Summary   string `json:"summary"`
	Files     int    `json:"files"`
	Error     string `json:"error"`
}

// App 是一个运行中的 Portide（每个进程一个）。
type App struct {
	svc     *app.Service
	changes *app.Coalescer
	disc    *bridge
}

// SetLanguage 设置服务层提示（错误、中继状态等）的语言；tag 是 "zh-Hans"、"en" 这样的语言标签。在 New 之前调用。
func SetLanguage(tag string) { i18n.SetLanguage(tag) }

// New 创建 App；dataDir 是数据目录（Application Support/portide），saveDir 是收到文件的保存目录（Documents）。
// 第一次 Call 时才开始启动流程。
func New(dataDir, saveDir string, p Platform) *App {
	return newApp(dataDir, saveDir, p, "")
}

// newApp 供测试指定监听地址（同一进程里跑两台设备）。
func newApp(dataDir, saveDir string, p Platform, listen string) *App {
	a := &App{disc: newBridge(p)}
	a.changes = app.NewCoalescer(app.ChangeWindow, p.OnChange)
	a.svc = app.New(app.Config{
		DataDir:    dataDir,
		SaveDir:    saveDir,
		ListenAddr: listen,
		Keys:       platformKeys{p},
		Discovery:  a.disc,
		Logf:       func(format string, args ...any) { p.Log(sprintf(format, args...)) },
		OnChange:   a.changes.Signal,
		OnEvent: func(e node.Event) {
			// 约束同 OnChange：不能阻塞，也不能回调核心。
			b, _ := json.Marshal(Event{Kind: e.Kind, HistoryID: e.HistoryID, InboxID: e.InboxID, PeerName: e.PeerName,
				Summary: e.Summary, Files: e.Files, Error: e.Error})
			go p.OnEvent(string(b))
		},
	})
	return a
}

// Suspend 在应用退到后台、延长的时间用完时调用：停止核心并关闭数据库。
func (a *App) Suspend() { app.Suspend(a.svc) }

// Resume 在应用回到前台时调用：重新启动核心，返回启动后的状态（JSON，同 Call("State")）。
func (a *App) Resume() string {
	b, _ := json.Marshal(app.Resume(a.svc))
	return string(b)
}

// Stop 停止核心，之后 App 不能再用。
func (a *App) Stop() {
	app.Stop(a.svc)
	a.changes.Close()
}

// CleanOutbox 删掉发件箱 dir 里不再被未结束任务引用的副本（应用启动、核心运行后调用）。
func (a *App) CleanOutbox(dir string) error {
	return app.CleanOutbox(a.svc, dir)
}

// ReportFound 由 Swift 在浏览到一个实例时调用：browseID 是 StartBrowse 的编号，ipsJSON 是解析出的 IP 地址数组。
func (a *App) ReportFound(browseID int64, instance, ipsJSON string, port int64) {
	var ips []string
	if json.Unmarshal([]byte(ipsJSON), &ips) != nil {
		return
	}
	a.disc.found(browseID, instance, ips, int(port))
}

// platformKeys 把 Platform 的 Keychain 方法适配成 keystore.Store。
type platformKeys struct{ p Platform }

func (k platformKeys) Get(name string) ([]byte, error) {
	v, err := k.p.KeyGet(name)
	if err != nil {
		return nil, err
	}
	if len(v) == 0 {
		return nil, keystore.ErrNotFound
	}
	return v, nil
}

func (k platformKeys) Set(name string, value []byte) error {
	if len(value) == 0 {
		return errors.New("mobile: refusing to store an empty secret")
	}
	return k.p.KeySet(name, value)
}

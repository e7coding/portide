package main

import (
	"fmt"
	"log"
	"strconv"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"portide/app"
	"portide/core/i18n"
	"portide/core/node"
)

// 通知的种类（分类）和按钮（M3b 设计 3.4）。
const (
	categoryFile = "portide-file" // 收到文件：打开 / 打开所在文件夹
	categoryAsk  = "portide-ask"  // 待确认的接收：接受 / 拒绝

	actionOpen   = "open"
	actionReveal = "reveal"
	actionAccept = "accept"
	actionReject = "reject"
)

// notifier 把核心的事件变成系统通知，并处理通知上的按钮。
// 通知的 ID 是种类前缀加编号：h<历史编号>（收到）、f<历史编号>（发送失败）、a<收件箱编号>（待确认）。
type notifier struct {
	ns   *notifications.NotificationService
	svc  *app.Service
	show func() // 打开窗口并显示时间线
}

// start 注册按钮分类和点击回调；在应用启动之后调用（通知服务那时才连上系统）。
func (n *notifier) start() {
	for _, c := range []notifications.NotificationCategory{
		{ID: categoryFile, Actions: []notifications.NotificationAction{{ID: actionOpen, Title: i18n.T("打开")}, {ID: actionReveal, Title: i18n.T("打开所在文件夹")}}},
		{ID: categoryAsk, Actions: []notifications.NotificationAction{{ID: actionAccept, Title: i18n.T("接受")}, {ID: actionReject, Title: i18n.T("拒绝")}}},
	} {
		if err := n.ns.RegisterNotificationCategory(c); err != nil {
			log.Printf(i18n.T("通知：注册按钮失败：%v"), err)
		}
	}
	n.ns.OnNotificationResponse(n.respond)
}

// send 发一条通知。由 node.Options.OnEvent 调用，不能阻塞：真正发送放到新的 goroutine 里。
func (n *notifier) send(e node.Event) {
	go func() {
		var err error
		switch e.Kind {
		case node.EventReceived:
			opts := notifications.NotificationOptions{ID: fmt.Sprintf("h%d", e.HistoryID), Title: i18n.Tf("来自「%s」", e.PeerName), Body: e.Summary}
			if e.Files > 0 {
				opts.CategoryID = categoryFile
				err = n.ns.SendNotificationWithActions(opts)
			} else {
				err = n.ns.SendNotification(opts)
			}
		case node.EventSendFailed:
			err = n.ns.SendNotification(notifications.NotificationOptions{
				ID: fmt.Sprintf("f%d", e.HistoryID), Title: i18n.Tf("没有发给「%s」", e.PeerName), Body: i18n.Tf("%s：%s", e.Summary, e.Error),
			})
		case node.EventAsk:
			err = n.ns.SendNotificationWithActions(notifications.NotificationOptions{
				ID: fmt.Sprintf("a%d", e.InboxID), Title: i18n.Tf("「%s」想发给你", e.PeerName), Body: e.Summary, CategoryID: categoryAsk,
			})
		}
		if err != nil {
			log.Printf(i18n.T("通知：发送失败：%v"), err)
		}
	}()
}

// respond 处理通知上的点击：按钮执行对应的操作，点通知本身打开窗口。
func (n *notifier) respond(r notifications.NotificationResult) {
	if r.Error != nil {
		log.Printf(i18n.T("通知：%v"), r.Error)
		return
	}
	kind, num := parseNoteID(r.Response.ID)
	var err error
	switch {
	case kind == 'h' && r.Response.ActionIdentifier == actionOpen:
		err = n.svc.OpenFile(num, 0)
	case kind == 'h' && r.Response.ActionIdentifier == actionReveal:
		err = n.svc.RevealFile(num, 0)
	case kind == 'a' && r.Response.ActionIdentifier == actionAccept:
		err = n.svc.DecideInbox(num, true)
	case kind == 'a' && r.Response.ActionIdentifier == actionReject:
		err = n.svc.DecideInbox(num, false)
	default:
		n.show()
	}
	if err != nil {
		log.Printf(i18n.T("通知：%v"), err)
	}
}

// parseNoteID 拆开通知 ID（种类前缀加编号）；不是这种格式时 kind 为 0。
func parseNoteID(id string) (kind byte, num int64) {
	if len(id) < 2 {
		return 0, 0
	}
	num, err := strconv.ParseInt(id[1:], 10, 64)
	if err != nil {
		return 0, 0
	}
	return id[0], num
}

// hint 发一条没有按钮的提示。
func (n *notifier) hint(title, body string) {
	go func() {
		if err := n.ns.SendNotification(notifications.NotificationOptions{ID: "hint", Title: title, Body: body}); err != nil {
			log.Printf(i18n.T("通知：发送失败：%v"), err)
		}
	}()
}

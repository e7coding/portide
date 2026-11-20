package node

import (
	"path"
	"unicode/utf8"

	"portide/core/i18n"
	"portide/core/store"
)

// 事件种类（Event.Kind），用于桌面通知（M3b）。
const (
	EventReceived   = "received"    // 收到了文本或文件（全部或部分）
	EventSendFailed = "send-failed" // 发出的任务没有送达（被拒绝、过期、对方停止接收……）
	EventAsk        = "ask"         // 有一个接收在等用户确认
)

// Event 是一件值得通知用户的事。
type Event struct {
	Kind      string
	HistoryID int64  // received / send-failed：对应的历史编号
	InboxID   int64  // ask：等待确认的编号
	PeerName  string // 对方的设备名
	Summary   string // 内容摘要：第一个文件名加数量，或文本开头
	Files     int    // received：收到的文件数
	Error     string // send-failed：没有送达的原因
}

// notifyPreviewRunes 是摘要里文本开头的最大字数。
const notifyPreviewRunes = 40

// Notify 报告是否发出通知事件（设置项，默认开启）。
func (n *Node) Notify() bool {
	v, _, _ := n.store.GetMeta(store.MetaNotify)
	return v != "off"
}

// SetNotify 打开或关闭通知事件。
func (n *Node) SetNotify(on bool) error {
	v := "on"
	if !on {
		v = "off"
	}
	return n.store.SetMeta(store.MetaNotify, v)
}

// emit 在通知开启时调用 Options.OnEvent。约束同 OnChange：不能阻塞、不能回调 Node 或 Store。
func (n *Node) emit(e Event) {
	if n.opts.OnEvent != nil && n.Notify() {
		n.opts.OnEvent(e)
	}
}

// historyEvent 根据刚写入的一条历史决定要不要通知：收到（全部或部分）和发送没有完全送达
// （失败或只送达了一部分）才通知。
func (n *Node) historyEvent(id int64, direction, status string, body HistoryBody) {
	switch {
	case direction == store.MessageIn && (status == store.MessageDone || status == store.MessagePartial):
		n.emit(Event{Kind: EventReceived, HistoryID: id, PeerName: body.PeerName, Summary: bodySummary(body), Files: len(body.Files)})
	case direction == store.MessageOut && (status == store.MessageFailed || status == store.MessagePartial):
		n.emit(Event{Kind: EventSendFailed, HistoryID: id, PeerName: body.PeerName, Summary: bodySummary(body), Error: body.Error})
	}
}

// bodySummary 是通知里的内容摘要。
func bodySummary(b HistoryBody) string {
	switch {
	case len(b.Files) == 1:
		return path.Base(b.Files[0].Path)
	case len(b.Files) > 1:
		return i18n.Tf("%s 等 %d 个文件", path.Base(b.Files[0].Path), len(b.Files))
	case len(b.Texts) > 0:
		t := b.Texts[0]
		if utf8.RuneCountInString(t) > notifyPreviewRunes {
			t = string([]rune(t)[:notifyPreviewRunes]) + "…"
		}
		return t
	}
	return ""
}

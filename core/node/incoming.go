package node

import (
	"encoding/hex"
	"errors"
	"time"

	"portide/core/store"
	"portide/core/transfer"
)

// stoppedKeep 是"已停止的接收"记录的保留时间：超过排队期限很久之后，发送方早就放弃了那次传输。
const stoppedKeep = 30 * 24 * time.Hour

// ErrNoSuchIncoming 表示没有这次正在进行的接收（已经结束，或守护进程没有运行）。
var ErrNoSuchIncoming = errors.New("node: no such incoming transfer")

// incomingProgressEvery 是有接收在进行时发出变化信号（store.ChangeTransfers）的间隔：界面据此刷新接收进度。
const incomingProgressEvery = 500 * time.Millisecond

// IncomingTransfer 是一次正在进行的接收（M3b 传输页）。只在运行守护进程的进程里有，不写数据库。
type IncomingTransfer struct {
	PeerKey    string
	PeerName   string
	TransferID string // 十六进制
	Summary    string // 内容摘要，例如"报告.pdf 等 3 个文件"
	Done       int64  // 已经在本机的字节数（包括续传前收到的部分）
	Total      int64
	Started    time.Time // 这次连接开始接收的时间
}

// IncomingTransfers 返回正在进行的接收；守护进程没有运行时为空。
func (n *Node) IncomingTransfers() []IncomingTransfer {
	r := n.receiver()
	if r == nil {
		return nil
	}
	var out []IncomingTransfer
	for _, a := range r.Active() {
		summary, _ := transfer.Describe(a.Offer)
		out = append(out, IncomingTransfer{
			PeerKey: a.Peer, PeerName: n.memberName(a.Peer), TransferID: hex.EncodeToString(a.TransferID),
			Summary: summary, Done: a.Done, Total: a.Total, Started: a.Started,
		})
	}
	return out
}

// receiver 返回守护进程的接收端；守护进程没有运行时返回 nil。
func (n *Node) receiver() *transfer.Receiver {
	n.mu.Lock()
	d := n.daemon
	n.mu.Unlock()
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.recv
}

// notifyChange 发出一个变化信号（不经过数据库的变化，例如接收进度）。
func (n *Node) notifyChange(topic string) {
	if n.opts.OnChange != nil {
		n.opts.OnChange(topic)
	}
}

// reportIncomingProgress 在有接收进行时发出 transfers 信号（守护进程主循环定时调用）。
func (n *Node) reportIncomingProgress() {
	if r := n.receiver(); r != nil && len(r.Active()) > 0 {
		n.notifyChange(store.ChangeTransfers)
	}
}

// StopIncoming 停止一次正在进行的接收（M3b）：删除未完成的文件，并记住这次传输——发送方之后重试时被拒绝，
// 任务以"对方停止了接收"结束。先写记录再断开，所以断开后立刻到来的重试也会被拒绝。
func (n *Node) StopIncoming(peerKey, transferID string) error {
	id, err := hex.DecodeString(transferID)
	r := n.receiver()
	if err != nil || r == nil {
		return ErrNoSuchIncoming
	}
	found := false
	for _, a := range r.Active() {
		found = found || (a.Peer == peerKey && hex.EncodeToString(a.TransferID) == transferID)
	}
	if !found {
		return ErrNoSuchIncoming
	}
	if err := n.store.AddStoppedIncoming(store.IncomingKey{PeerKey: peerKey, TransferID: transferID}); err != nil {
		return err
	}
	if !r.Stop(peerKey, id) {
		return ErrNoSuchIncoming
	}
	return nil
}

package node

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"portide/core/i18n"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"portide/core/pb"
	"portide/core/store"
	"portide/core/transfer"
)

// askTimeout 是询问模式下等待用户确认的时长，超时按拒绝处理。
const askTimeout = 2 * time.Minute

// staleAfter 之前开始、一直没有结束的传输，在守护进程启动时和之后定期清理；staleScanEvery 是
// 定期清理的间隔：守护进程可能一连运行很多天，只在启动时清理一次不够，过期的接收会一直占着名字
// （见 Receiver.supersedeOrphans 的注释）。都是变量只为了让测试缩短它们（必须在守护进程启动前
// 修改，和 queueDay 一样）。
var (
	staleAfter     = 7 * 24 * time.Hour
	staleScanEvery = time.Hour
)

// 接收策略（设计方案 5.4）。
const (
	PolicyAuto     = "auto"      // 自动接收（默认）
	PolicyAsk      = "ask"       // 总是询问
	PolicyAskAbove = "ask-above" // 超过阈值时询问
)

// ReceivePolicy 是接收策略。
type ReceivePolicy struct {
	Mode  string
	Above int64 // Mode 为 PolicyAskAbove 时的阈值（字节）
}

// ParseReceivePolicy 解析数据库里保存的策略："auto"、"ask" 或 "ask-above:<字节数>"。
func ParseReceivePolicy(s string) (ReceivePolicy, error) {
	switch {
	case s == "" || s == PolicyAuto:
		return ReceivePolicy{Mode: PolicyAuto}, nil
	case s == PolicyAsk:
		return ReceivePolicy{Mode: PolicyAsk}, nil
	case strings.HasPrefix(s, PolicyAskAbove+":"):
		n, err := strconv.ParseInt(strings.TrimPrefix(s, PolicyAskAbove+":"), 10, 64)
		if err != nil || n < 0 {
			return ReceivePolicy{}, fmt.Errorf("node: bad receive policy %q", s)
		}
		return ReceivePolicy{Mode: PolicyAskAbove, Above: n}, nil
	}
	return ReceivePolicy{}, fmt.Errorf("node: bad receive policy %q", s)
}

// String 返回保存到数据库的形式。
func (p ReceivePolicy) String() string {
	if p.Mode == PolicyAskAbove {
		return PolicyAskAbove + ":" + strconv.FormatInt(p.Above, 10)
	}
	return p.Mode
}

func (p ReceivePolicy) needsAsk(total int64) bool {
	return p.Mode == PolicyAsk || (p.Mode == PolicyAskAbove && total > p.Above)
}

// ReceivePolicy 返回当前接收策略；没有设置或无法解析时为自动接收。
func (n *Node) ReceivePolicy() ReceivePolicy {
	v, _, _ := n.store.GetMeta(store.MetaReceivePolicy)
	p, err := ParseReceivePolicy(v)
	if err != nil {
		return ReceivePolicy{Mode: PolicyAuto}
	}
	return p
}

// SetReceivePolicy 保存接收策略；守护进程收到下一个报价时生效。
func (n *Node) SetReceivePolicy(p ReceivePolicy) error {
	if _, err := ParseReceivePolicy(p.String()); err != nil {
		return err
	}
	return n.store.SetMeta(store.MetaReceivePolicy, p.String())
}

// DefaultSaveDir 是默认的保存目录：下载/Portide（设计方案 5.6）。
func DefaultSaveDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads", "Portide"), nil
}

// SaveDir 返回接收文件的保存目录。
func (n *Node) SaveDir() (string, error) {
	if v, ok, err := n.store.GetMeta(store.MetaSaveDir); err != nil {
		return "", err
	} else if ok && v != "" {
		return v, nil
	}
	return DefaultSaveDir()
}

// SetSaveDir 设置保存目录（保存为绝对路径）。
func (n *Node) SetSaveDir(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	return n.store.SetMeta(store.MetaSaveDir, abs)
}

// memberName 返回成员的设备名；不认识时返回 KeyID 前缀。
func (n *Node) memberName(keyID string) string {
	c, _ := n.snapshot()
	if c != nil {
		if m, ok := c.Member(keyID); ok {
			return m.Name
		}
		if m, ok := c.FormerMember(keyID); ok {
			return m.Name // 已被移除的设备：历史和日志里仍然显示它的名字
		}
	}
	return fmt.Sprintf("%.12s…", keyID)
}

// newReceiver 创建守护进程的接收端。
func (n *Node) newReceiver() *transfer.Receiver {
	return &transfer.Receiver{
		Store:  n.store,
		Decide: n.decideOffer,
		OnText: func(peer, text string) {
			// 文本内容只写进加密的本地历史，不出现在日志里。
			n.opts.Logf(i18n.T("收到「%s」的一段文本（%d 字），用 portide history 查看"), n.memberName(peer), utf8.RuneCountInString(text))
		},
		OnEnd: func(peer string, o *pb.Offer, r transfer.Result) {
			n.recordReceive(peer, o, r)
			desc := logDescribe(o)
			name := n.memberName(peer)
			dir := r.SaveDir
			switch r.End {
			case transfer.EndComplete:
				n.opts.Logf(i18n.T("已收到「%s」发来的 %s，保存在 %s"), name, desc, dir)
			case transfer.EndPartial:
				n.opts.Logf(i18n.T("收到「%s」发来的 %s，但有文件没有送达（见发送方的提示），已收到的保存在 %s"), name, desc, dir)
			case transfer.EndCancelled:
				n.opts.Logf(i18n.T("「%s」取消了发送：%s"), name, desc)
			case transfer.EndStopped:
				n.opts.Logf(i18n.T("已停止接收「%s」的 %s"), name, desc)
			default:
				n.opts.Logf(i18n.T("与「%s」的传输中断：%s；对方重新连上后会自动续传"), name, desc)
			}
		},
		OnActive: func() { n.notifyChange(store.ChangeTransfers) },
	}
}

// decideOffer 按接收策略决定是否接受报价；需要询问时把报价记入数据库，等用户用 `portide accept` / `portide reject` 处理。
func (n *Node) decideOffer(ctx context.Context, peer string, o *pb.Offer, resumed bool) (string, string) {
	dir, err := n.SaveDir()
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		n.opts.Logf(i18n.T("无法使用保存目录：%v"), err)
		return "", transfer.ReasonError
	}
	// 询问摘要（在数据库里加密保存、显示在 portide inbox）可以带文本开头；日志里的摘要不带。
	summary, total := transfer.Describe(o)
	desc := logDescribe(o)
	name := n.memberName(peer)
	// 本机停止过这次接收（M3b）：对方的重试一律拒绝，让它以"对方停止了接收"结束。
	if stopped, err := n.store.IsStoppedIncoming(store.IncomingKey{PeerKey: peer, TransferID: hex.EncodeToString(o.TransferId)}); err != nil {
		return "", transfer.ReasonError
	} else if stopped {
		return "", transfer.ReasonStopped
	}
	if resumed {
		n.opts.Logf(i18n.T("继续接收「%s」的 %s"), name, desc)
		return dir, ""
	}
	if !n.ReceivePolicy().needsAsk(total) {
		n.opts.Logf(i18n.T("开始接收「%s」的 %s"), name, desc)
		return dir, ""
	}
	id, err := n.store.CreateInbox(name, summary, total, askTimeout)
	if err != nil {
		return "", transfer.ReasonError
	}
	n.emit(Event{Kind: EventAsk, InboxID: id, PeerName: name, Summary: summary})
	defer n.store.DeleteInbox(id)
	n.opts.Logf(i18n.T("「%s」想发送 %s。运行 `portide accept %d` 接收，或 `portide reject %d` 拒绝（%d 分钟内有效）"),
		name, desc, id, id, int(askTimeout.Minutes()))
	timeout := time.NewTimer(askTimeout)
	defer timeout.Stop()
	tick := time.NewTicker(n.opts.PollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", transfer.ReasonError
		case <-timeout.C:
			// 用户可能在最后一刻才做出决定：再读一次数据库，不要错失它。
			if e, err := n.store.GetInbox(id); err == nil {
				switch e.Status {
				case store.InboxAccepted:
					n.opts.Logf(i18n.T("开始接收「%s」的 %s"), name, desc)
					return dir, ""
				case store.InboxRejected:
					n.opts.Logf(i18n.T("已拒绝「%s」的 %s"), name, desc)
					return "", transfer.ReasonDeclined
				}
			}
			n.opts.Logf(i18n.T("没有在 %d 分钟内确认，已拒绝「%s」的 %s"), int(askTimeout.Minutes()), name, desc)
			return "", transfer.ReasonTimeout
		case <-tick.C:
			e, err := n.store.GetInbox(id)
			if err != nil {
				return "", transfer.ReasonError
			}
			switch e.Status {
			case store.InboxAccepted:
				n.opts.Logf(i18n.T("开始接收「%s」的 %s"), name, desc)
				return dir, ""
			case store.InboxRejected:
				n.opts.Logf(i18n.T("已拒绝「%s」的 %s"), name, desc)
				return "", transfer.ReasonDeclined
			}
		}
	}
}

// staleThreshold 返回清理过期接收的期限：不短于本机配置的排队保留天数（QueueDays）——接收方把
// 排队保留期设得比默认的 staleAfter 更长时，不该让清理比发送队列更早地丢掉还可能续传的接收。
func (n *Node) staleThreshold() time.Duration {
	if keep := time.Duration(n.QueueDays()) * queueDay; keep > staleAfter {
		return keep
	}
	return staleAfter
}

// removeStaleIncoming 删除很久没有结束的接收留下的临时文件和续传记录：守护进程启动时调用一次
// （skip 传 nil——这时还没处理过任何报价，没有连接在用任何 key），之后由 runOutgoing 按
// staleScanEvery 定期调用（skip 传 d.recv.InUse，跳过有连接正在处理、或还停在决定/接受阶段的
// key；一个连接已经断开、只剩续传记录等续传的接收不受保护，过期后就会被这里清掉——这是设计上的
// 行为，不是漏网之鱼）。
func (n *Node) removeStaleIncoming(skip func(store.IncomingKey) bool) {
	dir, err := n.SaveDir()
	if err != nil {
		return
	}
	before := time.Now().Add(-n.staleThreshold())
	if err := transfer.RemoveStale(dir, n.store, before, skip); err != nil && !errors.Is(err, os.ErrNotExist) {
		n.opts.Logf(i18n.T("清理未完成的接收失败：%v"), err)
	}
}

// cleanupStaleTransfers 在守护进程启动时调用：删除很久没有结束的接收留下的临时文件和续传记录、
// 命令行没来得及删除的发送任务，以及守护进程上次退出时没处理完的询问记录
// （ClearInbox 只在启动时做一次：运行期间的询问都是当前这个守护进程自己负责的，不能被定期清理误删）。
func (n *Node) cleanupStaleTransfers() {
	n.removeStaleIncoming(nil)
	_ = n.store.PruneOutgoing(time.Now().Add(-staleAfter))
	_ = n.store.ClearInbox()
}

package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"portide/core/i18n"
	"portide/core/pb"
	"portide/core/store"
	"portide/core/transfer"
)

const (
	// progressEvery 是写入发送进度的间隔。
	progressEvery = 300 * time.Millisecond
	// retryBase、retryMax：第 n 次失败（中断或出错）后等 retryBase·2^(n-1) 再试，最长 retryMax（设计方案 5.3）。
	retryBase = 5 * time.Second
	retryMax  = 10 * time.Minute
	// DefaultQueueDays 是排队任务默认保留的天数（设计方案 5.3）。
	DefaultQueueDays = 7
	// finishedKeep 是已结束的任务留给 `portide queue` 查看的时长，之后由守护进程删除。
	finishedKeep = 24 * time.Hour
)

// queueDay 是保留期限里的一"天"。是变量只为了让测试缩短它（必须在守护进程启动前修改）。
var queueDay = 24 * time.Hour

// retryDelay 返回第 attempts 次失败之后的等待时间。
func retryDelay(attempts int) time.Duration {
	d := retryBase
	for i := 1; i < attempts && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax)
}

// QueueDays 返回排队任务的保留天数。
func (n *Node) QueueDays() int {
	v, _, _ := n.store.GetMeta(store.MetaQueueDays)
	days, err := strconv.Atoi(v)
	if err != nil || days < 1 {
		return DefaultQueueDays
	}
	return days
}

// SetQueueDays 设置排队任务的保留天数（1–365）。
func (n *Node) SetQueueDays(days int) error {
	if days < 1 || days > 365 {
		return fmt.Errorf("node: queue days must be between 1 and 365, got %d", days)
	}
	return n.store.SetMeta(store.MetaQueueDays, strconv.Itoa(days))
}

// ErrAmbiguousDevice 表示设备名或 KeyID 前缀匹配到多台设备。
var ErrAmbiguousDevice = errors.New("node: more than one device matches")

// ErrNoSuchDevice 表示没有匹配的设备。
var ErrNoSuchDevice = errors.New("node: no such device in the circle")

// errPaused 是用户暂停任务时取消发送所用的原因：和守护进程关闭一样按连接中断处理，接收方保留临时文件等续传。
var errPaused = errors.New("node: paused by user")

// FindDevice 按设备名（不区分大小写）或 KeyID 前缀（至少 4 个字符）查找设备圈中的其他设备。
func (n *Node) FindDevice(query string) (DeviceStatus, error) {
	devs, err := n.Devices()
	if err != nil {
		return DeviceStatus{}, err
	}
	var byName, byKey []DeviceStatus
	for _, d := range devs {
		if d.Self {
			continue
		}
		if strings.EqualFold(d.Name, query) {
			byName = append(byName, d)
		}
		if len(query) >= 4 && strings.HasPrefix(d.KeyID, strings.ToUpper(query)) {
			byKey = append(byKey, d)
		}
	}
	for _, m := range [][]DeviceStatus{byName, byKey} {
		switch len(m) {
		case 0:
			continue
		case 1:
			return m[0], nil
		default:
			return DeviceStatus{}, ErrAmbiguousDevice
		}
	}
	return DeviceStatus{}, ErrNoSuchDevice
}

// QueueSend 创建一个发送任务，由守护进程执行；返回任务 ID。
func (n *Node) QueueSend(peerKey string, p *transfer.Plan) (int64, error) {
	o, err := p.Offer(transfer.NewTransferID())
	if err != nil {
		return 0, err
	}
	if err := transfer.CheckOffer(o); err != nil {
		return 0, fmt.Errorf("node: offer would be rejected: %w", err)
	}
	offer, err := proto.Marshal(o)
	if err != nil {
		return 0, err
	}
	sources, err := json.Marshal(p.Sources)
	if err != nil {
		return 0, err
	}
	return n.store.CreateOutgoing(peerKey, o.TransferId, offer, sources, p.Total())
}

// runOutgoing 在守护进程每次轮询时调用：启动可以开始的发送任务（对方在线、到了重试时间），
// 处理取消和过期，并定期删除已结束很久的任务。
func (n *Node) runOutgoing(ctx context.Context) {
	tasks, err := n.store.OpenOutgoing()
	if err != nil {
		n.opts.Logf(i18n.T("读取发送任务失败：%v"), err)
		return
	}
	d := n.daemon
	now := time.Now()
	days := n.QueueDays()
	keep := time.Duration(days) * queueDay
	c, _ := n.snapshot()
	for _, task := range tasks {
		// 对方已被移出设备圈：任务不会再有机会送达（正在发送的那次随连接断开而中断，下一轮再处理）。
		removed := c != nil && !c.IsMember(task.PeerKey)
		d.mu.Lock()
		running := d.sending[task.ID]
		mc := d.conns[task.PeerKey]
		if mc != nil && mc.conn.Context().Err() != nil {
			mc = nil
		}
		// 暂停中的任务不开始、也不过期；排队期限从最近一次继续时重新计算（见 Outgoing.QueuedSince）。
		expired := !running && !task.Paused && now.Sub(task.QueuedSince()) > keep
		start := !running && !removed && !expired && !task.Cancel && !task.Paused && mc != nil && !now.Before(task.NextAt)
		if start {
			d.sending[task.ID] = true
		}
		d.mu.Unlock()
		switch {
		case running:
		case removed && !task.Cancel:
			if cur, err := n.store.GetOutgoing(task.ID); err == nil && (cur.Status == store.SendPending || cur.Status == store.SendInterrupted) {
				msg := i18n.T("对方已被移出设备圈")
				// 先写历史再改状态（同 expired 分支）。
				if o, srcs, err := decodeTask(*cur); err == nil {
					n.recordSend(task.PeerKey, o, srcs, store.MessageFailed, msg)
				}
				_ = n.store.UpdateOutgoing(task.ID, store.SendFailed, msg)
				n.opts.Logf(i18n.T("发给「%s」的任务 %d：%s"), n.memberName(task.PeerKey), task.ID, msg)
			}
		case task.Cancel:
			// 重新读一次：轮询期间任务可能刚刚结束，不能把已完成的任务改成已取消。
			if cur, err := n.store.GetOutgoing(task.ID); err == nil && (cur.Status == store.SendPending || cur.Status == store.SendInterrupted) {
				// 先写历史再改状态（同 expired 分支）。
				if o, srcs, err := decodeTask(*cur); err == nil {
					n.recordSend(task.PeerKey, o, srcs, store.MessageCancelled, "")
				}
				_ = n.store.UpdateOutgoing(task.ID, store.SendCancelled, "")
			}
		case expired:
			if cur, err := n.store.GetOutgoing(task.ID); err == nil && (cur.Status == store.SendPending || cur.Status == store.SendInterrupted) {
				// Attempts > 0 表示对方曾经上线、至少开始过一次发送：问题不是"一直没上线"，而是没能在期限内送达。
				msg := i18n.Tf("对方 %d 天内没有上线，已放弃发送", days)
				if cur.Attempts > 0 {
					msg = i18n.Tf("%d 天内没能送达，已放弃发送", days)
				}
				// 先写历史再改状态：看到任务结束的人（命令行、portide queue）随后就能在历史里查到它。
				if o, srcs, err := decodeTask(*cur); err == nil {
					n.recordSend(task.PeerKey, o, srcs, store.MessageFailed, msg)
				}
				_ = n.store.UpdateOutgoing(task.ID, store.SendFailed, msg)
				n.opts.Logf(i18n.T("发给「%s」的任务 %d：%s"), n.memberName(task.PeerKey), task.ID, msg)
			}
		case start:
			d.spawn(func() { n.sendTask(ctx, task, mc) })
		}
	}
	if now.Sub(d.lastPrune) > time.Minute {
		d.lastPrune = now
		_ = n.store.PruneOutgoing(now.Add(-finishedKeep))
		_ = n.store.PruneStoppedIncoming(now.Add(-stoppedKeep))
		n.pruneHistory(now)
	}
	// 清理过期未完成的接收（见 removeStaleIncoming）：守护进程可能连续运行很多天，
	// 只在启动时做一次不够。不在这里清理 inbox：那是启动时才做的一次性工作（见 cleanupStaleTransfers）。
	if now.Sub(d.lastStale) > staleScanEvery {
		d.lastStale = now
		n.removeStaleIncoming(d.recv.InUse)
	}
}

// sendTask 在成员连接 mc 上执行一个发送任务，并把结果写回数据库。
func (n *Node) sendTask(ctx context.Context, task store.Outgoing, mc *memberConn) {
	d := n.daemon
	defer func() {
		d.mu.Lock()
		delete(d.sending, task.ID)
		d.mu.Unlock()
	}()
	// 轮询读到的是快照：任务可能在这之后已经结束、被取消或被命令行删除。占住 sending 之后再读一次才可靠。
	cur, err := n.store.GetOutgoing(task.ID)
	if err != nil || cur.Cancel || cur.Paused || (cur.Status != store.SendPending && cur.Status != store.SendInterrupted && cur.Status != store.SendActive) {
		return
	}
	task = *cur
	o, srcs, err := decodeTask(task)
	if err != nil {
		_ = n.store.UpdateOutgoing(task.ID, store.SendFailed, i18n.T("任务数据损坏"))
		return
	}
	name := n.memberName(task.PeerKey)
	if !n.refreshOffer(task.ID, task.PeerKey, name, o, srcs) {
		return
	}
	if err := n.store.UpdateOutgoing(task.ID, store.SendActive, ""); err != nil {
		return
	}
	desc := logDescribe(o)
	if task.Attempts > 0 {
		n.opts.Logf(i18n.T("继续向「%s」发送 %s（第 %d 次重试）"), name, desc, task.Attempts)
	} else {
		n.opts.Logf(i18n.T("开始向「%s」发送 %s"), name, desc)
	}

	// 命令行通过数据库请求取消：轮询这个标志。用 WithCancelCause 区分用户取消和其他原因的取消
	// （守护进程关闭时派生自 Run 的 ctx 会被取消，但那不是用户取消）。
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var done atomic.Int64
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		tick := time.NewTicker(progressEvery)
		defer tick.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-tick.C:
				_ = n.store.SetOutgoingProgress(task.ID, done.Load())
				t, err := n.store.GetOutgoing(task.ID)
				switch {
				case err == nil && t.Cancel:
					cancel(transfer.ErrUserCancel)
				case err == nil && t.Paused:
					cancel(errPaused)
				case errors.Is(err, sql.ErrNoRows):
					// 命令行已经放弃并删除了任务记录：和用户取消一样处理。
					cancel(transfer.ErrUserCancel)
				}
			}
		}
	}()
	failures, err := transfer.Send(sctx, mc.conn, o, srcs, func(delta int64) { done.Add(delta) })
	close(stopWatch)
	<-watchDone
	_ = n.store.SetOutgoingProgress(task.ID, done.Load())

	var rej *transfer.RejectedError
	userCancel := errors.Is(context.Cause(sctx), transfer.ErrUserCancel)
	switch {
	case err == nil && len(failures) == 0:
		// 先写历史再改状态：命令行看到"已发送"之后，portide history 里一定已经有这一条。
		n.recordSend(task.PeerKey, o, srcs, store.MessageDone, "")
		_ = n.store.UpdateOutgoing(task.ID, store.SendDone, "")
		n.opts.Logf(i18n.T("已向「%s」发送 %s"), name, desc)
	case userCancel && err != nil:
		n.recordSend(task.PeerKey, o, srcs, store.MessageCancelled, "")
		_ = n.store.UpdateOutgoing(task.ID, store.SendCancelled, "")
		n.opts.Logf(i18n.T("已取消向「%s」发送 %s"), name, desc)
	case errors.Is(context.Cause(sctx), errPaused) && err != nil:
		// 用户暂停：回到中断状态（保留续传进度、不算失败次数），等继续之后从断点接着发。不写历史：任务还没结束。
		_ = n.store.RetryOutgoing(task.ID, task.Attempts, time.Time{}, "")
		n.opts.Logf(i18n.T("已暂停向「%s」发送 %s"), name, desc)
	case errors.As(err, &rej):
		msg := rejectMessage(rej.Reason)
		n.recordSend(task.PeerKey, o, srcs, store.MessageFailed, msg)
		_ = n.store.UpdateOutgoing(task.ID, store.SendFailed, msg)
		n.opts.Logf(i18n.T("「%s」没有接收 %s：%s"), name, desc, msg)
	case err != nil && ctx.Err() != nil:
		// 守护进程正在退出：下次启动、对方连上后续传。
		_ = n.store.RetryOutgoing(task.ID, task.Attempts, time.Time{}, i18n.T("守护进程已退出"))
		n.opts.Logf(i18n.T("守护进程退出，向「%s」发送 %s 暂停，下次启动后继续"), name, desc)
	case err != nil:
		// 连接中断或其他错误：退避后重试；排队期限到了才放弃。
		msg := i18n.T("连接中断")
		if !errors.Is(err, transfer.ErrInterrupted) {
			msg = i18n.Tf("发送失败：%v", err)
		}
		attempts := task.Attempts + 1
		wait := retryDelay(attempts)
		// 对方可能已经用一条新连接重新连上（新连接登记时这个任务还在发送，RetryNow 跳过了它）：那就立即重试。
		// 检查连接和写库必须在同一次持锁内完成：register 也在持有 d.mu 时写库（RetryNow），
		// 分两次持锁会让这里随后写入的退避时间覆盖 register 刚清零的 next_at。
		d.mu.Lock()
		cur := d.conns[task.PeerKey]
		reconnected := cur != nil && cur != mc && cur.conn.Context().Err() == nil
		next := time.Now().Add(wait)
		if reconnected {
			next = time.Time{}
		}
		_ = n.store.RetryOutgoing(task.ID, attempts, next, msg)
		d.mu.Unlock()
		if reconnected {
			n.opts.Logf(i18n.T("向「%s」发送 %s 时%s，对方已重新连上，立即重试"), name, desc, msg)
		} else {
			n.opts.Logf(i18n.T("向「%s」发送 %s 时%s，%s 后重试（对方重新上线时会立即重试）"), name, desc, msg, wait.Round(time.Second))
		}
	case len(failures) > 0:
		var parts []string
		for _, f := range failures {
			parts = append(parts, i18n.Tf("%s：%s", f.Path, itemMessage(f.Err)))
		}
		msg := i18n.Tf("%d 个文件没有送达：%s", len(failures), strings.Join(parts, i18n.T("；")))
		n.recordSend(task.PeerKey, o, srcs, store.MessagePartial, msg)
		_ = n.store.UpdateOutgoing(task.ID, store.SendFailed, msg)
		n.opts.Logf(i18n.T("向「%s」发送 %s 完成，但有 %d 个文件没有送达"), name, desc, len(failures))
	}
}

// PendingSourcePaths 返回还没结束的发送任务引用的本机文件（M4：iOS 清理发件箱时要保留它们）。
func (n *Node) PendingSourcePaths() ([]string, error) {
	tasks, err := n.store.ListOutgoing()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tasks {
		if t.Status != store.SendPending && t.Status != store.SendActive && t.Status != store.SendInterrupted {
			continue
		}
		_, srcs, err := decodeTask(t)
		if err != nil {
			continue
		}
		for _, s := range srcs {
			if s.Path != "" {
				out = append(out, s.Path)
			}
		}
	}
	return out, nil
}

// decodeTask 解出任务里保存的报价和本机来源。
func decodeTask(t store.Outgoing) (*pb.Offer, []transfer.Source, error) {
	var o pb.Offer
	var srcs []transfer.Source
	if err := proto.Unmarshal(t.Offer, &o); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(t.Sources, &srcs); err != nil {
		return nil, nil, err
	}
	return &o, srcs, nil
}

// refreshOffer 在每次开始发送前核对来源文件（设计方案 5.3、10）：文件被移动或删除时任务失败；
// 文件被修改过就按新内容重新报价（换一个新的传输 ID）。返回 false 表示不要继续发送。
func (n *Node) refreshOffer(id int64, peerKey, peerName string, o *pb.Offer, srcs []transfer.Source) bool {
	changed, err := transfer.Refresh(o, srcs)
	var missing *transfer.MissingError
	switch {
	case errors.As(err, &missing):
		msg := i18n.Tf("文件在发送前被移动或删除：%s", missing.Path)
		n.recordSend(peerKey, o, srcs, store.MessageFailed, msg)
		_ = n.store.UpdateOutgoing(id, store.SendFailed, msg)
		n.opts.Logf(i18n.T("发给「%s」的任务 %d 失败：%s"), peerName, id, msg)
		return false
	case err != nil:
		msg := i18n.Tf("无法读取要发送的文件：%v", err)
		n.recordSend(peerKey, o, srcs, store.MessageFailed, msg)
		_ = n.store.UpdateOutgoing(id, store.SendFailed, msg)
		return false
	case changed:
		o.TransferId = transfer.NewTransferID()
		b, err := proto.Marshal(o)
		if err != nil {
			return false
		}
		_, total := transfer.Describe(o)
		if err := n.store.ReplaceOffer(id, o.TransferId, b, total); err != nil {
			return false
		}
		n.opts.Logf(i18n.T("发给「%s」的文件在排队期间被修改过，按新内容重新报价"), peerName)
	}
	return true
}

// rejectMessage 把接收方的拒绝原因代码转成提示文字。
func rejectMessage(reason string) string {
	switch reason {
	case transfer.ReasonDeclined:
		return i18n.T("对方拒绝了")
	case transfer.ReasonTimeout:
		return i18n.T("对方没有在 2 分钟内确认")
	case transfer.ReasonNoSpace:
		return i18n.T("对方磁盘空间不足")
	case transfer.ReasonInvalid:
		return i18n.T("对方认为报价无效（版本不兼容？）")
	case transfer.ReasonStopped:
		return i18n.T("对方停止了接收")
	default:
		return i18n.Tf("对方出错了（%s）", reason)
	}
}

// itemMessage 把条目失败原因转成提示文字。
func itemMessage(msg string) string {
	switch msg {
	case "source file changed":
		return i18n.T("文件在发送前被修改了")
	case "cannot open source file", "cannot read source file":
		return i18n.T("无法读取文件")
	case "hash mismatch after retry":
		return i18n.T("校验两次都不一致")
	case "source data does not match offer":
		return i18n.T("文件内容与报价不一致")
	default:
		return i18n.Tf("对方写入失败（%s）", msg)
	}
}

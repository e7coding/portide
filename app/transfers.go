package app

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"portide/core/i18n"
	"portide/core/node"
	"portide/core/store"
)

// 传输页条目的状态（TransferItem.Status）：发送任务沿用 store.Send*，接收只有一种。
const StatusReceiving = "receiving"

// 速度取最近 speedWindow 内的样本平均；超过 speedStale 没有新样本的条目被丢掉。
const (
	speedWindow = 5 * time.Second
	speedStale  = time.Minute
)

// TransferItem 是传输页上的一条：一个未结束的发送任务，或一次正在进行的接收。
type TransferItem struct {
	Key         string `json:"key"`       // 界面列表用的唯一键："o<任务号>" 或 "i<对方 KeyID>:<传输 ID>"
	Direction   string `json:"direction"` // "out" | "in"
	ID          int64  `json:"id"`        // 发送：任务号；接收：0
	PeerKey     string `json:"peerKey"`
	PeerName    string `json:"peerName"`
	TransferID  string `json:"transferId"` // 接收：传输 ID（十六进制），StopReceive 用
	Summary     string `json:"summary"`
	Status      string `json:"status"` // 发送：pending / sending / interrupted；接收：receiving
	Paused      bool   `json:"paused"`
	BytesDone   int64  `json:"bytesDone"`
	BytesTotal  int64  `json:"bytesTotal"`
	BytesPerSec int64  `json:"bytesPerSec"` // 0 表示没有在传
	EtaSec      int64  `json:"etaSec"`      // 剩余秒数；-1 表示算不出来
	Error       string `json:"error"`       // 发送：上一次中断的原因
	At          int64  `json:"at"`          // Unix 毫秒：发送任务的创建时间，或这次接收的开始时间
}

// TransferList 是传输页的内容。
type TransferList struct {
	Outgoing []TransferItem `json:"outgoing"`
	Incoming []TransferItem `json:"incoming"`
	// Active 是正在传输的数量（正在发送且没暂停的，加上正在接收的）：导航角标和托盘只数这些，
	// 排队等对方上线、中断等待重试、已暂停的不算（M3c 设计 4.1）。
	Active int `json:"active"`
}

// Count 是未结束的传输数（包括排队和暂停的）。
func (l TransferList) Count() int { return len(l.Outgoing) + len(l.Incoming) }

// Transfers 返回未结束的发送任务（包括已暂停的）和正在进行的接收，带速度和剩余时间。
func (s *Service) Transfers() (TransferList, error) {
	n, err := s.node()
	if err != nil {
		return TransferList{}, err
	}
	tasks, err := n.Store().ListOutgoing()
	if err != nil {
		return TransferList{}, uiError(err)
	}
	now := time.Now()
	list := TransferList{Outgoing: []TransferItem{}, Incoming: []TransferItem{}}
	names := deviceNames(n)
	for _, t := range tasks {
		if !unfinished(t.Status) {
			continue
		}
		it := taskItem(t, names[t.PeerKey])
		item := TransferItem{
			Key: fmt.Sprintf("o%d", t.ID), Direction: store.MessageOut, ID: t.ID, PeerKey: t.PeerKey, PeerName: it.PeerName,
			Summary: it.Summary, Status: t.Status, Paused: t.Paused, BytesDone: t.BytesDone, BytesTotal: t.BytesTotal,
			Error: t.Error, At: it.At,
		}
		if t.Status == store.SendActive && !t.Paused {
			list.Active++
			s.speed.fill(&item, now)
		} else {
			item.EtaSec = -1
		}
		list.Outgoing = append(list.Outgoing, item)
	}
	for _, in := range n.IncomingTransfers() {
		item := TransferItem{
			Key: "i" + in.PeerKey + ":" + in.TransferID, Direction: store.MessageIn, PeerKey: in.PeerKey, PeerName: in.PeerName,
			TransferID: in.TransferID, Summary: in.Summary, Status: StatusReceiving, BytesDone: in.Done, BytesTotal: in.Total,
			At: in.Started.UnixMilli(),
		}
		s.speed.fill(&item, now)
		list.Active++
		list.Incoming = append(list.Incoming, item)
	}
	s.speed.prune(now)
	return list, nil
}

// PauseSend 暂停一个还没结束的发送任务（正在发送的会停下来，已经传过去的部分保留）。
func (s *Service) PauseSend(id int64) error {
	return s.taskOp(id, func(st *store.Store) error { return st.PauseOutgoing(id) })
}

// ResumeSend 继续一个暂停的发送任务：立即重新开始，从断点续传。
func (s *Service) ResumeSend(id int64) error {
	return s.taskOp(id, func(st *store.Store) error { return st.ResumeOutgoing(id) })
}

// RetrySend 立即重试一个中断（或排队）的发送任务，不用等自动重试的退避时间。
// 只是把任务标成"现在就可以发"，真正发送仍由守护进程执行：它保证同一个任务同一时间只发一次，
// 所以和自动重试、对方重新上线时的立即重试同时发生也不会重复发送。
func (s *Service) RetrySend(id int64) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	t, err := n.Store().GetOutgoing(id)
	if err != nil || !unfinished(t.Status) || t.Cancel {
		return i18n.New("这个任务已经结束")
	}
	if err := retryRefusal(t); err != nil {
		return err
	}
	online := false
	if devs, err := n.Devices(); err == nil {
		for _, d := range devs {
			if d.KeyID == t.PeerKey {
				online = d.Online
			}
		}
	}
	if !online {
		return i18n.New("「%s」现在不在线，上线后会自动发送", deviceNames(n)[t.PeerKey])
	}
	if err := n.Store().RetryOutgoingNow(id); errors.Is(err, store.ErrTaskFinished) {
		// 读任务和写库之间状态变了（开始发送、结束、被暂停）：按最新状态说明。
		if t, err := n.Store().GetOutgoing(id); err == nil && unfinished(t.Status) && !t.Cancel {
			if err := retryRefusal(t); err != nil {
				return err
			}
		}
		return i18n.New("这个任务已经结束")
	} else if err != nil {
		return uiError(err)
	}
	return nil
}

// retryRefusal 说明一个未结束的任务为什么不能手动重试；可以重试时返回 nil。
func retryRefusal(t *store.Outgoing) error {
	switch {
	case t.Paused:
		return i18n.New("这个任务已暂停，请点继续")
	case t.Status == store.SendActive:
		return i18n.New("这个任务正在发送")
	}
	return nil
}

// PauseAll 暂停全部未结束的发送任务，返回暂停了几个（已经暂停的不算）。
func (s *Service) PauseAll() (int, error) {
	return s.allTasks(func(t store.Outgoing) bool { return !t.Paused }, (*store.Store).PauseOutgoing)
}

// ResumeAll 继续全部暂停的发送任务，返回继续了几个。
func (s *Service) ResumeAll() (int, error) {
	return s.allTasks(func(t store.Outgoing) bool { return t.Paused }, (*store.Store).ResumeOutgoing)
}

// allTasks 对每个满足 want 的未结束任务执行 op；期间刚好结束的任务跳过。
func (s *Service) allTasks(want func(store.Outgoing) bool, op func(*store.Store, int64) error) (int, error) {
	n, err := s.node()
	if err != nil {
		return 0, err
	}
	tasks, err := n.Store().ListOutgoing()
	if err != nil {
		return 0, uiError(err)
	}
	count := 0
	for _, t := range tasks {
		if !unfinished(t.Status) || !want(t) {
			continue
		}
		switch err := op(n.Store(), t.ID); {
		case errors.Is(err, store.ErrTaskFinished):
		case err != nil:
			return count, uiError(err)
		default:
			count++
		}
	}
	return count, nil
}

func (s *Service) taskOp(id int64, op func(*store.Store) error) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	if err := op(n.Store()); errors.Is(err, store.ErrTaskFinished) {
		return i18n.New("这个任务已经结束")
	} else if err != nil {
		return uiError(err)
	}
	return nil
}

// StopReceive 停止一次正在进行的接收：已收到的部分会删除，对方的任务以"对方停止了接收"结束。
func (s *Service) StopReceive(peerKey, transferID string) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	if err := n.StopIncoming(peerKey, transferID); errors.Is(err, node.ErrNoSuchIncoming) {
		return i18n.New("这次接收已经结束")
	} else if err != nil {
		return uiError(err)
	}
	return nil
}

// speedMeter 记录每个条目最近的已传字节数，用来估算速度（M3b 设计 2.4：核心不提供速度）。
type speedMeter struct {
	mu      sync.Mutex
	samples map[string][]speedSample
}

type speedSample struct {
	at   time.Time
	done int64
}

// fill 记下 it 这一次的字节数，并填上 BytesPerSec 和 EtaSec。
func (m *speedMeter) fill(it *TransferItem, now time.Time) {
	it.BytesPerSec = m.observe(it.Key, now, it.BytesDone)
	it.EtaSec = -1
	if it.BytesPerSec > 0 && it.BytesTotal > it.BytesDone {
		it.EtaSec = (it.BytesTotal - it.BytesDone + it.BytesPerSec - 1) / it.BytesPerSec
	}
}

// observe 记下一个样本，返回最近 speedWindow 内的平均速度（字节/秒）。字节数变小（例如续传从头开始）、
// 或者上一个样本已经超过 speedWindow（例如暂停后继续）时重新计算，不拿很久以前的样本来平均。
func (m *speedMeter) observe(key string, now time.Time, done int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.samples == nil {
		m.samples = map[string][]speedSample{}
	}
	ss := m.samples[key]
	if len(ss) > 0 && (done < ss[len(ss)-1].done || now.Sub(ss[len(ss)-1].at) > speedWindow) {
		ss = nil
	}
	ss = append(ss, speedSample{at: now, done: done})
	for len(ss) > 2 && now.Sub(ss[1].at) >= speedWindow {
		ss = ss[1:] // 保留一个落在窗口外的样本作为起点，让窗口刚好盖住 speedWindow
	}
	m.samples[key] = ss
	first := ss[0]
	elapsed := now.Sub(first.at)
	if elapsed <= 0 {
		return 0
	}
	return int64(float64(done-first.done) / elapsed.Seconds())
}

// prune 丢掉很久没有更新的条目（已经结束的传输）。
func (m *speedMeter) prune(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, ss := range m.samples {
		if now.Sub(ss[len(ss)-1].at) > speedStale {
			delete(m.samples, key)
		}
	}
}

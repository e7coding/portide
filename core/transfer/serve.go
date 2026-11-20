package transfer

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/pb"
	"portide/core/store"
	"portide/core/wire"
)

// supersedeWait 是新报价等待同一传输的旧接收退出的上限（旧连接可能要等空闲超时才断开）。
const supersedeWait = 45 * time.Second

// Receiver 把成员连接上收到的传输流交给对应的接收。一个守护进程一个 Receiver，可并发使用。
type Receiver struct {
	Store *store.Store
	// Decide 决定是否接受一个新报价：返回拒绝原因代码（Reason*），为空表示接受，此时给出保存目录。
	// resumed 为 true 表示这是一次中断传输的续传（之前已经接受过）。
	Decide func(ctx context.Context, peer string, o *pb.Offer, resumed bool) (saveDir, reject string)
	OnText func(peer, text string)
	OnFile func(peer, path string)
	// OnEnd 在一次接收结束时调用。
	OnEnd func(peer string, o *pb.Offer, r Result)
	// OnActive 在一次接收开始（接受报价、登记为进行中）和结束时调用（M3b：界面刷新接收进度）。不能阻塞。
	OnActive func()

	mu     sync.Mutex
	active map[string]*activeIn
	// busy 记录正处于 HandleOffer 决定/接受阶段、还没登记进 active 的 key（计数，允许同一个 key
	// 理论上被并发处理多次）。InUse 把这两个集合当成同一回事：清理和 supersedeOrphans 都不能动
	// 这里面的 key。
	busy map[string]int
	// acceptMu 串行化 Accept：新接收确定实际路径和写入续传记录必须一起完成，
	// 否则两个同时到达的报价可能选中同一个名字。
	acceptMu sync.Mutex
}

// InUse 报告 key 对应的接收是否正被某条连接处理：已经登记在 active 里（请求仍开着，可能在收数据，
// 也可能刚断开但这条 HandleOffer 还没退出），或者还在 HandleOffer 的决定/接受阶段（包括停在
// Decide 里询问用户的那段时间），尚未登记。注意：连接已经断开、HandleOffer 已经返回之后留下的
// 中断续传记录不算"在用"——按设计那正是 RemoveStale 过期后应该清理、supersedeOrphans 判定为孤儿
// 后应该丢弃的对象。RemoveStale（过期清理）和 supersedeOrphans 都必须跳过 InUse 为 true 的 key，
// 不能打断一个正在处理中的接收。
func (r *Receiver) InUse(k store.IncomingKey) bool {
	key := k.PeerKey + "/" + k.TransferID
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[key] != nil || r.busy[key] > 0
}

func (r *Receiver) markBusyLocked(key string) {
	if r.busy == nil {
		r.busy = map[string]int{}
	}
	r.busy[key]++
}

func (r *Receiver) clearBusyLocked(key string) {
	if n := r.busy[key]; n <= 1 {
		delete(r.busy, key)
	} else {
		r.busy[key] = n - 1
	}
}

func (r *Receiver) markBusy(key string) {
	r.mu.Lock()
	r.markBusyLocked(key)
	r.mu.Unlock()
}

func (r *Receiver) clearBusy(key string) {
	r.mu.Lock()
	r.clearBusyLocked(key)
	r.mu.Unlock()
}

// Result 是一次接收的结果，交给 OnEnd（用于日志和本地历史）。
type Result struct {
	End     End
	SaveDir string      // 这次接收使用的保存目录
	Saved   []SavedFile // 已经完整收到并落盘的文件
}

// SavedFile 是一个已经落盘的文件。
type SavedFile struct {
	Path string // 保存目录下的相对路径（已清理、已去重）
	Size uint64
}

// End 是一次接收的结束方式。
type End int

const (
	EndComplete    End = iota // 全部收齐
	EndPartial                // 发送方结束了传输，但有条目没有送达
	EndCancelled              // 发送方取消，未完成的文件已删除
	EndInterrupted            // 连接中断，临时文件已保留等续传
	EndStopped                // 本机停止了接收（Receiver.Stop），未完成的文件已删除
)

type activeIn struct {
	in      *Incoming
	ctrl    *quic.Stream
	done    chan struct{}
	peer    string
	started time.Time
	data    map[*quic.Stream]bool // 正在收数据的流（受 Receiver.mu 保护），Stop 要把它们一起断开
	stopped atomic.Bool
}

// ActiveTransfer 是一次正在进行的接收（Active 的快照）。
type ActiveTransfer struct {
	Peer       string
	TransferID []byte
	Offer      *pb.Offer
	Done       int64 // 已经在本机的字节数（包括续传前收到的部分）
	Total      int64
	Started    time.Time // 这次连接开始接收的时间
}

// Active 返回正在进行的接收，按开始时间排列。
func (r *Receiver) Active() []ActiveTransfer {
	r.mu.Lock()
	acts := make([]*activeIn, 0, len(r.active))
	for _, a := range r.active {
		acts = append(acts, a)
	}
	r.mu.Unlock()
	out := make([]ActiveTransfer, 0, len(acts))
	for _, a := range acts {
		done, total := a.in.Progress()
		out = append(out, ActiveTransfer{Peer: a.peer, TransferID: a.in.offer.TransferId, Offer: a.in.offer, Done: done, Total: total, Started: a.started})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// Stop 停止一次正在进行的接收：断开它的控制流和全部数据流，删除未完成的临时文件和续传记录。
// 没有这次接收时返回 false。发送方看到连接中断，稍后重试时由 Decide 拒绝（见 ReasonStopped）。
func (r *Receiver) Stop(peer string, transferID []byte) bool {
	r.mu.Lock()
	act := r.active[activeKey(peer, transferID)]
	var streams []*quic.Stream
	if act != nil {
		act.stopped.Store(true)
		for s := range act.data {
			streams = append(streams, s)
		}
	}
	r.mu.Unlock()
	if act == nil {
		return false
	}
	for _, s := range streams {
		s.CancelRead(0)
	}
	act.ctrl.CancelRead(0)
	select {
	case <-act.done:
	case <-time.After(supersedeWait):
	}
	return true
}

func (r *Receiver) activeChanged() {
	if r.OnActive != nil {
		r.OnActive()
	}
}

func activeKey(peer string, transferID []byte) string {
	return peer + "/" + hex.EncodeToString(transferID)
}

// HandleOffer 处理一条以 Envelope{Offer} 开头的控制流，直到这次传输结束。负责关闭 str。
func (r *Receiver) HandleOffer(ctx context.Context, peer string, str *quic.Stream, o *pb.Offer) {
	defer str.Close()
	reject := func(reason string) {
		_ = str.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_ = wire.Write(str, &pb.OfferReply{Reason: reason})
		str.CancelRead(0)
	}
	if err := CheckOffer(o); err != nil {
		reject(ReasonInvalid)
		return
	}
	key := activeKey(peer, o.TransferId)
	// 先标记忙，再去让同一传输的旧接收退出：supersede 等旧的 HandleOffer 结束时会把 key 从
	// r.active 里删掉，如果反过来先 supersede 再 markBusy，旧条目离开 active 和这次标记忙
	// 之间就会有一个 InUse(key) 短暂为 false 的空隙。从这里到登记进 active（或某个拒绝分支
	// 提前返回）为止，这次接收虽然还不在 active 里，但也不是"已经结束"：RemoveStale 和
	// supersedeOrphans 不能把它当成孤儿。registered 为 true 之后 busy 已经在登记 active 的
	// 同一次加锁里清掉了，defer 这里就不用再清一次。
	registered := false
	r.markBusy(key)
	defer func() {
		if !registered {
			r.clearBusy(key)
		}
	}()
	r.supersede(key)

	journal, err := r.Store.IncomingItems(store.IncomingKey{PeerKey: peer, TransferID: hex.EncodeToString(o.TransferId)})
	if err != nil {
		reject(ReasonError)
		return
	}
	// 等待决定期间（可能在询问用户）发送方可能取消：它会重置控制流。发送方在收到应答前不会再发数据，
	// 所以这里读一次流来发现重置；决定之后用一个已过去的读超时结束这次读取。
	dctx, stopDecide := context.WithCancel(ctx)
	probed := make(chan struct{})
	go func() {
		defer close(probed)
		var b [1]byte
		if _, err := str.Read(b[:]); !errors.Is(err, os.ErrDeadlineExceeded) {
			stopDecide()
		}
	}()
	saveDir, reason := r.Decide(dctx, peer, o, len(journal) > 0)
	_ = str.SetReadDeadline(time.Now())
	<-probed
	cancelled := dctx.Err() != nil && ctx.Err() == nil
	stopDecide()
	switch {
	case cancelled:
		str.CancelWrite(0)
		return
	case ctx.Err() != nil:
		// 本机守护进程正在退出：不回复，重置控制流，发送方按连接中断处理，之后重试。
		str.CancelWrite(0)
		str.CancelRead(0)
		return
	case reason != "":
		reject(reason)
		return
	}
	r.acceptMu.Lock()
	if len(journal) == 0 {
		// 新传输（不是续传）：同一台设备可能用新的传输 ID 重新发送同样的内容
		// （例如源文件变了，refreshOffer 换了新 ID），旧的中断接收已经被对方放弃。
		// 名字必须在 Accept 决定新报价的实际路径之前空出来，否则新的一份会被迫改名。
		r.supersedeOrphans(peer, saveDir, o)
	}
	in, offsets, err := Accept(o, AcceptOptions{
		SaveDir: saveDir, Store: r.Store, Peer: peer,
		OnText: func(t string) {
			if r.OnText != nil {
				r.OnText(peer, t)
			}
		},
		OnFile: func(p string) {
			if r.OnFile != nil {
				r.OnFile(peer, p)
			}
		},
	})
	var act *activeIn
	if err == nil {
		// 登记进 active 必须在 acceptMu 还持有的时候完成，紧跟着 Accept 成功：这样 busy 到 active
		// 之间不会有任何空隙，InUse 在这期间一直是 true。
		act = &activeIn{in: in, ctrl: str, done: make(chan struct{}), peer: peer, started: time.Now()}
		r.mu.Lock()
		if r.active == nil {
			r.active = map[string]*activeIn{}
		}
		r.active[key] = act
		r.clearBusyLocked(key)
		registered = true
		r.mu.Unlock()
	}
	r.acceptMu.Unlock()
	if act != nil {
		r.activeChanged()
	}
	switch {
	case errors.Is(err, ErrNoSpace):
		reject(ReasonNoSpace)
		return
	case errors.Is(err, ErrBadOffer):
		reject(ReasonInvalid)
		return
	case err != nil:
		reject(ReasonError)
		return
	}

	_ = str.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err = wire.Write(str, &pb.OfferReply{Accepted: true, Offsets: offsets})
	var end pb.TransferEnd
	if err == nil {
		_ = str.SetDeadline(time.Time{})
		err = wire.ReadMax(str, &end, 64)
	}
	cancelled = end.Cancelled || IsCancel(err)
	stopped := act.stopped.Load()
	final := err == nil || cancelled || stopped
	in.Finish(final)
	result := EndInterrupted
	switch {
	case stopped:
		result = EndStopped
	case cancelled:
		result = EndCancelled
	case final && in.Complete():
		result = EndComplete
	case final:
		result = EndPartial
	}

	r.mu.Lock()
	if r.active[key] == act {
		delete(r.active, key)
	}
	r.mu.Unlock()
	close(act.done)
	r.activeChanged()
	if r.OnEnd != nil {
		r.OnEnd(peer, o, Result{End: result, SaveDir: saveDir, Saved: in.Saved()})
	}
}

// supersedeOrphans 丢弃同一发送方占用了和新报价相同顶层名字、但已经不在进行中的旧接收的续传记录和
// .part 文件。同一台设备用新的传输 ID 重新发送同名内容时（源文件变了、或用户干脆再发一次），
// 旧的中断接收已经被对方放弃：不丢弃的话 store.IncomingTops 会一直占着这个名字，
// 新的一份就会被迫改名成"名称 (1)"，但其实对方根本不会再续传那次旧传输。
// 只处理当前没有接收在用的传输（跳过 InUse 为 true 的——有连接正在处理，或还停在决定/接受阶段的，
// 不能动；一个连接已经断开、只留下续传记录等着被判定为孤儿的接收，才是这里要处理的对象）。
func (r *Receiver) supersedeOrphans(peer, saveDir string, o *pb.Offer) {
	tops := map[string]bool{}
	for _, it := range o.Items {
		if it.Kind == pb.ItemKind_ITEM_KIND_TEXT {
			continue
		}
		p, err := CleanPath(it.Path)
		if err != nil {
			continue
		}
		top, _, _ := strings.Cut(p, "/")
		tops[foldKey(top)] = true
	}
	if len(tops) == 0 {
		return
	}
	keys, err := r.Store.IncomingKeys(peer)
	if err != nil || len(keys) == 0 {
		return
	}
	var root *os.Root
	defer func() {
		if root != nil {
			root.Close()
		}
	}()
	for _, k := range keys {
		if r.InUse(k) {
			continue
		}
		items, err := r.Store.IncomingItems(k)
		if err != nil {
			continue
		}
		hit := false
		for _, it := range items {
			if it.Path == "" {
				continue // 文本条目没有路径
			}
			top, _, _ := strings.Cut(it.Path, "/")
			if tops[foldKey(top)] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if root == nil {
			root, _ = os.OpenRoot(saveDir) // 打不开也继续：至少把续传记录删掉，不要一直占着名字
		}
		_ = dropIncoming(root, r.Store, k)
	}
}

// supersede 结束同一传输仍在进行的旧接收：对端断线后用新连接续传时，本机可能还没发现旧连接已经断开。
func (r *Receiver) supersede(key string) {
	r.mu.Lock()
	old := r.active[key]
	r.mu.Unlock()
	if old == nil {
		return
	}
	old.ctrl.CancelRead(0)
	select {
	case <-old.done:
	case <-time.After(supersedeWait):
	}
}

// HandleFileData 处理一条以 Envelope{FileData} 开头的数据流。负责关闭 str。
func (r *Receiver) HandleFileData(peer string, str *quic.Stream, fd *pb.FileData) {
	defer str.Close()
	r.mu.Lock()
	act := r.active[activeKey(peer, fd.TransferId)]
	if act != nil && !act.stopped.Load() {
		if act.data == nil {
			act.data = map[*quic.Stream]bool{}
		}
		act.data[str] = true
	}
	r.mu.Unlock()
	if act == nil || act.stopped.Load() {
		_ = wire.Write(str, &pb.FileAck{Error: "unknown transfer"})
		str.CancelRead(0)
		return
	}
	defer func() {
		r.mu.Lock()
		delete(act.data, str)
		r.mu.Unlock()
	}()
	if err := act.in.ReceiveFile(str, fd); errors.Is(err, errStream) {
		str.CancelRead(0)
	}
}

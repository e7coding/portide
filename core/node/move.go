package node

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"portide/core/i18n"
	"slices"
	"strings"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/chain"
	"portide/core/keystore"
	"portide/core/memberauth"
	"portide/core/pairing"
	"portide/core/pb"
	"portide/core/store"
	"portide/core/wire"
)

// 合并之后来源圈的其他设备跟过来（合并设备圈设计第 8 节）。本机参与过一次合并（自己是加入方，或者跟随了
// 别的设备发来的迁移消息）后，在数据库里留一条迁移记录、在密钥库里留来源圈的发现密钥，等待期内用它们
// 去找还没跟过来的旧设备。迁移记录只有一条：再换一次圈（JOIN、ADOPT、合并、离开）就覆盖或者删掉它。

// moveGrace 是迁移等待期：合并之后这么久还没跟过来的设备就不再等了。
const moveGrace = 30 * 24 * time.Hour

// discoveryKeyLen 是发现密钥的长度。密钥库没有删除操作，旧密钥"删除"时写入空值，长度不对的一律当作没有。
const discoveryKeyLen = 32

// moveState 是迁移记录在内存里的缓存（由 n.mu 保护）。oldKey 是来源圈的发现密钥，没有或无效时为 nil。
type moveState struct {
	rec    *store.CircleMove
	oldKey []byte
}

// newMove 是换圈时要写入的迁移记录和来源圈的发现密钥。
type newMove struct {
	rec    *store.CircleMove
	oldKey []byte
}

// loadMoveLocked 从数据库和密钥库读出迁移记录和来源圈的发现密钥，放进 n.move。调用方持有 n.mu（Open 时除外）。
func (n *Node) loadMoveLocked() error {
	rec, err := n.store.CircleMove()
	if err != nil {
		return err
	}
	var key []byte
	if rec != nil {
		k, err := n.opts.Keys.Get(keystore.OldDiscoveryKey)
		if err != nil && !errors.Is(err, keystore.ErrNotFound) {
			return err
		}
		if len(k) == discoveryKeyLen {
			key = k
		}
	}
	n.move = moveState{rec: rec, oldKey: key}
	return nil
}

// saveMoveLocked 写入（mv 不为 nil）或清除（mv 为 nil）迁移记录和来源圈的发现密钥，再刷新 n.move。调用方持有 n.mu。
// 先密钥后记录：有记录就一定有密钥；清除时先把密钥写空，中途失败留下的记录也因为没有密钥而不会被使用。
func (n *Node) saveMoveLocked(mv *newMove) error {
	if mv == nil {
		// 已经没有的就不再写：大多数换圈（JOIN、ADOPT）根本没有迁移记录，不必每次都写一遍系统密钥库。
		// 按数据库和密钥库判断，不按缓存：缓存可能还没跟上别的进程的写入。
		rec, err := n.store.CircleMove()
		if err != nil {
			return err
		}
		k, err := n.opts.Keys.Get(keystore.OldDiscoveryKey)
		if err != nil && !errors.Is(err, keystore.ErrNotFound) {
			return err
		}
		if len(k) != 0 {
			if err := n.opts.Keys.Set(keystore.OldDiscoveryKey, nil); err != nil {
				return err
			}
		}
		if rec != nil {
			if err := n.store.DeleteCircleMove(); err != nil {
				return err
			}
		}
	} else {
		if err := n.opts.Keys.Set(keystore.OldDiscoveryKey, mv.oldKey); err != nil {
			return err
		}
		if err := n.store.SaveCircleMove(mv.rec); err != nil {
			return err
		}
	}
	return n.loadMoveLocked()
}

// activeMove 返回迁移记录的副本和来源圈的发现密钥。没有记录时都是 nil；记录已到期、没有还在等的设备、
// 密钥无效、本机不在圈里（包括已经被移出现在这个圈），或者记录指向本机现在所在的圈（换圈中途失败留下的）时
// oldKey 为 nil：这时不该再用旧圈连接找人。
func (n *Node) activeMove() (rec *store.CircleMove, oldKey []byte) {
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.move
	if m.rec == nil {
		return nil, nil
	}
	rec = &store.CircleMove{
		OldCircle: m.rec.OldCircle,
		OldChain:  cloneRecords(m.rec.OldChain),
		Brought:   slices.Clone(m.rec.Brought),
		Pending:   slices.Clone(m.rec.Pending),
		Elsewhere: slices.Clone(m.rec.Elsewhere),
		ExpiresAt: m.rec.ExpiresAt,
	}
	if m.oldKey == nil || len(rec.Pending) == 0 || !time.Now().Before(rec.ExpiresAt) ||
		n.chain == nil || !n.chain.IsMember(n.id.KeyID) || rec.OldCircle == n.chain.CircleID() {
		return rec, nil
	}
	return rec, bytes.Clone(m.oldKey)
}

// moveDigest 是 activeMove 结果的摘要：有没有记录、来源圈的发现密钥、还在等的设备。发现要按它重启。
func moveDigest(rec *store.CircleMove, oldKey []byte) string {
	if rec == nil {
		return ""
	}
	return "move|" + hex.EncodeToString(oldKey) + "|" + strings.Join(rec.Pending, ",")
}

// syncMove 在每次轮询时维护迁移记录（合并设备圈设计 8.2、8.3），activeMove 的结果因此变了就重启发现：
// 记录指向本机现在所在的圈（换圈中途失败留下的）→ 删除；Pending 里已经被移出本圈的设备去掉；
// Pending 空了 → 删除记录和来源圈的密钥；到期了 → 来源圈的密钥写空、OldChain 清掉，记录留给界面显示"没有跟过来"。
func (n *Node) syncMove(ctx context.Context) {
	n.mu.Lock()
	n.maintainMoveLocked()
	n.mu.Unlock()
	// 不持有 n.mu 时再拿 daemon.mu（锁的顺序）。
	digest := moveDigest(n.activeMove())
	d := n.daemon
	d.mu.Lock()
	changed := digest != d.discMove
	d.mu.Unlock()
	if changed {
		n.restartDiscovery(ctx)
	}
}

// maintainMoveLocked 是 syncMove 里改迁移记录的部分。调用方持有 n.mu。
func (n *Node) maintainMoveLocked() {
	rec := n.move.rec
	if rec == nil || n.chain == nil {
		return
	}
	if rec.OldCircle == n.chain.CircleID() {
		if err := n.saveMoveLocked(nil); err != nil {
			n.opts.Logf(i18n.T("删除迁移记录失败：%v"), err)
		}
		return
	}
	n.editMoveLocked(func(rec *store.CircleMove) bool {
		gone := func(id string) bool { return !n.chain.IsMember(id) }
		before := len(rec.Pending) + len(rec.Elsewhere)
		rec.Pending = slices.DeleteFunc(rec.Pending, gone)
		rec.Elsewhere = slices.DeleteFunc(rec.Elsewhere, gone)
		return len(rec.Pending)+len(rec.Elsewhere) != before
	})
	if len(n.move.rec.Pending) == 0 {
		if err := n.saveMoveLocked(nil); err != nil {
			n.opts.Logf(i18n.T("删除迁移记录失败：%v"), err)
		} else {
			n.opts.Logf("%s", i18n.T("原设备圈的设备都已经跟过来，不再用原设备圈的身份找它们"))
		}
		return
	}
	if !time.Now().Before(n.move.rec.ExpiresAt) && n.move.rec.OldChain != nil {
		// 先把密钥写空再清 OldChain：中途失败的话下次轮询再来一次。
		if err := n.opts.Keys.Set(keystore.OldDiscoveryKey, nil); err != nil {
			n.opts.Logf(i18n.T("清除原设备圈的发现密钥失败：%v"), err)
			return
		}
		n.move.oldKey = nil
		n.editMoveLocked(func(rec *store.CircleMove) bool {
			rec.OldChain = nil
			return true
		})
		n.opts.Logf("%s", i18n.T("合并已满 30 天，不再等还没跟过来的设备"))
	}
}

func cloneRecords(recs [][]byte) [][]byte {
	if recs == nil {
		return nil
	}
	out := make([][]byte, len(recs))
	for i, r := range recs {
		out[i] = bytes.Clone(r)
	}
	return out
}

// ---------- 迁移消息与旧圈连接（合并设备圈设计 8.1、8.3） ----------

const (
	// moveTimeout 是一次旧圈连接（发迁移消息、等回复、处理对方的迁移消息）最长用的时间。
	moveTimeout = 10 * time.Second
	// moveRetry 是旧圈连接失败（对方回复错误、版本太旧、已经去了别的圈）之后，再对它发起旧圈连接之前等的时间。
	moveRetry = 10 * time.Minute
	// maxMoveAck 是迁移回复的最大字节数（只有一个短的错误串）。
	maxMoveAck = 1024

	// 迁移回复里的错误。
	moveElsewhereMsg = "moved elsewhere"
	moveExpiredMsg   = "expired"
	moveBadChainMsg  = "invalid chain"
	moveBadKeyMsg    = "invalid discovery key"
	moveLocalErrMsg  = "switch failed" // 接收方本地出错（写密钥库、数据库失败）
)

// oldForkLog 是两边的来源圈链分叉时的日志（合并设备圈设计 8.4：不核对，按快照处理）。
func oldForkLog() string {
	return i18n.T("两边记下的原设备圈分叉了，按合并时的设备名单处理")
}

// oldCircleLog 是每次旧圈连接握手通过时日志的开头。
func oldCircleLog() string { return i18n.T("已用原设备圈的身份连上") }

// editMoveLocked 修改迁移记录的一份副本，edit 返回 true 时写回数据库和缓存。没有记录时什么都不做。调用方持有 n.mu。
// 写回之前再读一次数据库：别的进程（例如命令行里的一次合并）可能已经换了一份迁移记录，这时不覆盖它。
func (n *Node) editMoveLocked(edit func(rec *store.CircleMove) bool) {
	if n.move.rec == nil {
		return
	}
	rec := cloneMove(n.move.rec)
	if !edit(rec) {
		return
	}
	stored, err := n.store.CircleMove()
	if err != nil {
		n.opts.Logf(i18n.T("读取迁移记录失败：%v"), err)
		return
	}
	if stored == nil || stored.OldCircle != rec.OldCircle {
		n.opts.Logf("%s", i18n.T("迁移记录已经被别处换掉，这次的修改不保存"))
		return
	}
	if err := n.store.SaveCircleMove(rec); err != nil {
		n.opts.Logf(i18n.T("保存迁移记录失败：%v"), err)
		return
	}
	n.move.rec = rec
}

func cloneMove(r *store.CircleMove) *store.CircleMove {
	return &store.CircleMove{
		OldCircle: r.OldCircle,
		OldChain:  cloneRecords(r.OldChain),
		Brought:   slices.Clone(r.Brought),
		Pending:   slices.Clone(r.Pending),
		Elsewhere: slices.Clone(r.Elsewhere),
		ExpiresAt: r.ExpiresAt,
	}
}

// mergeOldChain 把对方的来源圈链合并进 rec.OldChain。不是同一个圈或者链无效时不改；分叉时不改、返回 true。
func mergeOldChain(rec *store.CircleMove, other [][]byte) (changed, forked bool) {
	oc, err := chain.Load(rec.OldChain)
	if err != nil {
		return false, false
	}
	changed, err = oc.Merge(other)
	if errors.Is(err, chain.ErrFork) {
		return false, true
	}
	if err != nil || !changed {
		return false, false
	}
	rec.OldChain = oc.Records()
	return true, false
}

// movedPeer 把 keyID 从 Pending、Elsewhere 里去掉并保存：它已经跟过来了。
func (n *Node) movedPeer(keyID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.editMoveLocked(func(rec *store.CircleMove) bool {
		if !slices.Contains(rec.Pending, keyID) && !slices.Contains(rec.Elsewhere, keyID) {
			return false
		}
		rec.Pending = slices.DeleteFunc(rec.Pending, func(s string) bool { return s == keyID })
		rec.Elsewhere = slices.DeleteFunc(rec.Elsewhere, func(s string) bool { return s == keyID })
		return true
	})
}

// movedElsewhere 记下 keyID 回复了"已经去了别的设备圈"（它仍在 Pending 里，界面据此提示）。
func (n *Node) movedElsewhere(keyID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.editMoveLocked(func(rec *store.CircleMove) bool {
		if !slices.Contains(rec.Pending, keyID) || slices.Contains(rec.Elsewhere, keyID) {
			return false
		}
		rec.Elsewhere = append(rec.Elsewhere, keyID)
		return true
	})
}

// admitOld 是旧圈连接的成员核对（握手的 Admit）：对方用来源圈的密钥通过了圈子证明，只有它还在 Pending 里、
// 仍是本机现在这个圈的成员（没有在新圈里被移除）、出示的是来源圈的链时才放行。放行时把对方的来源圈链合并进
// 迁移记录；两条来源圈链分叉时不改，forked 为 true。不碰本机现在的链。
func (n *Node) admitOld(p *memberauth.Peer) (forked bool, err error) {
	pc, err := chain.Load(p.Chain)
	if err != nil {
		return false, errNotMember
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.move
	if m.rec == nil || m.oldKey == nil || !time.Now().Before(m.rec.ExpiresAt) || n.chain == nil ||
		!n.chain.IsMember(p.KeyID) || !slices.Contains(m.rec.Pending, p.KeyID) || pc.CircleID() != m.rec.OldCircle {
		return false, errNotMember
	}
	n.editMoveLocked(func(rec *store.CircleMove) bool {
		changed, f := mergeOldChain(rec, p.Chain)
		forked = f
		return changed
	})
	if forked {
		n.opts.Logf("%s", oldForkLog())
	}
	return forked, nil
}

// oldHelloChain 返回旧圈连接上本机出示的来源圈链：rec.OldChain 里本机已经是成员的最短前缀。
// 对方还在来源圈里，按普通成员握手合并本机出示的链；出示整条的话，两边的来源圈链分叉时它的握手就会失败，
// 永远跟不过来。前缀是它自己链的前缀，合并不会分叉；完整的链随迁移消息发过去，由它按"分叉时用自己的"处理。
func oldHelloChain(records [][]byte, self string) [][]byte {
	for i := 1; i <= len(records); i++ {
		c, err := chain.Load(records[:i])
		if err != nil {
			return records
		}
		if c.IsMember(self) {
			return records[:i]
		}
	}
	return records
}

// moveCoolingDown 报告 keyID 是否在旧圈连接的冷却期内（见 moveFailed）。
func (n *Node) moveCoolingDown(keyID string) bool {
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	last, ok := d.moveTried[keyID]
	return ok && time.Since(last) < moveRetry
}

// reconcileMove 按最新的来源圈核对新圈（合并设备圈设计 8.4）：rec.OldChain 是最新的 A（旧圈连接握手时已经合并了
// 对方的 A 链，分叉时调用方不调用本函数），本机现在的链是 B。
//   - A 里后来加入的设备（不是本机、不在 B 里、从没在 B 里被移除过、不在 Brought 里）：由本机签进 B，
//     加进 Brought 和 Pending；
//   - 还在 Pending 里（还没跟过来）、后来在 A 里被移除、仍在 B 里的设备：移出 B（照常轮换 B 的发现密钥），
//     从 Brought、Pending 去掉。已经跟过来的设备不因为 A 里的移除被移出 B（需要的话在 B 里直接移除），
//     本机和这条连接的对方（peerID）也不移除。
//
// 改名不同步。和处理迁移消息一样拿 moveMu：同一时间只有一次核对，不会两条旧圈连接各签一份同样的加入记录。
func (n *Node) reconcileMove(ctx context.Context, peerID string) error {
	n.moveMu.Lock()
	defer n.moveMu.Unlock()
	rec, oldKey := n.activeMove()
	b, _ := n.snapshot()
	if rec == nil || oldKey == nil || b == nil {
		return nil
	}
	a, err := chain.Load(rec.OldChain)
	if err != nil || a.CircleID() != rec.OldCircle {
		return nil
	}
	bID := b.CircleID()
	var errs []error

	// 补签：在 B 的副本上一次签好，走 mergeChain 的正常持久化。
	next, err := chain.Load(b.Records())
	if err != nil {
		return err
	}
	var added []string
	for _, m := range a.Members() {
		if m.KeyID == n.id.KeyID || next.IsMember(m.KeyID) || slices.Contains(rec.Brought, m.KeyID) {
			continue
		}
		if _, ok := next.FormerMember(m.KeyID); ok {
			continue // 在 B 里被移除过：不因为它还在 A 里就重新签进来
		}
		if err := next.AddDevice(n.id, m.PublicKey, m.Name); err != nil {
			errs = append(errs, err)
			continue
		}
		added = append(added, m.KeyID)
	}
	if len(added) > 0 {
		if _, err := n.mergeChain(next.Records()); err != nil {
			errs = append(errs, err)
			added = nil
		} else {
			n.markLocalChange()
			n.mu.Lock()
			n.editMoveLocked(func(r *store.CircleMove) bool {
				if r.OldCircle != rec.OldCircle || n.chain == nil || n.chain.CircleID() != bID {
					return false // 中途换了圈，这份记录已经不是刚才核对的那份
				}
				ch := false
				for _, id := range added {
					if !n.chain.IsMember(id) {
						continue
					}
					if !slices.Contains(r.Brought, id) {
						r.Brought = append(r.Brought, id)
						ch = true
					}
					if !slices.Contains(r.Pending, id) {
						r.Pending = append(r.Pending, id)
						ch = true
					}
				}
				return ch
			})
			n.mu.Unlock()
			for _, id := range added {
				m, _ := a.Member(id)
				n.opts.Logf(i18n.T("「%s」是合并之后才加入原设备圈的，已把它加进现在的设备圈"), m.Name)
			}
		}
	}

	// 移除：RemoveDevice 自己拿 n.mu，这里不持有。它设置 localChange，下一次轮询断开被移除的设备、推送新链。
	for _, id := range rec.Pending {
		if id == n.id.KeyID || id == peerID {
			continue
		}
		if _, ok := a.FormerMember(id); !ok || a.IsMember(id) {
			continue
		}
		cur, _ := n.snapshot()
		if cur == nil || cur.CircleID() != bID || !cur.IsMember(id) {
			continue
		}
		name := ""
		if m, ok := cur.Member(id); ok {
			name = m.Name
		}
		if err := n.RemoveDevice(id); errors.Is(err, ErrRemovedKeyPending) {
			// 移除记录已经写进链里，只是新密钥还没启用：算移除了，之后的轮询会再启用。
			n.opts.Logf(i18n.T("移除「%s」之后启用新的发现密钥失败：%v"), name, err)
		} else if err != nil {
			errs = append(errs, err)
			continue
		}
		n.mu.Lock()
		n.editMoveLocked(func(r *store.CircleMove) bool {
			if r.OldCircle != rec.OldCircle || !slices.Contains(r.Pending, id) {
				return false
			}
			drop := func(s string) bool { return s == id }
			before := len(r.Brought) + len(r.Pending) + len(r.Elsewhere)
			r.Brought = slices.DeleteFunc(r.Brought, drop)
			r.Pending = slices.DeleteFunc(r.Pending, drop)
			r.Elsewhere = slices.DeleteFunc(r.Elsewhere, drop)
			return len(r.Brought)+len(r.Pending)+len(r.Elsewhere) != before
		})
		n.mu.Unlock()
		n.opts.Logf(i18n.T("「%s」在合并之后被移出了原设备圈，已把它移出现在的设备圈"), name)
	}
	return errors.Join(errs...)
}

// moveFailed 记下对 keyID 的旧圈连接没有成功，moveRetry 之内不再对它发起旧圈连接。
// 返回 true 表示这是冷却期之外的第一次（调用方据此只记一次日志：对方主动连过来时仍会每隔几秒重试）。
func (n *Node) moveFailed(keyID string) (first bool) {
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.moveTried == nil {
		return true
	}
	last, ok := d.moveTried[keyID]
	if ok && time.Since(last) < moveRetry {
		return false
	}
	d.moveTried[keyID] = time.Now()
	return true
}

// serveOldMember 服务一条旧圈连接：握手通过之后不登记为成员连接，只做两件事，最多 moveTimeout：
// 发迁移消息让对方跟过来、等回复；同时接收对方发来的迁移消息（对方也已经迁移过）。然后关闭连接。
func (n *Node) serveOldMember(ctx context.Context, conn *quic.Conn, peer *memberauth.Peer, forked bool) {
	defer conn.CloseWithError(0, "moved")
	n.opts.Logf(i18n.T("%s「%s」（%s）"), oldCircleLog(), peer.Name, describeAddr(conn))
	if err := n.store.SetPeerFeatures(peer.KeyID, peer.Features); err != nil {
		n.opts.Logf(i18n.T("记录设备功能失败：%v"), err)
	}
	if !slices.Contains(peer.Features, pairing.FeatureMerge) {
		// 旧版本不认识迁移消息，会丢掉它：不发，也不反复去连（握手时已经记进冷却期，见 authenticate）。
		n.opts.Logf(i18n.T("「%s」的 Portide 版本不支持合并设备圈，需要先升级才能跟过来"), peer.Name)
		return
	}
	mctx, cancel := context.WithTimeout(ctx, moveTimeout)
	defer cancel()

	sp := streamPeer{KeyID: peer.KeyID, Name: peer.Name}
	received := make(chan struct{})
	accepted := make(chan struct{})
	n.daemon.spawn(func() {
		defer close(accepted)
		if n.acceptOldStreams(ctx, mctx, conn, sp) {
			close(received)
		}
	})
	if n.sendCircleMove(mctx, conn, peer, forked) {
		// 对方也可能是帮手、正在给本机发迁移消息：等它处理完（或者对方断开）再关闭。
		select {
		case <-received:
		case <-conn.Context().Done():
		case <-mctx.Done():
		}
	}
	cancel()
	_ = conn.CloseWithError(0, "moved")
	<-accepted
}

// acceptOldStreams 接收旧圈连接上对方开来的流：是迁移消息就处理并返回 true，其他的丢掉。
// 连接断开或者 mctx 结束时返回 false。处理迁移消息用的是守护进程的 ctx（之后可能要换圈、重新拨号）。
func (n *Node) acceptOldStreams(ctx, mctx context.Context, conn *quic.Conn, peer streamPeer) bool {
	for {
		str, err := conn.AcceptStream(mctx)
		if err != nil {
			return false
		}
		_ = str.SetReadDeadline(time.Now().Add(dialTimeout))
		var env pb.Envelope
		err = wire.Read(str, &env)
		m, ok := env.Msg.(*pb.Envelope_CircleMove)
		if err != nil || !ok {
			// 例如对方还在来源圈里，照常推送它的链：这条连接不是成员连接，不处理。
			str.CancelRead(0)
			_ = str.Close()
			continue
		}
		n.handleCircleMove(ctx, peer, str, m.CircleMove)
		return true
	}
}

// circleMoveFor 生成发给 peerID 的迁移消息。对方已经不在 Pending 里、不是本机现在这个圈的成员，
// 或者迁移已经结束时返回 nil：新圈的链和密钥只发给还在等的新圈成员。
func (n *Node) circleMoveFor(peerID string) *pb.CircleMove {
	c, key := n.snapshot()
	rec, oldKey := n.activeMove()
	if c == nil || oldKey == nil || !c.IsMember(peerID) || !slices.Contains(rec.Pending, peerID) {
		return nil
	}
	relayConf := n.firstRelayString()
	return &pb.CircleMove{
		Chain: c.Records(), DiscoveryKey: key, RelayConfig: relayConf, RelaySettings: n.relaySettingsForWire(),
		OldChain: rec.OldChain, ExpiresAt: rec.ExpiresAt.Unix(),
	}
}

// sendCircleMove 在旧圈连接上发迁移消息并处理回复。返回是否应该再等一会儿对方的迁移消息
// （对方已经迁移过，或者回复说去了别的圈，它也是帮手）。
func (n *Node) sendCircleMove(mctx context.Context, conn *quic.Conn, peer *memberauth.Peer, forked bool) bool {
	if !forked {
		if err := n.reconcileMove(mctx, peer.KeyID); err != nil {
			n.opts.Logf(i18n.T("核对原设备圈的变化失败：%v"), err)
		}
	}
	m := n.circleMoveFor(peer.KeyID)
	if m == nil {
		return true // 不用再带它（刚刚跟过来了）；对方可能还在给本机发
	}
	str, err := conn.OpenStreamSync(mctx)
	if err != nil {
		if n.moveFailed(peer.KeyID) {
			n.opts.Logf(i18n.T("请「%s」跟过来失败：%v"), peer.Name, err)
		}
		return false
	}
	if dl, ok := mctx.Deadline(); ok {
		_ = str.SetDeadline(dl)
	}
	var ack pb.CircleMoveAck
	err = wire.Write(str, &pb.Envelope{Msg: &pb.Envelope_CircleMove{CircleMove: m}})
	if err == nil {
		err = wire.ReadMax(str, &ack, maxMoveAck)
	}
	// 读完回复再关掉本机这一头：对方等到这个才继续（例如换圈、断开这条连接），回复不会被断开连接截掉。
	_ = str.Close()
	switch {
	case err != nil:
		if n.moveFailed(peer.KeyID) {
			n.opts.Logf(i18n.T("请「%s」跟过来失败：%v"), peer.Name, err)
		}
		return false
	case ack.Error == "":
		n.movedPeer(peer.KeyID)
		n.opts.Logf(i18n.T("「%s」已经跟过来"), peer.Name)
		return true
	case ack.Error == moveElsewhereMsg:
		n.movedElsewhere(peer.KeyID)
		if n.moveFailed(peer.KeyID) {
			n.opts.Logf(i18n.T("「%s」已经合并到了别的设备圈，没有跟过来"), peer.Name)
		}
		return true
	default:
		if n.moveFailed(peer.KeyID) {
			n.opts.Logf(i18n.T("「%s」没有跟过来：%s"), peer.Name, ack.Error)
		}
		return false
	}
}

// handleCircleMove 处理已经迁移的设备 peer 发来的迁移消息（本机是接收方），在同一条流上回复。
func (n *Node) handleCircleMove(ctx context.Context, peer streamPeer, str *quic.Stream, m *pb.CircleMove) {
	_ = str.SetDeadline(time.Now().Add(moveTimeout))
	reply, reason := n.acceptCircleMove(peer, m)
	if reply != "" {
		n.opts.Logf(i18n.T("没有跟随「%s」换设备圈：%s"), peer.Name, reply)
	}
	_ = wire.Write(str, &pb.CircleMoveAck{Error: reply})
	_ = str.Close()
	// 等对方读完回复、关掉它那一头再继续：换圈之后这条连接会被断开，太早断开回复可能还没送到。
	_, _ = io.Copy(io.Discard, str)
	if reason != "" {
		n.onChainChanged(ctx, reason)
	}
}

// acceptCircleMove 校验迁移消息并照做，返回给对方的回复（空串表示成功）和本机链变化时 onChainChanged 的说明。
// 一次只处理一条：一台设备只接受一次迁移，先到先得。
func (n *Node) acceptCircleMove(peer streamPeer, m *pb.CircleMove) (reply, reason string) {
	n.moveMu.Lock()
	defer n.moveMu.Unlock()
	nc, err := chain.Load(m.Chain)
	if err != nil || !nc.IsMember(n.id.KeyID) || !nc.IsMember(peer.KeyID) {
		return moveBadChainMsg, ""
	}
	if len(m.DiscoveryKey) != discoveryKeyLen ||
		(nc.KeyCheck() != nil && !bytes.Equal(chain.DiscoveryKeyCheck(m.DiscoveryKey), nc.KeyCheck())) {
		return moveBadKeyMsg, ""
	}
	cur, curKey := n.snapshot()
	if cur == nil {
		return moveElsewhereMsg, ""
	}
	// 本机已经在新圈里（例如两台帮手碰上了）：同步新圈的链，对方算是跟过来了。
	if cur.CircleID() == nc.CircleID() {
		changed, err := n.applyChain(m.Chain)
		if err != nil {
			if changed {
				reason = i18n.Tf("从 %s 收到设备圈更新", peer.Name)
			}
			return moveBadChainMsg, reason
		}
		if changed {
			reason = i18n.Tf("从 %s 收到设备圈更新", peer.Name)
		}
		newKey, err := n.adoptDiscoveryKey(m.DiscoveryKey)
		if err != nil {
			// 新密钥没存下来：不算跟过来，对方过一阵再试。
			n.opts.Logf(i18n.T("保存 %s 下发的发现密钥失败：%v"), peer.Name, err)
			return moveLocalErrMsg, reason
		}
		if newKey {
			reason = i18n.Tf("从 %s 收到新的发现密钥", peer.Name)
		}
		forked := false
		n.mu.Lock()
		n.editMoveLocked(func(rec *store.CircleMove) bool {
			had := slices.Contains(rec.Pending, peer.KeyID) || slices.Contains(rec.Elsewhere, peer.KeyID)
			rec.Pending = slices.DeleteFunc(rec.Pending, func(s string) bool { return s == peer.KeyID })
			rec.Elsewhere = slices.DeleteFunc(rec.Elsewhere, func(s string) bool { return s == peer.KeyID })
			ch, f := mergeOldChain(rec, m.OldChain)
			forked = f
			return had || ch
		})
		n.mu.Unlock()
		if forked {
			n.opts.Logf("%s", oldForkLog())
		}
		if changed || newKey {
			reason = i18n.Tf("从 %s 收到设备圈更新", peer.Name)
		}
		return "", reason
	}
	oc, err := chain.Load(m.OldChain)
	if err != nil || cur.CircleID() != oc.CircleID() {
		return moveElsewhereMsg, ""
	}
	// 本机还在来源圈里：跟过去。
	// 等待期从合并算起，最长 30 天：对方给的期限再长也不超过从现在起的 30 天。
	expires := time.Unix(m.ExpiresAt, 0)
	if limit := time.Now().Add(moveGrace); expires.After(limit) {
		expires = limit
	}
	if !time.Now().Before(expires) {
		return moveExpiredMsg, ""
	}
	if !cur.IsMember(peer.KeyID) {
		return moveBadChainMsg, ""
	}
	// 最新的来源圈：本机的链合并对方记下的；分叉时用本机的。
	latest, err := chain.Load(cur.Records())
	if err != nil {
		return moveBadChainMsg, ""
	}
	if _, err := latest.Merge(m.OldChain); err != nil {
		if errors.Is(err, chain.ErrFork) {
			n.opts.Logf("%s", oldForkLog())
		}
		if latest, err = chain.Load(cur.Records()); err != nil {
			return moveBadChainMsg, ""
		}
	}
	var brought, pending []string
	for _, mem := range latest.Members() {
		if mem.KeyID == n.id.KeyID || !nc.IsMember(mem.KeyID) {
			continue
		}
		brought = append(brought, mem.KeyID)
		if mem.KeyID != peer.KeyID {
			pending = append(pending, mem.KeyID)
		}
	}
	mv := &newMove{
		rec: &store.CircleMove{
			OldCircle: cur.CircleID(), OldChain: latest.Records(),
			Brought: brought, Pending: pending, ExpiresAt: expires,
		},
		oldKey: curKey,
	}
	if err := n.switchCircle(m.Chain, m.DiscoveryKey, m.RelayConfig, m.RelaySettings, 0, mv); err != nil {
		n.opts.Logf(i18n.T("跟随「%s」换设备圈失败：%v"), peer.Name, err)
		return moveLocalErrMsg, ""
	}
	return "", i18n.Tf("已跟随「%s」合并到新的设备圈", peer.Name)
}

package node

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/chain"
	"portide/core/discovery"
	"portide/core/pairing"
	"portide/core/store"
	"portide/core/transport"
)

// browsePairFor 是加入方在局域网里寻找邀请方的时长。
const browsePairFor = 4 * time.Second

// ErrNoInviter 表示没给邀请方地址、在局域网里也没找到正在等待配对的设备。
var ErrNoInviter = errors.New("node: no inviting device found on the local network (is `portide pair` running? try --addr)")

// JoinResult 是 Join 成功后的信息。
type JoinResult struct {
	CircleID string
	Members  []string     // 加入后本机所在圈的成员名
	Mode     pairing.Mode // ModeJoin：本机换到了对方的圈；ModeAdopt：对方换到了本机的圈
	Peer     string       // 对方的设备名
	// Waiting 是合并（ModeMerge）后原来设备圈里还没跟过来的设备名，按原来设备圈的成员顺序。
	Waiting []string
	// Outdated 是 Waiting 里需要先升级的设备名：按设备 KeyID 判断，每台需要升级的设备一项（同名的设备各出现一次，不去重）。
	Outdated []string
}

// Join 用配对码和邀请方配对。addrs 为空时在局域网里通过 mDNS 寻找正在邀请的设备。
// 本机不在圈里或单机时加入对方的圈（JOIN）；本机圈里有其他设备、对方单机时，对方换到本机的圈（ADOPT）；
// 两边都有多台设备时是合并（MERGE），mergeOK 表示用户已经同意：本机带着原来设备圈的成员换到对方的圈，
// 留下迁移记录等它们跟过来；对方的版本不支持合并时返回 pairing.ErrMergeUnsupported，什么都不改。
// Join 不需要守护进程：它用一个临时端口拨出，成功后把结果写入本机。守护进程在运行时也可以调用，
// 它的下一次轮询会发现链变了（换圈时还会断开旧圈的连接），推送给成员。
func (n *Node) Join(ctx context.Context, code string, addrs []string, mergeOK bool) (*JoinResult, error) {
	// 邀请方会拒绝空名字（"invalid device"），在这里先说清楚。
	if n.Name() == "" {
		return nil, ErrNoName
	}
	code, err := pairing.NormalizeCode(code)
	if err != nil {
		return nil, err
	}
	t, err := transport.Listen(":0")
	if err != nil {
		return nil, err
	}
	defer t.Close()

	if len(addrs) == 0 {
		if n.opts.DisableMDNS {
			return nil, errors.New("node: no inviter address given and mDNS is disabled")
		}
		addrs = n.browsePair(ctx)
		if len(addrs) == 0 {
			return nil, ErrNoInviter
		}
	}

	var lastErr error
	for _, addr := range addrs {
		// 拨号单独限时：对端发布的地址里可能有到不了的（例如虚拟网卡），不能在一个地址上卡到握手超时。
		dialCtx, cancelDial := context.WithTimeout(ctx, dialTimeout)
		conn, err := t.DialPair(dialCtx, addr)
		cancelDial()
		if err != nil {
			lastErr = err
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, pairTimeout)
		joined, sent, err := n.runJoin(dctx, conn, code, mergeOK)
		_ = conn.CloseWithError(0, "")
		cancel()
		if isFinalPairError(err) {
			return nil, err // 不再尝试其他设备：输错配对码会消耗它们的尝试次数，其余原因换一台也一样
		}
		if err != nil {
			lastErr = err
			continue
		}
		// 各种情况都记下邀请方地址，守护进程直接连接它。换圈之前写：守护进程一看到换了圈就立刻拨号，
		// 这时必须已经有地址（ADOPT 的链已经在 Commit 里写入，这里也是尽早写）。
		_ = n.store.UpsertPeer(store.Peer{KeyID: joined.InviterKeyID, Addr: addr, LastSeen: time.Now()})
		var waiting, outdated []string
		switch joined.Mode {
		case pairing.ModeJoin:
			// 本机丢弃自己的圈（单机圈或还没有圈），换到邀请方的圈；中继列表是邀请方的与本机原来的合并。
			if err := n.switchCircle(joined.Chain.Records(), joined.DiscoveryKey, joined.RelayConfig, joined.RelaySettings, 0, nil); err != nil {
				return nil, err
			}
		case pairing.ModeMerge:
			// 本机带着原来设备圈的成员换到邀请方的圈，留下迁移记录和原来的发现密钥，等它们跟过来。
			var mv *newMove
			mv, waiting, outdated = n.mergeMove(sent, joined.Chain, joined.InviterKeyID)
			if err := n.switchCircle(joined.Chain.Records(), joined.DiscoveryKey, joined.RelayConfig, joined.RelaySettings, 0, mv); err != nil {
				return nil, err
			}
		}
		c, _ := n.snapshot()
		res := &JoinResult{CircleID: c.CircleID(), Mode: joined.Mode, Peer: joined.InviterName, Waiting: waiting, Outdated: outdated}
		for _, m := range c.Members() {
			res.Members = append(res.Members, m.Name)
		}
		return res, nil
	}
	return nil, fmt.Errorf("node: could not pair with any inviter: %w", lastErr)
}

// isFinalPairError 报告 err 是否是不该换一台设备再试的配对结果。
func isFinalPairError(err error) bool {
	for _, e := range []error{pairing.ErrWrongCode, pairing.ErrSameCircle, pairing.ErrInviterOutdated,
		pairing.ErrMergeUnsupported, pairing.ErrMergeNotConfirmed, pairing.ErrUnexpectedJoin, pairing.ErrStaleCircle} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// sentCircle 是加入方在一次配对里交出去的本机设备圈：链的副本和当时的发现密钥。本机不在圈里时为空。
type sentCircle struct {
	chain *chain.Chain
	key   []byte
}

// runJoin 在配对连接上打开一条流，并在其上执行加入方协议。同时返回本次交出去的本机设备圈，合并时据此写迁移记录。
func (n *Node) runJoin(ctx context.Context, conn *quic.Conn, code string, mergeOK bool) (*pairing.Joined, sentCircle, error) {
	var sent sentCircle
	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, sent, err
	}
	defer str.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = str.SetDeadline(dl)
	}
	exp, err := exportKeyingMaterial(conn, pairing.ExporterLabel, pairing.ExporterLen)
	if err != nil {
		return nil, sent, err
	}
	req := pairing.JoinRequest{Self: n.id, Name: n.Name(), Code: code, Features: n.features, MergeOK: mergeOK}
	// 本机在圈里：交出链的副本（pairing.Join 据成员数决定流程）、发现密钥和中继设置（ADOPT 时发给对方）。
	// 每次尝试都重新取：上一次尝试可能已经提交了"加入 对方"。
	if current, key := n.snapshot(); current != nil {
		if req.Chain, err = chain.Load(current.Records()); err != nil {
			return nil, sent, err
		}
		sent = sentCircle{chain: req.Chain, key: key}
		req.DiscoveryKey = key
		req.RelayConfig = n.firstRelayString()
		req.RelaySettings = n.relaySettingsForWire()
		// ADOPT：把"加入 对方"合并进本机的链（持久化并更新内存）。之后即使对方没能换圈、回复没送到，
		// 本机也是一致的：链里多了一个离线的成员，标记 localChange 让守护进程把新链推给其他成员；
		// 用户在对方上重新显示配对码再配一次即可（设计方案 6.4）。
		req.Commit = func(records [][]byte) error {
			changed, err := n.mergeChain(records)
			if changed {
				n.markLocalChange()
			}
			return err
		}
	}
	joined, err := pairing.Join(str, exp, req)
	return joined, sent, err
}

// mergeMove 算出合并（本机是加入方）之后的迁移记录：原来设备圈里除本机和邀请方以外、已经在新圈 joined 里的成员，
// 都要等它们跟过来。waiting 是这些设备的名字，按原来设备圈的成员顺序。邀请方本来就在新圈里，不用等它。
func (n *Node) mergeMove(sent sentCircle, joined *chain.Chain, inviter string) (mv *newMove, waiting, outdated []string) {
	var ids []string
	for _, m := range sent.chain.Members() {
		if m.KeyID == n.id.KeyID || m.KeyID == inviter {
			continue
		}
		if _, ok := joined.Member(m.KeyID); !ok {
			continue
		}
		ids = append(ids, m.KeyID)
		waiting = append(waiting, m.Name)
	}
	rec := &store.CircleMove{
		OldCircle: sent.chain.CircleID(),
		OldChain:  sent.chain.Records(),
		Brought:   ids,
		Pending:   slices.Clone(ids),
		ExpiresAt: time.Now().Add(moveGrace),
	}
	// 需要先升级的设备：规则和设备列表里的 MoveOutdated 相同（按 KeyID 判断，结果是名字）。
	if feats, err := n.store.PeerFeatures(); err == nil {
		for _, m := range sent.chain.Members() {
			if slices.Contains(ids, m.KeyID) && moveStatus(rec, m.KeyID, feats[m.KeyID]) == MoveOutdated {
				outdated = append(outdated, m.Name)
			}
		}
	}
	return &newMove{rec: rec, oldKey: sent.key}, waiting, outdated
}

// NearbyInviters 报告局域网里是否有设备正在开放配对（浏览 browsePairFor）。
func (n *Node) NearbyInviters(ctx context.Context) bool {
	if n.opts.DisableMDNS { // 关闭了局域网发现：不浏览（discoverer 仍会返回真实的 mDNS）
		return false
	}
	return len(n.browsePair(ctx)) > 0
}

// browsePair 在局域网里收集正在邀请的设备地址。
func (n *Node) browsePair(ctx context.Context) []string {
	bctx, cancel := context.WithTimeout(ctx, browsePairFor)
	defer cancel()
	var mu sync.Mutex
	var out []string
	seen := map[string]bool{}
	_ = n.discoverer().Browse(bctx, discovery.PairService, func(f discovery.Found) {
		mu.Lock()
		defer mu.Unlock()
		for _, a := range f.Addrs {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	})
	mu.Lock()
	defer mu.Unlock()
	return out
}

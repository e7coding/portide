package node

import (
	"time"

	"portide/core/chain"
	"portide/core/keystore"
	"portide/core/store"
)

// 切换设备圈（合并设备圈设计第 7 节）：JOIN 的加入方、ADOPT 的邀请方都要从当前的圈（通常是单机圈）换到另一个圈。
// 单机圈被丢弃时没有别的成员需要通知；LeaveCircle 是这个操作的"只删除"版本。
// 历史按对方的 KeyID 记录，换圈前后 KeyID 不变，所以照常显示；排队的发送任务对方仍是成员的照常发送。

// switchCircle 把本机换到 records 描述的设备圈：作废还在等待的配对码（keepSession 除外：ADOPT 的邀请方
// 正在用它，随后记为完成），写入发现密钥和链，清空旧圈的密钥环；中继列表是收到的 relaySettings（对方是旧版本时
// 是 relayConfig 这一项）与本机原来的列表的合并（见 combineRelaysOnSwitch）。
// 守护进程在运行时，下一次轮询发现 localChange、圈 ID 变了，就断开旧圈的连接、重启发现和中继订阅。
// mv 不为 nil（合并）时写入新的迁移记录和来源圈的发现密钥；为 nil 时清掉以前的：本机已经不在那次合并的新圈里了，
// 不能再用旧密钥把别的设备带进一个本机已经离开的圈。
// 写入顺序：旧密钥 → 迁移记录 → 新密钥 → 密钥环 → 链。中途失败时最多留下一条指向本机当前圈、链还没换的记录，
// activeMove 不会用它，之后的同步会删掉它。
func (n *Node) switchCircle(records [][]byte, discoveryKey []byte, relayConfig string, relaySettings []byte, keepSession int64, mv *newMove) error {
	c, err := chain.Load(records)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.voidPendingPairing(keepSession); err != nil {
		return err
	}
	if err := n.saveMoveLocked(mv); err != nil {
		return err
	}
	// 先密钥后链：有链就一定有密钥。旧圈的密钥环（轮换下来的旧密钥）在新圈里没有用。
	if err := n.opts.Keys.Set(keystore.DiscoveryKey, discoveryKey); err != nil {
		return err
	}
	if err := saveRing(n.opts.Keys, nil); err != nil {
		return err
	}
	if err := n.store.SaveChain(records); err != nil {
		return err
	}
	n.combineRelaysOnSwitch(c, relaySettings, relayConfig)
	n.localChange = true
	return n.useChainLocked(c)
}

// voidPendingPairing 作废还在等待的配对码（记为已取消），keep 那个会话保留。只读写数据库，调用方可以持有 n.mu。
// 不作废的话，换圈或离开之后守护进程会用它把别的设备拉进本机现在所在的圈。
func (n *Node) voidPendingPairing(keep int64) error {
	for {
		ps, err := n.store.PendingPairSession(time.Now())
		if err != nil {
			return err
		}
		// 新建会话会作废之前所有未完成的，所以等待中的只有最新的一个：轮到 keep 就停下。
		if ps == nil || ps.ID == keep {
			return nil
		}
		if err := n.store.FinishPairSession(ps.ID, store.PairFailed, "", store.PairErrCancelled); err != nil {
			return err
		}
	}
}

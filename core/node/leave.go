package node

import (
	"context"
	"errors"
	"portide/core/i18n"
	"fmt"
	"slices"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/pb"
	"portide/core/wire"
)

// 主动退出设备圈（例如两台设备各自新建了设备圈，其中一台改为用配对码加入另一台的）。
// 签名链规定设备不能移除自己，所以退出的设备请求一台在线的成员用普通的移除记录把它移出去
// （LeaveRequest）：移除同时轮换发现密钥，退出的设备拿不到新密钥；旧版本也读得懂这样的链。
// 请求完（或者没有成员能处理、用户确认仍然离开）之后，本机用 LeaveCircle 删掉自己的设备圈记录。

// FeatureLeave 是握手里声明"能处理 LeaveRequest"的功能名。
const FeatureLeave = "leave"

// leaveTimeout 是向一台成员请求移出、等它回复的时限。
const leaveTimeout = 10 * time.Second

// ErrNoLeaveHelper 表示没有在线、而且支持 LeaveRequest 的成员，没法让设备圈把本机移出去。
var ErrNoLeaveHelper = errors.New("node: no online device can remove this one from the circle")

// RequestLeave 请求一台在线成员把本机移出设备圈，返回它的设备名。需要本 Node 的守护进程正在运行
// （请求走已有的成员连接）。依次尝试每台声明过 FeatureLeave 的在线成员，第一台成功即返回；
// 一台都没有时返回 ErrNoLeaveHelper，全都失败时返回最后一个错误。
// 成功之后本机已不是成员（对方会断开连接），调用方应停止守护进程、再用 LeaveCircle 删掉本机的设备圈记录。
func (n *Node) RequestLeave(ctx context.Context) (string, error) {
	n.mu.Lock()
	d, inCircle := n.daemon, n.chain != nil
	n.mu.Unlock()
	if !inCircle {
		return "", ErrNotInCircle
	}
	if d == nil {
		return "", errors.New("node: the daemon is not running")
	}
	known, err := n.store.PeerFeatures()
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	var conns []*memberConn
	for id, mc := range d.conns {
		if mc.conn.Context().Err() == nil && slices.Contains(known[id], FeatureLeave) {
			conns = append(conns, mc)
		}
	}
	d.mu.Unlock()
	if len(conns) == 0 {
		return "", ErrNoLeaveHelper
	}
	var lastErr error
	for _, mc := range conns {
		if err := n.askToLeave(ctx, mc); err != nil {
			n.opts.Logf(i18n.T("请求「%s」把本机移出设备圈失败：%v"), n.memberName(mc.peerID), err)
			lastErr = err
			continue
		}
		return n.memberName(mc.peerID), nil
	}
	return "", lastErr
}

// askToLeave 在成员连接 mc 上发送 LeaveRequest 并等待回复。
func (n *Node) askToLeave(ctx context.Context, mc *memberConn) error {
	ctx, cancel := context.WithTimeout(ctx, leaveTimeout)
	defer cancel()
	str, err := mc.conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer str.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = str.SetDeadline(dl)
	}
	if err := wire.Write(str, &pb.Envelope{Msg: &pb.Envelope_LeaveRequest{LeaveRequest: &pb.LeaveRequest{}}}); err != nil {
		return err
	}
	var reply pb.LeaveReply
	if err := wire.Read(str, &reply); err != nil {
		return err
	}
	if reply.Error != "" {
		return fmt.Errorf("node: the device refused: %s", reply.Error)
	}
	return nil
}

// handleLeaveRequest 处理成员 peer 的退出请求：用移除记录把它移出设备圈（轮换发现密钥），回复结果，
// 然后断开它、把新链推给其他成员。
func (n *Node) handleLeaveRequest(ctx context.Context, peer streamPeer, str *quic.Stream) {
	defer str.Close()
	_ = str.SetDeadline(time.Now().Add(leaveTimeout))
	err := n.RemoveDevice(peer.KeyID)
	if errors.Is(err, ErrRemovedKeyPending) {
		// 移除记录已经写进链里，只是新密钥还没启用：对请求方来说已经移出了，下一次轮询会补上密钥。
		n.opts.Logf(i18n.T("「%s」退出了设备圈，但新的发现密钥还没有启用：%v"), peer.Name, err)
		err = nil
	}
	reply := &pb.LeaveReply{}
	if err != nil {
		reply.Error = err.Error()
		n.opts.Logf(i18n.T("「%s」请求退出设备圈，没能移除：%v"), peer.Name, err)
	}
	if werr := wire.Write(str, reply); werr != nil || err != nil {
		return
	}
	// 等对方读完回复、关掉这条流再断开连接：马上断开的话，回复可能还没送到。读到流结束或超时都算。
	_, _ = str.Read(make([]byte, 1))
	n.onChainChanged(ctx, i18n.Tf("「%s」退出了设备圈", peer.Name))
}

// LeaveCircle 删掉本机的设备圈记录，回到"还不在设备圈里"，之后可以用配对码加入别的设备圈。
// 设备身份、历史和设置都保留。它只改本机：设备圈里还有别的设备时，应该先用 RequestLeave 让它们把本机移出，
// 不然它们会一直把本机当成员（显示离线、给它排队发送），要在它们上面手动移除。
// 守护进程（本 Node 的和别的进程的）必须没有在运行：它会一直用当前的链和发现密钥，也会继续开放配对。
// Node 只能 Run 一次，所以运行过守护进程的 Node 要关掉、重新 Open 一个再调用它。
func (n *Node) LeaveCircle() error {
	if n.DaemonRunning() {
		return errors.New("node: stop the daemon before leaving the circle")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.daemon != nil {
		return errors.New("node: stop the daemon before leaving the circle")
	}
	if n.chain == nil {
		return nil
	}
	// 还在等待的配对码作废：不然加入别的设备圈之后，守护进程会用它把新设备拉进那个设备圈。
	if err := n.voidPendingPairing(0); err != nil {
		return err
	}
	// 合并留下的迁移记录和来源圈的发现密钥先清掉：离开之后不能再去把旧设备带进一个本机已经不在的圈。
	// 放在删链之前，中途失败时本机还在圈里、只是少了迁移记录。
	if err := n.saveMoveLocked(nil); err != nil {
		return err
	}
	// 先删链再清密钥环：中途失败时最多留下用不到的密钥，不会出现有链没密钥。
	// 发现密钥本身不用删：加入时会被覆盖，不在设备圈里时也不会被用到。
	if err := n.store.SaveChain(nil); err != nil {
		return err
	}
	if err := saveRing(n.opts.Keys, nil); err != nil {
		return err
	}
	n.chain, n.discKey, n.retired = nil, nil, nil
	return nil
}

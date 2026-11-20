package node

import (
	"errors"
	"fmt"
	"slices"

	"portide/core/chain"
	"portide/core/pairing"
)

// FeatureRename 是握手里声明"能理解改名记录"的功能名（M3c）。
// 旧版本不认识改名记录，会拒绝含有它的链；所以只有其他成员全都声明过它，才允许改名。
const FeatureRename = chain.FeatureRename

// FeatureRelays 是握手里声明"能理解中继设置"的功能名：只有声明了它的成员才会收到中继设置。
const FeatureRelays = "relays"

// features 是本机在握手里声明的功能。
var features = []string{FeatureRename, FeatureLeave, pairing.FeatureCircle2, pairing.FeatureMerge, FeatureRelays}

// ErrRenameUnsupported 表示还有成员没升级到支持改名的版本。
var ErrRenameUnsupported = errors.New("node: some devices do not support renaming yet")

// CanRename 报告现在能否改名；不能时返回还没声明支持改名的设备名（从没连上过的设备也算）。
func (n *Node) CanRename() (bool, []string, error) {
	n.mu.Lock()
	c := n.chain
	n.mu.Unlock()
	if c == nil {
		return false, nil, ErrNotInCircle
	}
	missing, err := n.renameUnsupported(c)
	if err != nil {
		return false, nil, err
	}
	return len(missing) == 0, missing, nil
}

// renameUnsupported 返回链 c 上除本机以外、还没声明支持改名的成员名。只读数据库，调用方可以持有 n.mu。
func (n *Node) renameUnsupported(c *chain.Chain) ([]string, error) {
	known, err := n.store.PeerFeatures()
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, m := range c.Members() {
		if m.KeyID != n.id.KeyID && !slices.Contains(known[m.KeyID], FeatureRename) {
			missing = append(missing, m.Name)
		}
	}
	return missing, nil
}

// RenameDevice 把成员 keyID（可以是本机）改名为 name：追加一条由本机签名的改名记录，之后像其他链更新一样同步给各成员。
func (n *Node) RenameDevice(keyID, name string) error {
	// 名字没变就不追加：改名记录永久留在链上，每次握手都要传。
	if c, _ := n.snapshot(); c != nil {
		if m, ok := c.Member(keyID); ok && m.Name == name {
			return nil
		}
	}
	ok, missing, err := n.CanRename()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %v", ErrRenameUnsupported, missing)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	// 以数据库里的链为准：守护进程可能刚合并了别的更新。
	if _, err := n.reloadCircleLocked(); err != nil {
		return err
	}
	next, err := chain.Load(n.chain.Records())
	if err != nil {
		return err
	}
	if err := next.RenameDevice(n.id, keyID, name); err != nil {
		return fmt.Errorf("node: %w", err)
	}
	if err := n.store.AppendChain(n.chain.Len(), next.Records()); err != nil {
		return err
	}
	n.localChange = true
	return n.useChainLocked(next)
}

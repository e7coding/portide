package node

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"portide/core/chain"
	"portide/core/keystore"
)

// retiredKeyGrace 是轮换下来的发现密钥继续被接受的时长：移除设备时不在线的成员还拿着旧密钥，
// 它们在这段时间内连进来（本机作为接收方）仍能通过圈子证明，随后在加密通道里拿到新密钥。
// 被移除的设备也有旧密钥，但成员握手在本机出示身份之前就会因为它已不是成员而拒绝它。
const retiredKeyGrace = 30 * 24 * time.Hour

// staleStagedGrace 是一把"已生成、还没有启用"的新密钥在密钥环里保留的时长：移除记录写进链里之后，
// 如果一直没有机会启用它（例如这个移除从未被别的成员采纳、后来被别的移除覆盖），就不能永久占着
// 密钥库的空间（操作系统密钥库的大小有限）。
const staleStagedGrace = 24 * time.Hour

// ringEntry 是密钥环里的一把发现密钥。
type ringEntry struct {
	Key     []byte `json:"key"`
	Retired int64  `json:"retired"`          // 停用时间（Unix 秒）；0 表示已经生成、还没有启用
	Staged  int64  `json:"staged,omitempty"` // 生成（stage）时间（Unix 秒）；没有记录（旧版本写入）时为 0
}

func loadRing(keys keystore.Store) ([]ringEntry, error) {
	b, err := keys.Get(keystore.DiscoveryKeyRing)
	if errors.Is(err, keystore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ring []ringEntry
	if err := json.Unmarshal(b, &ring); err != nil {
		return nil, fmt.Errorf("node: discovery key ring: %w", err)
	}
	return ring, nil
}

func saveRing(keys keystore.Store, ring []ringEntry) error {
	b, err := json.Marshal(ring)
	if err != nil {
		return err
	}
	return keys.Set(keystore.DiscoveryKeyRing, b)
}

// addToRing 把 e 写进密钥环（同一把密钥只留一条，已有的保留较早的停用时间）。
func addToRing(ring []ringEntry, e ringEntry) []ringEntry {
	for i := range ring {
		if bytes.Equal(ring[i].Key, e.Key) {
			if ring[i].Retired == 0 || (e.Retired != 0 && e.Retired < ring[i].Retired) {
				ring[i].Retired = e.Retired
			}
			return ring
		}
	}
	return append(ring, e)
}

// circleKeySet 是本机当前使用和接受的发现密钥。
type circleKeySet struct {
	current []byte
	retired []ringEntry // 只含已停用的密钥
}

// accepted 返回作为接收方时接受的发现密钥：当前密钥在前，其后是宽限期内的旧密钥。
func (k circleKeySet) accepted(now time.Time) [][]byte {
	out := [][]byte{k.current}
	for _, e := range k.retired {
		if now.Sub(time.Unix(e.Retired, 0)) < retiredKeyGrace {
			out = append(out, e.Key)
		}
	}
	return out
}

// planDiscoveryKeys 按签名链规划当前发现密钥变化：链里最近一条移除记录的校验值对得上哪一把，哪一把就是当前密钥。
// 当前密钥对不上、密钥环里有对得上的（本机刚移除了设备，或者刚收到成员下发的新密钥）时启用它，
// 原来的当前密钥移进密钥环、记下停用时间。都对不上表示本机还没拿到新密钥（移除时不在线），
// 继续用原来的密钥。dirty 为 true 表示需要改密钥库（启用或清理）；当 write 为 false 且需要启用时返回 dirty=true，
// 让调用方重做一遍但这次拿着跨进程锁（另一个进程可能同时在改）。
func planDiscoveryKeys(keys keystore.Store, c *chain.Chain, now time.Time, write bool) (set circleKeySet, dirty bool, err error) {
	cur, err := keys.Get(keystore.DiscoveryKey)
	if err != nil {
		return circleKeySet{}, false, fmt.Errorf("node: discovery key: %w", err)
	}
	ring, err := loadRing(keys)
	if err != nil {
		return circleKeySet{}, false, err
	}
	check := c.KeyCheck()
	if check != nil && !bytes.Equal(chain.DiscoveryKeyCheck(cur), check) {
		for _, e := range ring {
			if !bytes.Equal(chain.DiscoveryKeyCheck(e.Key), check) {
				continue
			}
			next := e.Key
			ring = addToRing(ring, ringEntry{Key: cur, Retired: now.Unix()})
			dirty = true
			if write {
				if err := saveRing(keys, ring); err != nil {
					return circleKeySet{}, false, err
				}
				if err := keys.Set(keystore.DiscoveryKey, next); err != nil {
					return circleKeySet{}, false, err
				}
			}
			cur = next
			break
		}
	}
	// 清理：当前密钥不再留在密钥环里，过了宽限期的旧密钥删掉。还没启用的新密钥保留（可能属于另一个进程正在做的移除），
	// 但staged 超过 staleStagedGrace 还没启用就认定它永远不会被启用了，一并删掉；没有记录 staged 时间的（旧版本写入）不删。
	var kept []ringEntry
	set = circleKeySet{current: cur}
	for _, e := range ring {
		switch {
		case bytes.Equal(e.Key, cur):
		case e.Retired != 0 && now.Sub(time.Unix(e.Retired, 0)) >= retiredKeyGrace:
			dirty = true
		case e.Retired == 0 && e.Staged != 0 && now.Sub(time.Unix(e.Staged, 0)) >= staleStagedGrace:
			dirty = true
		default:
			kept = append(kept, e)
			if e.Retired != 0 {
				set.retired = append(set.retired, e)
			}
		}
	}
	if len(kept) != len(ring) {
		dirty = true
		if write {
			if err := saveRing(keys, kept); err != nil {
				return circleKeySet{}, false, err
			}
		}
	}
	return set, dirty, nil
}

// resolveDiscoveryKeys 按签名链确定当前发现密钥，必要时改密钥库。使用无锁快路径读一遍；如果需要改密钥库就拿跨进程锁再读一遍再改
// （另一个进程可能同时在改）。这样打开设备时不必一直持有数据库写锁。
func resolveDiscoveryKeys(keys keystore.Store, lock func(func() error) error, c *chain.Chain, now time.Time) (circleKeySet, error) {
	set, dirty, err := planDiscoveryKeys(keys, c, now, false)
	if err != nil || !dirty {
		return set, err
	}
	// 要改密钥库：拿跨进程的锁，在锁内重新读一遍再改（另一个进程可能刚刚改过）。
	err = lock(func() error {
		set, _, err = planDiscoveryKeys(keys, c, now, true)
		return err
	})
	return set, err
}

// stageDiscoveryKey 把一把还没启用的新发现密钥写进密钥环（先于签名链写入：有链就一定有密钥）。
func stageDiscoveryKey(keys keystore.Store, lock func(func() error) error, key []byte) error {
	return lock(func() error {
		ring, err := loadRing(keys)
		if err != nil {
			return err
		}
		return saveRing(keys, addToRing(ring, ringEntry{Key: key, Staged: time.Now().Unix()}))
	})
}

func newDiscoveryKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

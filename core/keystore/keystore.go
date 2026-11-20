// Package keystore 保存设备的秘密（设备密钥种子、发现密钥、数据库密钥）。
// 优先使用操作系统密钥库；不可用时退回到用口令加密的本地文件。
package keystore

import (
	"errors"
	"sync"
)

// 存储项名称。
const (
	DeviceSeed   = "device-seed"
	DiscoveryKey = "discovery-key"
	DatabaseKey  = "database-key" // 数据库字段加密的密钥（设计方案 6.2）
	// DiscoveryKeyRing 保存当前发现密钥以外的发现密钥（JSON）：移除设备时生成、尚未启用的新密钥，
	// 以及轮换下来、仍在宽限期内接受的旧密钥（设计方案 3.5）。
	DiscoveryKeyRing = "discovery-key-ring"
	// OldDiscoveryKey 是合并设备圈后来源圈的发现密钥，用来在迁移等待期里找还没跟过来的旧设备。
	// 密钥库没有删除操作，"删除"就是写入空值；长度不是 32 字节的一律当作没有。
	OldDiscoveryKey = "old-discovery-key"
)

// ErrNotFound 表示该项尚未保存。
var ErrNotFound = errors.New("keystore: not found")

// ErrWrongPassphrase 表示文件密钥库的口令不对（或文件被改过）。
var ErrWrongPassphrase = errors.New("keystore: wrong passphrase or tampered file")

// Store 是秘密存储的最小接口。
type Store interface {
	Get(name string) ([]byte, error)
	Set(name string, value []byte) error
}

// Memory 是只存在于内存中的 Store，供测试使用。测试里守护进程和测试本身会同时读写它，所以加锁。
type Memory map[string][]byte

// memoryMu 保护所有 Memory 的读写（测试用，一把全局锁就够了）。
var memoryMu sync.Mutex

func (m Memory) Get(name string) ([]byte, error) {
	memoryMu.Lock()
	defer memoryMu.Unlock()
	v, ok := m[name]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (m Memory) Set(name string, value []byte) error {
	memoryMu.Lock()
	defer memoryMu.Unlock()
	m[name] = append([]byte(nil), value...)
	return nil
}

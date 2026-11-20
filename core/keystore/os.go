package keystore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"

	"github.com/zalando/go-keyring"
)

// OS 使用操作系统密钥库：Windows 凭据管理器、macOS 钥匙串、Linux Secret Service。
type OS struct {
	service string
}

// NewOS 返回绑定到 dataDir 的系统密钥库。服务名包含 dataDir 的哈希，
// 这样同一台机器上的多个数据目录（例如测试时）互不覆盖。
func NewOS(dataDir string) *OS {
	sum := sha256.Sum256([]byte(dataDir))
	return &OS{service: "portide-" + hex.EncodeToString(sum[:6])}
}

func (o *OS) Get(name string) ([]byte, error) {
	v, err := keyring.Get(o.service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(v)
}

func (o *OS) Set(name string, value []byte) error {
	return keyring.Set(o.service, name, base64.StdEncoding.EncodeToString(value))
}

// Available 通过写入再读取一个探测项，判断系统密钥库是否可用（无图形桌面的 Linux 上通常不可用）。
func (o *OS) Available() bool {
	const probe = "probe"
	if err := keyring.Set(o.service, probe, "ok"); err != nil {
		return false
	}
	v, err := keyring.Get(o.service, probe)
	_ = keyring.Delete(o.service, probe)
	return err == nil && v == "ok"
}

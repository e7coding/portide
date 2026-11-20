// Package mdnsprobe 验证匿名轮换令牌 + mDNS 发布/浏览，以及 Go 实现与 iOS Bonjour 能否互相发现。
// M0 一次性验证代码。
package mdnsprobe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"
)

const (
	ServiceType   = "_portide._udp"
	WindowSeconds = 600 // 令牌每 10 分钟轮换一次
)

// Window 返回 t 所在的时间窗口序号。
func Window(t time.Time) int64 { return t.Unix() / WindowSeconds }

// Token = 截断的 HMAC-SHA256(发现密钥, 窗口序号 ‖ 设备 ID)，24 个十六进制字符，用作 mDNS 服务实例名。
func Token(key []byte, window int64, deviceID string) string {
	m := hmac.New(sha256.New, key)
	var w [8]byte
	binary.BigEndian.PutUint64(w[:], uint64(window))
	m.Write(w[:])
	m.Write([]byte(deviceID))
	return hex.EncodeToString(m.Sum(nil)[:12])
}

// Match 返回 name 对应的成员设备 ID；为容忍时钟偏差，接受前后各一个时间窗口。
func Match(key []byte, deviceIDs []string, name string, now time.Time) (string, bool) {
	w := Window(now)
	for _, id := range deviceIDs {
		for d := int64(-1); d <= 1; d++ {
			if hmac.Equal([]byte(Token(key, w+d, id)), []byte(name)) {
				return id, true
			}
		}
	}
	return "", false
}

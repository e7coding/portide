// Package discovery 实现局域网内的匿名设备发现（设计方案 4.1）。
// 成员设备以轮换的不透明令牌作为 mDNS 实例名和主机名发布服务，外人看不出是谁、也无法长期追踪。
package discovery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"
)

const (
	// MemberService 是成员设备发布的服务类型。
	MemberService = "_portide._udp"
	// PairService 是邀请进行中时发布的服务类型，实例名为随机串。
	PairService = "_portide-pair._udp"
	// Window 是令牌轮换周期。
	Window = 10 * time.Minute
)

// windowOf 返回 t 所在的时间窗口序号。
func windowOf(t time.Time) int64 { return t.Unix() / int64(Window/time.Second) }

// token = 截断的 HMAC-SHA256(发现密钥, 窗口序号 ‖ KeyID)，24 个小写十六进制字符。
func token(key []byte, window int64, keyID string) string {
	m := hmac.New(sha256.New, key)
	var w [8]byte
	binary.BigEndian.PutUint64(w[:], uint64(window))
	m.Write(w[:])
	m.Write([]byte(keyID))
	return hex.EncodeToString(m.Sum(nil)[:12])
}

// MemberName 返回设备 keyID 在时刻 now 应发布的实例名。
func MemberName(key []byte, keyID string, now time.Time) string {
	return token(key, windowOf(now), keyID)
}

// Identify 判断实例名属于哪个成员；为容忍时钟偏差，接受前后各一个时间窗口。
func Identify(key []byte, keyIDs []string, name string, now time.Time) (string, bool) {
	w := windowOf(now)
	for _, id := range keyIDs {
		for d := int64(-1); d <= 1; d++ {
			if hmac.Equal([]byte(token(key, w+d, id)), []byte(name)) {
				return id, true
			}
		}
	}
	return "", false
}

// nextRotation 返回 now 之后下一次轮换的时刻。
func nextRotation(now time.Time) time.Time {
	return time.Unix((windowOf(now)+1)*int64(Window/time.Second), 0)
}

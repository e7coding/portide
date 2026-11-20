// Package relayset 实现设备圈共享的中继列表（中继设置同步与多中继设计 3）：
// 列表由最后修改它的设备签名，接收方核对签名者是当前成员，再按版本号决定采用哪一份。
//
// 本包只做纯逻辑（签名、校验、比较、合并），不碰数据库和网络。
package relayset

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"portide/core/pb"
	"portide/core/relay"
)

// MaxRelays 是一个设备圈最多配置的中继数。
const MaxRelays = 4

// MaxVersion 是版本号的上限（不含）：Sign 和 Parse 拒绝 >= 它的版本号，合并时的 max+1 就不会溢出。
const MaxVersion = uint64(1) << 63

// signContext 是签名的域分隔前缀。
const signContext = "portide-relay-settings-v1\x00"

var (
	// ErrFull 表示中继数量超过 MaxRelays。
	ErrFull = errors.New("relayset: too many relays")
	// ErrDuplicate 表示列表里有指纹相同的中继。
	ErrDuplicate = errors.New("relayset: duplicate relay fingerprint")
	// ErrBadSignature 表示签名校验失败。
	ErrBadSignature = errors.New("relayset: bad signature")
	// ErrNotMember 表示签名者不是设备圈的当前成员。
	ErrNotMember = errors.New("relayset: signer is not a member")
	// ErrInvalid 表示格式错误或配置串无法解析。
	ErrInvalid = errors.New("relayset: invalid settings")
)

// Settings 是一份中继设置。
type Settings struct {
	Version uint64
	Configs []relay.Config
	Time    time.Time
	// Signer 和 Raw 在本地构造、还没签名的设置里为空（版本 0 的升级配置）。
	Signer ed25519.PublicKey
	// Raw 是签名后的 pb.RelaySettings 序列化字节，原样存盘、原样转发。
	Raw []byte
}

// check 检查数量上限和指纹重复。
func check(configs []relay.Config) error {
	if len(configs) > MaxRelays {
		return ErrFull
	}
	seen := make(map[[32]byte]bool, len(configs))
	for _, c := range configs {
		if seen[c.Fingerprint] {
			return ErrDuplicate
		}
		seen[c.Fingerprint] = true
	}
	return nil
}

// Sign 用 priv 对一份设置签名。超过 MaxRelays 个返回 ErrFull，指纹重复返回 ErrDuplicate，
// 版本号 >= MaxVersion 返回 ErrInvalid。
func Sign(priv ed25519.PrivateKey, version uint64, configs []relay.Config, now time.Time) (*Settings, error) {
	if version >= MaxVersion {
		return nil, ErrInvalid
	}
	if err := check(configs); err != nil {
		return nil, err
	}
	strs := make([]string, len(configs))
	for i, c := range configs {
		strs[i] = c.ConfigString()
	}
	body, err := proto.Marshal(&pb.RelaySettingsBody{Version: version, Configs: strs, TimeUnix: now.Unix()})
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	raw, err := proto.Marshal(&pb.RelaySettings{
		Body:      body,
		Signer:    pub,
		Signature: ed25519.Sign(priv, append([]byte(signContext), body...)),
	})
	if err != nil {
		return nil, err
	}
	return &Settings{
		Version: version,
		Configs: append([]relay.Config(nil), configs...),
		Time:    time.Unix(now.Unix(), 0),
		Signer:  pub,
		Raw:     raw,
	}, nil
}

// Parse 解析并校验收到的设置。顺序：格式、签名、成员、版本号和配置串、数量和重复。
func Parse(raw []byte, isMember func(ed25519.PublicKey) bool) (*Settings, error) {
	var env pb.RelaySettings
	if err := proto.Unmarshal(raw, &env); err != nil || len(env.Signer) != ed25519.PublicKeySize {
		return nil, ErrInvalid
	}
	signer := ed25519.PublicKey(env.Signer)
	if !ed25519.Verify(signer, append([]byte(signContext), env.Body...), env.Signature) {
		return nil, ErrBadSignature
	}
	if !isMember(signer) {
		return nil, ErrNotMember
	}
	var body pb.RelaySettingsBody
	if err := proto.Unmarshal(env.Body, &body); err != nil || body.Version >= MaxVersion {
		return nil, ErrInvalid
	}
	configs := make([]relay.Config, 0, len(body.Configs))
	for _, s := range body.Configs {
		c, err := relay.ParseConfig(s)
		if err != nil {
			return nil, ErrInvalid
		}
		configs = append(configs, c)
	}
	if err := check(configs); err != nil {
		return nil, err
	}
	return &Settings{
		Version: body.Version,
		Configs: configs,
		Time:    time.Unix(body.TimeUnix, 0),
		Signer:  signer,
		Raw:     append([]byte(nil), raw...),
	}, nil
}

// bodyOf 返回用于比较的 body：已签名的取签名里的 body，未签名的取配置串的拼接。
func bodyOf(s *Settings) []byte {
	if len(s.Raw) > 0 {
		var env pb.RelaySettings
		if proto.Unmarshal(s.Raw, &env) == nil {
			return env.Body
		}
	}
	var b []byte
	for _, c := range s.Configs {
		b = append(b, c.ConfigString()...)
	}
	return b
}

// Preferred 在 b 按设计 3.2 胜过 a 时返回 true：版本大的胜；同版本时签名者公钥字节序大的胜；
// 签名者也相同时 body 的 SHA-256 大的胜。签名者和 body 都相同时返回 false。
// 签名者不同时一定分出胜负，哪怕 body 相同（两台设备在同一秒签了同样的内容）。
func Preferred(a, b *Settings) bool {
	if a.Version != b.Version {
		return b.Version > a.Version
	}
	if c := bytes.Compare(a.Signer, b.Signer); c != 0 {
		return c < 0
	}
	ab, bb := bodyOf(a), bodyOf(b)
	if bytes.Equal(ab, bb) {
		return false
	}
	ah, bh := sha256.Sum256(ab), sha256.Sum256(bb)
	return bytes.Compare(ah[:], bh[:]) < 0
}

// Action 是 Combine 的结论。
type Action int

const (
	// Keep 保留本机的设置。
	Keep Action = iota
	// Adopt 采用收到的设置。
	Adopt
	// Resign 用返回的配置和版本号由本机重新签名。
	Resign
)

func sameConfigs(a, b []relay.Config) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ConfigString() != b[i].ConfigString() {
			return false
		}
	}
	return true
}

// Union 返回 first 和 second 的并集：first 的在前，按指纹去重，截到 MaxRelays 个。不修改传入的切片。
func Union(first, second []relay.Config) []relay.Config {
	var out []relay.Config
	seen := make(map[[32]byte]bool)
	for _, list := range [][]relay.Config{first, second} {
		for _, c := range list {
			if seen[c.Fingerprint] || len(out) >= MaxRelays {
				continue
			}
			seen[c.Fingerprint] = true
			out = append(out, c)
		}
	}
	return out
}

// Combine 决定收到 in 之后本机 local 该怎么办（设计 3.2、3.3）。local 为 nil 时采用 in。
// 两边版本都 >0 时按 Preferred 二选一。任一方版本为 0 时取并集（Union，Preferred 的胜者在前）：
// 并集等于 in 的列表且 in 的版本 >0 时采用，等于 local 的列表且 local 的版本 >0 时保留，
// 否则由本机以 max(两边版本) + 1 重新签名——版本 0 的设置从不被原样采用或保留，
// 两台都是版本 0 的设备第一次接触后都离开版本 0，之后的删除才能按版本号生效。
// 只有 Resign 时返回的 configs 和 version 才有意义。
func Combine(local, in *Settings) (act Action, configs []relay.Config, version uint64) {
	if local == nil {
		return Adopt, nil, 0
	}
	inWins := Preferred(local, in)
	if local.Version != 0 && in.Version != 0 {
		if inWins {
			return Adopt, nil, 0
		}
		return Keep, nil, 0
	}

	if inWins {
		configs = Union(in.Configs, local.Configs)
	} else {
		configs = Union(local.Configs, in.Configs)
	}
	switch {
	case in.Version > 0 && sameConfigs(configs, in.Configs):
		return Adopt, nil, 0
	case local.Version > 0 && sameConfigs(configs, local.Configs):
		return Keep, nil, 0
	}
	return Resign, configs, max(local.Version, in.Version) + 1
}

// Upsert 按指纹新增或更新一个中继：指纹已有就原地替换（updated 为 true），否则追加；已满返回 ErrFull。
// 不修改传入的切片。
func Upsert(configs []relay.Config, c relay.Config) (out []relay.Config, updated bool, err error) {
	out = append([]relay.Config(nil), configs...)
	for i := range out {
		if out[i].Fingerprint == c.Fingerprint {
			out[i] = c
			return out, true, nil
		}
	}
	if len(out) >= MaxRelays {
		return nil, false, ErrFull
	}
	return append(out, c), false, nil
}

// Remove 按指纹删除一个中继；没有这个指纹时第二个返回值为 false。不修改传入的切片。
func Remove(configs []relay.Config, fp [32]byte) ([]relay.Config, bool) {
	for i := range configs {
		if configs[i].Fingerprint == fp {
			out := append([]relay.Config(nil), configs[:i]...)
			return append(out, configs[i+1:]...), true
		}
	}
	return configs, false
}

// Package memberauth 实现匿名成员握手（设计方案 4.2"已知限制"的修复，方案 B）。
//
// 双方的 TLS 证书都是一次性的，不含设备身份。握手在加密通道里一来一回地进行：
//
//  1. 圈子证明：双方各用设备圈密钥（由发现密钥派生）对 TLS 导出值算 MAC，只证明"我在这个圈子里"。
//     发起方先发；接收方验证通过后才回自己的证明，所以主动连进来的外人什么也拿不到。
//  2. 身份：两边都通过第 1 步之后，发起方先发 Hello（设备公钥、对导出值的签名、签名链），
//     接收方验证后再回自己的 Hello。
//
// MAC 和签名都绑定本条 TLS 连接的导出值，并区分角色，无法重放到别的连接，也无法反射回去。
// 成员资格（对方公钥是否在签名链里）由调用方通过 Config.Admit 在合并对方的链之后判断，
// 接收方在 Admit 通过之前不会出示自己的身份。
package memberauth

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"portide/core/identity"
	"portide/core/pb"
	"portide/core/wire"
)

const (
	// ExporterLabel 是成员握手使用的 TLS 导出标签。
	ExporterLabel = "portide-member-auth-v2"
	// ExporterLen 是导出值的字节数。
	ExporterLen = 32
	// ProtocolVersion 是 Hello 中携带的协议版本。
	ProtocolVersion = 2
	// CircleKeyLen 是发现密钥的字节数。
	CircleKeyLen = 32

	roleDialer   = "dialer"
	roleListener = "listener"
	gateContext  = "portide-member-gate-v2\x00"
	signContext  = "portide-member-identity-v2\x00"

	maxGateSize = 128 // 真实的圈子证明约 34 字节
)

var (
	// ErrNotInCircle 表示对方没能证明自己在这个设备圈里。
	ErrNotInCircle = errors.New("memberauth: peer did not prove membership of this circle")
	// ErrBadIdentity 表示对方的身份证明无效。
	ErrBadIdentity = errors.New("memberauth: peer identity proof is invalid")
)

// Config 是一次握手中本机的参数。
type Config struct {
	Self  *identity.Identity
	Name  string
	Chain [][]byte // 本机当前的签名链
	// CircleKeys 是可接受的发现密钥，第一项是当前密钥。作为发起方时只用当前密钥；
	// 作为接收方时接受其中任意一个，用来在发现密钥轮换期间兼容还持有旧密钥的成员（设计方案 3.5）。
	CircleKeys [][]byte
	Dialer     bool // 本机是否是发起连接的一方
	// Features 是本机支持的可选功能（M3c，例如 "rename"），随 Hello 告诉对方；旧版本不发。
	Features []string
	// Admit 在对方身份校验通过之后、本机出示身份（接收方）或结束握手（发起方）之前调用：
	// 由调用方合并对方的签名链并判断对方是否为成员，返回错误则握手失败，本机不再发出任何内容。
	// 这样即使对方持有轮换前的发现密钥、能通过圈子证明，只要已不是成员，也拿不到本机的身份和签名链。必填。
	Admit func(*Peer) error
	// ChainFor 可选，只对接收方有效：Admit 通过之后、出示 Hello 之前，按对方通过圈子证明用的那把密钥
	// 选要出示的签名链（合并设备圈设计 8.3：用来源圈的密钥连进来的只能看到来源圈的链）。为 nil 时用 Chain。
	ChainFor func(key []byte) [][]byte
}

// Peer 是握手得到的对方身份。对方的签名链尚未与本机合并，成员资格由调用方判断。
type Peer struct {
	Key      ed25519.PublicKey
	KeyID    string
	Name     string
	Chain    [][]byte
	Features []string // 对方声明的可选功能；旧版本为空
	GateKey  []byte   // 对方通过圈子证明用的发现密钥（发起方就是 CircleKeys[0]）
}

// Exchange 在 rw（成员连接上的第一条流）上执行握手。exporter 必须是本条 TLS 连接以
// ExporterLabel 导出的 ExporterLen 字节。
func Exchange(rw io.ReadWriter, exporter []byte, cfg Config) (*Peer, error) {
	if len(exporter) != ExporterLen {
		return nil, fmt.Errorf("memberauth: exporter must be %d bytes", ExporterLen)
	}
	if len(cfg.CircleKeys) == 0 {
		return nil, errors.New("memberauth: no circle key")
	}
	// 空或长度不对的密钥会派生出任何人都能算出的认证密钥，必须拒绝。
	for _, k := range cfg.CircleKeys {
		if len(k) != CircleKeyLen {
			return nil, fmt.Errorf("memberauth: circle keys must be %d bytes", CircleKeyLen)
		}
	}
	if cfg.Admit == nil {
		return nil, errors.New("memberauth: Admit is required")
	}
	me, them := roleListener, roleDialer
	if cfg.Dialer {
		me, them = roleDialer, roleListener
	}
	myHello := &pb.Hello{
		Version:    ProtocolVersion,
		DeviceName: cfg.Name,
		Chain:      cfg.Chain,
		DeviceKey:  cfg.Self.Public,
		Signature:  ed25519.Sign(cfg.Self.Private, identityMessage(me, exporter)),
		Features:   cfg.Features,
	}

	// 第 1 步：圈子证明。发起方用自己的当前密钥；接收方用刚才验证通过对方的那个密钥回应，
	// 这样发现密钥轮换期间只持有旧密钥的成员也能完成握手（新密钥随后在加密通道里下发）。
	var gateKey []byte
	if cfg.Dialer {
		gateKey = cfg.CircleKeys[0]
		mac, err := gateMAC(cfg.CircleKeys[0], me, exporter)
		if err != nil {
			return nil, err
		}
		if err := wire.Write(rw, &pb.MemberGate{Mac: mac}); err != nil {
			return nil, err
		}
		if _, err := readGate(rw, cfg.CircleKeys[:1], them, exporter); err != nil {
			return nil, err
		}
	} else {
		key, err := readGate(rw, cfg.CircleKeys, them, exporter)
		if err != nil {
			return nil, err
		}
		gateKey = key
		mac, err := gateMAC(key, me, exporter)
		if err != nil {
			return nil, err
		}
		if err := wire.Write(rw, &pb.MemberGate{Mac: mac}); err != nil {
			return nil, err
		}
	}

	// 第 2 步：身份。
	var peer *Peer
	var err error
	if cfg.Dialer {
		if err := wire.Write(rw, myHello); err != nil {
			return nil, err
		}
		if peer, err = readHello(rw, them, exporter); err != nil {
			return nil, err
		}
		peer.GateKey = gateKey
		if err := cfg.Admit(peer); err != nil {
			return nil, err
		}
	} else {
		if peer, err = readHello(rw, them, exporter); err != nil {
			return nil, err
		}
		peer.GateKey = gateKey
		if err := cfg.Admit(peer); err != nil {
			return nil, err // 对方不是成员：不出示本机身份
		}
		if cfg.ChainFor != nil {
			myHello.Chain = cfg.ChainFor(gateKey)
		}
		if err := wire.Write(rw, myHello); err != nil {
			return nil, err
		}
	}
	return peer, nil
}

// circleAuthKey 由发现密钥派生圈子认证密钥，避免同一个密钥同时用于 mDNS 令牌和握手。
func circleAuthKey(discoveryKey []byte) ([]byte, error) {
	return hkdf.Expand(sha256.New, discoveryKey, "portide-circle-auth-v1", 32)
}

func gateMAC(discoveryKey []byte, role string, exporter []byte) ([]byte, error) {
	k, err := circleAuthKey(discoveryKey)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte(gateContext + role + "\x00"))
	m.Write(exporter)
	return m.Sum(nil), nil
}

// readGate 读取并验证对方的圈子证明，返回验证通过的那个发现密钥。
func readGate(r io.Reader, keys [][]byte, role string, exporter []byte) ([]byte, error) {
	var g pb.MemberGate
	if err := wire.ReadMax(r, &g, maxGateSize); err != nil {
		return nil, fmt.Errorf("memberauth: read gate: %w", err)
	}
	for _, k := range keys {
		want, err := gateMAC(k, role, exporter)
		if err != nil {
			return nil, err
		}
		if hmac.Equal(g.Mac, want) {
			return k, nil
		}
	}
	return nil, ErrNotInCircle
}

func identityMessage(role string, exporter []byte) []byte {
	return append([]byte(signContext+role+"\x00"), exporter...)
}

func readHello(r io.Reader, role string, exporter []byte) (*Peer, error) {
	var h pb.Hello
	if err := wire.Read(r, &h); err != nil {
		return nil, fmt.Errorf("memberauth: read hello: %w", err)
	}
	return verifyHello(&h, role, exporter)
}

// verifyHello 校验对方 Hello 的协议版本和身份签名。
func verifyHello(h *pb.Hello, role string, exporter []byte) (*Peer, error) {
	if h.Version != ProtocolVersion {
		return nil, fmt.Errorf("memberauth: peer speaks protocol version %d, we speak %d", h.Version, ProtocolVersion)
	}
	if len(h.DeviceKey) != ed25519.PublicKeySize {
		return nil, ErrBadIdentity
	}
	key := ed25519.PublicKey(h.DeviceKey)
	if !ed25519.Verify(key, identityMessage(role, exporter), h.Signature) {
		return nil, ErrBadIdentity
	}
	return &Peer{Key: key, KeyID: identity.KeyIDOf(key), Name: h.DeviceName, Chain: h.Chain, Features: h.Features}, nil
}

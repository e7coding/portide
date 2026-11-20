package relayset

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"portide/core/pb"
	"portide/core/relay"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// cfg 构造一个中继配置：fp 的每个字节都填 n，令牌固定长度。
func cfg(n byte) relay.Config {
	c := relay.Config{Addr: "127.0.0.1:7443", Token: bytes.Repeat([]byte{n}, relay.TokenSize)}
	for i := range c.Fingerprint {
		c.Fingerprint[i] = n
	}
	return c
}

func strs(cs []relay.Config) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ConfigString())
	}
	return out
}

func sameList(a, b []relay.Config) bool {
	x, y := strs(a), strs(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func TestSignParseRoundTrip(t *testing.T) {
	pub, priv := newKey(t)
	now := time.Unix(1700000000, 0)
	s, err := Sign(priv, 3, []relay.Config{cfg(1), cfg(2)}, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(s.Raw, func(k ed25519.PublicKey) bool { return k.Equal(pub) })
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 3 || !sameList(got.Configs, s.Configs) || !got.Signer.Equal(pub) || !got.Time.Equal(now) {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestParseRejects(t *testing.T) {
	pub, priv := newKey(t)
	member := func(k ed25519.PublicKey) bool { return k.Equal(pub) }
	s, _ := Sign(priv, 1, []relay.Config{cfg(1)}, time.Now())

	// 篡改 body
	var env pb.RelaySettings
	if err := proto.Unmarshal(s.Raw, &env); err != nil {
		t.Fatal(err)
	}
	env.Body[len(env.Body)-1] ^= 1
	raw, _ := proto.Marshal(&env)
	if _, err := Parse(raw, member); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered: %v", err)
	}

	// 不是成员
	if _, err := Parse(s.Raw, func(ed25519.PublicKey) bool { return false }); !errors.Is(err, ErrNotMember) {
		t.Fatalf("not member: %v", err)
	}

	// 格式错误
	if _, err := Parse([]byte{0xff, 0xff}, member); !errors.Is(err, ErrInvalid) {
		t.Fatalf("garbage: %v", err)
	}

	// 手工签名：绕过 Sign 的检查
	forge := func(configs []string) []byte {
		body, _ := proto.Marshal(&pb.RelaySettingsBody{Version: 1, Configs: configs})
		sig := ed25519.Sign(priv, append([]byte(signContext), body...))
		raw, _ := proto.Marshal(&pb.RelaySettings{Body: body, Signer: pub, Signature: sig})
		return raw
	}
	five := []string{cfg(1).ConfigString(), cfg(2).ConfigString(), cfg(3).ConfigString(), cfg(4).ConfigString(), cfg(5).ConfigString()}
	if _, err := Parse(forge(five), member); !errors.Is(err, ErrFull) {
		t.Fatalf("five: %v", err)
	}
	if _, err := Parse(forge([]string{"not a config"}), member); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad config: %v", err)
	}
	if _, err := Parse(forge([]string{cfg(1).ConfigString(), cfg(1).ConfigString()}), member); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("dup: %v", err)
	}
	if _, err := Sign(priv, 1, []relay.Config{cfg(1), cfg(1)}, time.Now()); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("sign dup: %v", err)
	}
	if _, err := Sign(priv, 1, []relay.Config{cfg(1), cfg(2), cfg(3), cfg(4), cfg(5)}, time.Now()); !errors.Is(err, ErrFull) {
		t.Fatalf("sign full: %v", err)
	}

	// 版本号不能到 1<<63：防止 max+1 溢出
	const tooBig = uint64(1) << 63
	if _, err := Sign(priv, tooBig, nil, time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sign huge version: %v", err)
	}
	if _, err := Sign(priv, tooBig-1, nil, time.Now()); err != nil {
		t.Fatalf("sign largest version: %v", err)
	}
	body, _ := proto.Marshal(&pb.RelaySettingsBody{Version: tooBig})
	sig := ed25519.Sign(priv, append([]byte(signContext), body...))
	raw, _ = proto.Marshal(&pb.RelaySettings{Body: body, Signer: pub, Signature: sig})
	if _, err := Parse(raw, member); !errors.Is(err, ErrInvalid) {
		t.Fatalf("parse huge version: %v", err)
	}
}

func TestPreferred(t *testing.T) {
	_, p1 := newKey(t)
	_, p2 := newKey(t)
	now := time.Unix(1700000000, 0)
	a, _ := Sign(p1, 1, []relay.Config{cfg(1)}, now)
	b, _ := Sign(p1, 2, []relay.Config{cfg(1)}, now)
	if !Preferred(a, b) || Preferred(b, a) {
		t.Fatal("higher version should win")
	}

	// 同版本不同 body：签名者公钥字节序大的胜
	x, _ := Sign(p1, 5, []relay.Config{cfg(1)}, now)
	y, _ := Sign(p2, 5, []relay.Config{cfg(2)}, now)
	if bytes.Compare(x.Signer, y.Signer) < 0 {
		if !Preferred(x, y) || Preferred(y, x) {
			t.Fatal("larger signer should win")
		}
	} else if !Preferred(y, x) || Preferred(x, y) {
		t.Fatal("larger signer should win")
	}

	// 同版本、同签名者、不同 body：按 body 的 SHA-256 定胜负，恰好一个方向为 true
	m, _ := Sign(p1, 7, []relay.Config{cfg(1)}, now)
	n, _ := Sign(p1, 7, []relay.Config{cfg(2)}, now)
	mh, nh := sha256.Sum256(bodyOf(m)), sha256.Sum256(bodyOf(n))
	nWins := bytes.Compare(mh[:], nh[:]) < 0
	if Preferred(m, n) != nWins || Preferred(n, m) == nWins {
		t.Fatalf("sha256 tie-break: Preferred(m,n)=%v Preferred(n,m)=%v, want n wins=%v", Preferred(m, n), Preferred(n, m), nWins)
	}

	// 同版本、同 body（同一秒签的同样内容）、不同签名者：仍按签名者分出胜负，否则两边各自保留、永远不收敛
	sa, _ := Sign(p1, 9, []relay.Config{cfg(1)}, now)
	sb, _ := Sign(p2, 9, []relay.Config{cfg(1)}, now)
	if Preferred(sa, sb) == Preferred(sb, sa) {
		t.Fatal("same body, different signers: exactly one must win")
	}
	if bytes.Compare(sa.Signer, sb.Signer) < 0 != Preferred(sa, sb) {
		t.Fatal("same body, different signers: larger signer should win")
	}

	// 同版本同 body
	z, _ := Sign(p1, 5, []relay.Config{cfg(1)}, now)
	if Preferred(x, z) || Preferred(z, x) {
		t.Fatal("identical settings should not be preferred")
	}
}

func TestCombine(t *testing.T) {
	_, priv := newKey(t)
	now := time.Unix(1700000000, 0)
	r1, r2 := cfg(1), cfg(2)
	signed := func(v uint64, cs ...relay.Config) *Settings {
		s, err := Sign(priv, v, cs, now)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	unsigned := func(cs ...relay.Config) *Settings { return &Settings{Configs: cs} }

	if act, _, _ := Combine(nil, signed(1, r1)); act != Adopt {
		t.Fatalf("nil local: %v", act)
	}
	if act, _, _ := Combine(signed(3, r1), signed(5, r2)); act != Adopt {
		t.Fatalf("v3 vs v5: %v", act)
	}
	if act, _, _ := Combine(signed(5, r1), signed(3, r2)); act != Keep {
		t.Fatalf("v5 vs v3: %v", act)
	}
	act, cs, v := Combine(unsigned(r1), signed(2, r2))
	if act != Resign || !sameList(cs, []relay.Config{r2, r1}) || v != 3 {
		t.Fatalf("v0 vs v2: %v %v %d", act, cs, v)
	}
	if act, _, _ := Combine(signed(2, r1, r2), unsigned(r1)); act != Keep {
		t.Fatalf("v2 vs v0 superset: %v", act)
	}
	// 两份版本 0、内容相同：谁也不能保持版本 0，本机签成版本 1（之后按 Preferred 收敛）
	act, cs, v = Combine(unsigned(r1), unsigned(r1))
	if act != Resign || !sameList(cs, []relay.Config{r1}) || v != 1 {
		t.Fatalf("v0 vs v0 same: %v %v %d", act, cs, v)
	}
	act, cs, v = Combine(signed(0, r1), signed(0, r1))
	if act != Resign || !sameList(cs, []relay.Config{r1}) || v != 1 {
		t.Fatalf("signed v0 vs v0 same: %v %v %d", act, cs, v)
	}
	// 本机是版本 0，对方的版本 >0 且内容相同：采用对方
	if act, _, _ := Combine(unsigned(r1), signed(3, r1)); act != Adopt {
		t.Fatalf("v0 vs v3 same: %v", act)
	}
	// 本机版本 >0、内容是并集，对方是版本 0：保留本机
	if act, _, _ := Combine(signed(3, r1), unsigned(r1)); act != Keep {
		t.Fatalf("v3 vs v0 same: %v", act)
	}
	// 对方是版本 0、并集等于对方的列表：不能采用版本 0，签成 max+1
	act, cs, v = Combine(signed(2), signed(0, r1))
	if act != Resign || !sameList(cs, []relay.Config{r1}) || v != 3 {
		t.Fatalf("v2 empty vs v0 [r1]: %v %v %d", act, cs, v)
	}
	// 本机是版本 0、并集等于本机的列表：同样签成 max+1，不能停在版本 0
	act, cs, v = Combine(signed(0, r1), signed(2))
	if act != Resign || !sameList(cs, []relay.Config{r1}) || v != 3 {
		t.Fatalf("v0 [r1] vs v2 empty: %v %v %d", act, cs, v)
	}
	// 本机版本 0 是对方的超集
	act, cs, v = Combine(unsigned(r1, r2), unsigned(r1))
	if act != Resign || len(cs) != 2 || v != 1 {
		t.Fatalf("v0 superset vs v0: %v %v %d", act, cs, v)
	}

	// 并集超过 4 个：截到 4
	act, cs, v = Combine(unsigned(cfg(1), cfg(2), cfg(3)), signed(4, cfg(4), cfg(5), cfg(6)))
	if act != Resign || len(cs) != MaxRelays || v != 5 {
		t.Fatalf("overflow: %v %d %d", act, len(cs), v)
	}
	if cs[0].Fingerprint != cfg(4).Fingerprint {
		t.Fatal("winner should come first")
	}
}

func TestUpsertRemove(t *testing.T) {
	cs := []relay.Config{cfg(1), cfg(2)}
	changed := cfg(1)
	changed.Token = bytes.Repeat([]byte{9}, relay.TokenSize)
	out, updated, err := Upsert(cs, changed)
	if err != nil || !updated || len(out) != 2 || out[0].ConfigString() != changed.ConfigString() {
		t.Fatalf("replace: %v %v %v", out, updated, err)
	}
	if cs[0].ConfigString() == changed.ConfigString() {
		t.Fatal("input slice must not be modified")
	}
	out, updated, err = Upsert(out, cfg(3))
	if err != nil || updated || len(out) != 3 {
		t.Fatalf("append: %v %v %v", out, updated, err)
	}
	out, _, _ = Upsert(out, cfg(4))
	if _, _, err := Upsert(out, cfg(5)); !errors.Is(err, ErrFull) {
		t.Fatalf("fifth: %v", err)
	}

	left, ok := Remove(out, cfg(2).Fingerprint)
	if !ok || len(left) != 3 || len(out) != 4 {
		t.Fatalf("remove: %v %v", left, ok)
	}
	if _, ok := Remove(out, cfg(7).Fingerprint); ok {
		t.Fatal("removing unknown fingerprint should be false")
	}
}

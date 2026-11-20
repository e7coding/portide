package pairing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"slices"

	"filippo.io/cpace"

	"portide/core/chain"
	"portide/core/identity"
	"portide/core/pb"
	"portide/core/wire"
)

// ErrWrongCode 表示双方的配对码不一致（或者中间有人冒充）。
var ErrWrongCode = errors.New("pairing: wrong code")

// ErrJoinerOutdated 表示加入方的版本太旧，读不了含改名记录的链（邀请方已拒绝，没有把它加进设备圈）。
var ErrJoinerOutdated = errors.New("pairing: joiner does not support renamed devices")

const (
	// ExporterLabel 是配对使用的 TLS 导出标签。
	ExporterLabel = "portide-pair-v2"
	// ExporterLen 是导出值的字节数。
	ExporterLen = 32

	associatedData = "portide-pair-v2\x00"
	roleInitiator  = "initiator" // 加入方
	roleResponder  = "responder" // 邀请方
	joinerContext  = "portide-pair-joiner-v2\x00"
	inviterContext = "portide-pair-inviter-v2\x00"
	adopterContext = "portide-pair-adopter-v2\x00" // 反向加入时加入方发的欢迎消息
	wrongCodeMsg   = "wrong code"
	outdatedMsg    = "please update portide on this device first"
	internalMsg    = "internal error"

	sameCircleMsg       = "same circle"
	mergeUnsupportedMsg = "merge unsupported"
	mergeUnconfirmedMsg = "merge not confirmed"

	maxPreAuthSize = 1024 // 配对码确认之前的消息都很小（设备名最多 96 字节）
)

// FeatureCircle2 是在配对里声明"支持加入、反向加入、合并三种结果"的功能名（M5a）。
// 双方都声明了才走新流程（PairConfirm 带 circle、邀请方回 PairDecision）；否则走原来的加入流程。
const FeatureCircle2 = "circle2"

// FeatureMerge 是在配对和成员握手里声明"支持合并设备圈"的功能名（M5b）。
// 双方都声明了才会合并；M5a 已发布的设备声明了 circle2，却不认识合并和 CircleMove。
const FeatureMerge = "merge"

// Mode 是一次配对的结果（设计方案 3）。
type Mode string

const (
	ModeJoin  Mode = "join"  // 加入方加入邀请方的圈
	ModeAdopt Mode = "adopt" // 反向加入：单机的邀请方加入加入方的圈
	ModeMerge Mode = "merge" // 两边都有多台设备：合并（M5b）
)

var (
	// ErrSameCircle 表示双方已经在同一个设备圈里。
	ErrSameCircle = errors.New("pairing: both devices are already in the same circle")
	// ErrInviterOutdated 表示本机圈里有其他设备、而邀请方的版本太旧，不支持反向加入或合并（什么都没提交）。
	ErrInviterOutdated = errors.New("pairing: the inviting device must update portide first")
	// ErrMergeUnsupported 表示两边都有多台设备，但对方的版本不支持合并（对方没有声明 FeatureMerge；什么都没提交）。
	ErrMergeUnsupported = errors.New("pairing: the other device does not support merging circles")
	// ErrMergeNotConfirmed 表示两边都有多台设备，但用户没有同意合并。
	ErrMergeNotConfirmed = errors.New("pairing: merging two circles was not confirmed")
	// ErrUnexpectedJoin 表示本机圈里有其他设备，邀请方却要本机离开自己的圈、加入它的圈（已拒绝，本机什么都没改）。
	ErrUnexpectedJoin = errors.New("pairing: inviter asked this device to leave its circle")
	// ErrStaleCircle 表示同一个设备圈，但邀请方给的链不以本机的链为前缀（比本机旧或分叉了）：已拒绝，本机什么都没改。
	ErrStaleCircle = errors.New("pairing: the inviter's copy of this circle is older than this device's")
)

// Invite 是邀请方一次配对所需的状态。
type Invite struct {
	Code         string
	Self         *identity.Identity
	Chain        *chain.Chain // JOIN 成功时会追加"加入 新设备"记录；失败或 ADOPT 时保持不变
	DiscoveryKey []byte
	// RelayConfig 是邀请方的中继配置串，随欢迎消息发给加入方（M2a）；没有配置中继时为空。
	// M6a 起填中继列表里的第一个，给旧版本用。
	RelayConfig string
	// RelaySettings 是邀请方的中继设置（pb.RelaySettings 序列化字节，M6a），随欢迎消息原样发出；可能为空。
	RelaySettings []byte
	// Commit 在追加记录之后、通知加入方之前调用，用于把新链持久化；返回错误则本次配对失败。
	Commit func(records [][]byte) error
	// Features 是邀请方在 PairChallenge 里声明的功能；含 FeatureCircle2 才会走新流程。
	Features []string
	// Adopt 在 ADOPT 时调用：邀请方丢弃自己的单机圈、换成加入方的圈；返回错误则本次配对失败。
	Adopt func(a *Adopted) error
}

// Adopted 是反向加入时邀请方要换成的设备圈（加入方的圈，已包含"加入 邀请方"记录）。
type Adopted struct {
	Chain        *chain.Chain
	DiscoveryKey []byte
	RelayConfig  string // 加入方的中继配置串；可能为空，调用方自己校验格式
	// RelaySettings 是加入方的中继设置（M6a）；可能为空，调用方按新圈的链校验。
	RelaySettings []byte
	JoinerKeyID   string
}

// Result 是邀请方视角的配对结果。
type Result struct {
	JoinerKeyID string
	JoinerName  string
	Mode        Mode
	// Added 是 MERGE 时本次新签进本机圈的设备名，按来源圈的成员顺序，加入方在内；重配时为空。
	Added []string
}

// JoinRequest 是加入方一次配对所需的状态。
type JoinRequest struct {
	Self     *identity.Identity
	Name     string
	Code     string
	Features []string // 本机支持的功能；含 FeatureCircle2 才会走新流程
	// Chain 是本机当前的链，nil 表示不在圈里；Join 不修改它。
	Chain *chain.Chain
	// DiscoveryKey、RelayConfig、RelaySettings 是本机圈子的发现密钥、第一个中继的配置串和中继设置，ADOPT 时发给邀请方。
	DiscoveryKey  []byte
	RelayConfig   string
	RelaySettings []byte
	MergeOK       bool // 用户已同意两边都有多台设备时合并
	// Commit 在 ADOPT 时、追加"加入 邀请方"之后调用，用于持久化；返回错误则本次配对失败。
	Commit func(records [][]byte) error
}

// Joined 是加入方视角的配对结果。ADOPT 时 Chain 是本机追加后的链，DiscoveryKey、RelayConfig、RelaySettings 是本机自己的。
// MERGE 时 Chain 是邀请方追加了来源圈全部成员之后的链，DiscoveryKey、RelayConfig、RelaySettings 是邀请方的。
type Joined struct {
	Chain        *chain.Chain
	DiscoveryKey []byte
	InviterKeyID string
	InviterName  string
	RelayConfig  string // JOIN 时是邀请方的中继配置串；可能为空，调用方自己校验格式
	// RelaySettings 是 JOIN 时邀请方的中继设置（M6a）；旧版本邀请方不发，可能为空，调用方按新圈的链校验。
	RelaySettings []byte
	Mode          Mode
}

// contextInfo 把本条 TLS 连接的导出值绑定进 CPace：中间人终止的两条连接导出值不同，
// 双方算出的密钥就不同，确认标签必然对不上。
func contextInfo(exporter []byte) *cpace.ContextInfo {
	return cpace.NewContextInfo("portide-joiner", "portide-inviter", append([]byte(associatedData), exporter...))
}

// confirmTag 按方向派生确认标签：两个方向的标签不同，防止把对方的标签原样反射回去。
func confirmTag(key []byte, role string) ([]byte, error) {
	mk, err := hkdf.Expand(sha256.New, key, "portide-pair-confirm-v2", 32)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, mk)
	m.Write([]byte(role))
	return m.Sum(nil), nil
}

func signed(context string, exporter []byte) []byte { return append([]byte(context), exporter...) }

// refusals 是 PairDecision.error 的取值与对应错误。
var refusals = map[string]error{
	wrongCodeMsg:        ErrWrongCode,
	sameCircleMsg:       ErrSameCircle,
	mergeUnsupportedMsg: ErrMergeUnsupported,
	mergeUnconfirmedMsg: ErrMergeNotConfirmed,
}

// refusalMsg 是 err 在 PairDecision.error 里的写法。
func refusalMsg(err error) string {
	for msg, e := range refusals {
		if e == err {
			return msg
		}
	}
	return internalMsg
}

// circleID 返回圈子 ID 的原始字节（创世记录的 SHA-256）；c 为 nil 时返回 nil。
func circleID(c *chain.Chain) []byte {
	if c == nil {
		return nil
	}
	sum := sha256.Sum256(c.Records()[0])
	return sum[:]
}

// solo 判断 c 的当前成员是否只有 self。
func solo(c *chain.Chain, self *identity.Identity) bool {
	m := c.Members()
	return len(m) == 1 && m[0].KeyID == self.KeyID
}

// isPrefix 报告 a 是否是 b 的前缀（逐条记录比较）。
func isPrefix(a, b [][]byte) bool {
	return len(a) <= len(b) && slices.EqualFunc(a, b[:len(a)], bytes.Equal)
}

// checkSameCircle 检查 theirs 能不能换掉 mine：mine 为 nil 或不是同一个圈时放行；
// 同一个圈时只接受以 mine 为前缀的链，否则对方的链比本机旧或者分叉了，换过去会丢掉本机见过的记录（例如移除）。
func checkSameCircle(mine, theirs *chain.Chain) error {
	if mine == nil || !bytes.Equal(circleID(mine), circleID(theirs)) {
		return nil
	}
	if !isPrefix(mine.Records(), theirs.Records()) {
		return ErrStaleCircle
	}
	return nil
}

// decide 按设计方案 6.3 决定配对结果；返回的错误是拒绝原因。
// joinerFeatures 是加入方在 PairConfirm 里声明的功能。
func decide(inv Invite, joinerKeyID string, cs *pb.CircleState, joinerFeatures []string) (Mode, error) {
	member := inv.Chain.IsMember(joinerKeyID)
	// 创世记录相同就是同一个圈（第 2 步），绝不 ADOPT（那会用加入方的旧副本覆盖本机，丢掉移除记录）：
	// 加入方是成员，拒绝；本机把它记为已移除，说明本机的链更新、它拿的是移除之前的旧链，按 JOIN 重新加进来；
	// 其余情况（本机没见过它，多半是本机的链比它旧）也拒绝，免得 JOIN 把它的新链换回本机的旧链。
	if bytes.Equal(cs.GetCircleId(), circleID(inv.Chain)) {
		if _, removed := inv.Chain.FormerMember(joinerKeyID); !member && removed {
			return ModeJoin, nil
		}
		return "", ErrSameCircle
	}
	switch {
	case cs.GetMembers() <= 1:
		// 加入方单机或不在圈里；已是成员时是欢迎消息丢了之后的重配，serveJoin 直接重发链。
		// 已是成员、但这期间圈里又有了别的设备的，不在这里：JOIN 会让它丢下那些设备（设计方案 1），走下面的规则。
		return ModeJoin, nil
	case solo(inv.Chain, inv.Self):
		return ModeAdopt, nil
	case !cs.GetMergeOk():
		return "", ErrMergeNotConfirmed
	case !slices.Contains(inv.Features, FeatureMerge) || !slices.Contains(joinerFeatures, FeatureMerge):
		return "", ErrMergeUnsupported
	default:
		return ModeMerge, nil
	}
}

// Serve 在邀请方处理一条配对流。rw 是加入方打开的第一条流；exporter 是本条 TLS 连接以
// ExporterLabel 导出的 ExporterLen 字节。超时由调用方通过流的截止时间控制。
func Serve(rw io.ReadWriter, exporter []byte, inv Invite) (*Result, error) {
	if len(exporter) != ExporterLen {
		return nil, fmt.Errorf("pairing: exporter must be %d bytes", ExporterLen)
	}
	var hello pb.PairHello
	if err := wire.ReadMax(rw, &hello, maxPreAuthSize); err != nil {
		return nil, fmt.Errorf("pairing: read hello: %w", err)
	}
	msgB, key, err := cpace.Exchange(inv.Code, contextInfo(exporter), hello.CpaceMsgA)
	if err != nil {
		return nil, fmt.Errorf("pairing: cpace: %w", err)
	}
	respTag, err := confirmTag(key, roleResponder)
	if err != nil {
		return nil, err
	}
	if err := wire.Write(rw, &pb.PairChallenge{CpaceMsgB: msgB, ResponderTag: respTag, Features: inv.Features}); err != nil {
		return nil, err
	}
	var confirm pb.PairConfirm
	if err := wire.ReadMax(rw, &confirm, maxPreAuthSize); err != nil {
		// 加入方在校验我们的标签后放弃，也说明配对码不一致。
		return nil, ErrWrongCode
	}
	// 双方都声明了 circle2 才走新流程：加入方读的第一条回复是 PairDecision，否则是 PairWelcome。
	circle2 := slices.Contains(inv.Features, FeatureCircle2) && slices.Contains(confirm.Features, FeatureCircle2)
	refuse := func(msg string) {
		if circle2 {
			_ = wire.Write(rw, &pb.PairDecision{Error: msg})
		} else {
			_ = wire.Write(rw, &pb.PairWelcome{Error: msg})
		}
	}
	wantTag, err := confirmTag(key, roleInitiator)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(confirm.InitiatorTag, wantTag) {
		refuse(wrongCodeMsg)
		return nil, ErrWrongCode
	}
	// 加入方已证明知道配对码；再校验它确实持有所声明的设备密钥。
	if len(confirm.DeviceKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(ed25519.PublicKey(confirm.DeviceKey), signed(joinerContext, exporter), confirm.Signature) {
		refuse("invalid identity")
		return nil, errors.New("pairing: joiner identity proof is invalid")
	}
	joinerPub := ed25519.PublicKey(confirm.DeviceKey)
	joinerKeyID := identity.KeyIDOf(joinerPub)
	res := &Result{JoinerKeyID: joinerKeyID, JoinerName: confirm.DeviceName, Mode: ModeJoin}

	mode := ModeJoin
	if circle2 {
		if mode, err = decide(inv, joinerKeyID, confirm.Circle, confirm.Features); err != nil {
			refuse(refusalMsg(err))
			return nil, err
		}
		if mode == ModeAdopt && inv.Adopt == nil {
			refuse(internalMsg)
			return nil, errors.New("pairing: adopt requested but Invite.Adopt is nil")
		}
	}
	// 旧版本不认识改名记录：先拒绝，免得提交之后它读不了欢迎消息里的链，留下一个永远连不上的成员。
	// MERGE 不做这个检查：加入方声明了 merge，一定认识改名；来源圈里的旧版本设备等升级后再迁移（设计 6.3）。
	if mode == ModeJoin && !inv.Chain.IsMember(joinerKeyID) &&
		inv.Chain.HasRenames() && !slices.Contains(confirm.Features, chain.FeatureRename) {
		refuse(outdatedMsg)
		return nil, ErrJoinerOutdated
	}
	if circle2 {
		self, _ := inv.Chain.Member(inv.Self.KeyID)
		d := &pb.PairDecision{
			InviterKey:  inv.Self.Public,
			InviterName: self.Name,
			Signature:   ed25519.Sign(inv.Self.Private, signed(inviterContext, exporter)),
		}
		switch mode {
		case ModeAdopt:
			d.Mode = pb.PairDecision_ADOPT
		case ModeMerge:
			d.Mode = pb.PairDecision_MERGE
		}
		if err := wire.Write(rw, d); err != nil {
			return nil, err
		}
	}
	switch mode {
	case ModeAdopt:
		if err := serveAdopt(rw, exporter, inv, joinerPub); err != nil {
			return nil, err
		}
	case ModeMerge:
		if res.Added, err = serveMerge(rw, exporter, inv, joinerKeyID, confirm.Circle); err != nil {
			return nil, err
		}
	default:
		if err := serveJoin(rw, exporter, inv, joinerPub, confirm.DeviceName); err != nil {
			return nil, err
		}
	}
	res.Mode = mode
	// 等加入方读完并关闭流，确保消息送达后再返回（调用方随后会关闭连接）。
	_, _ = io.Copy(io.Discard, rw)
	return res, nil
}

// serveJoin 把加入方加进邀请方的链并发出欢迎消息（JOIN）。
func serveJoin(rw io.ReadWriter, exporter []byte, inv Invite, joinerPub ed25519.PublicKey, name string) error {
	// 加入方已经是成员，通常是上一次配对在邀请方提交后、加入方收到欢迎消息前中断了。
	// 这次配对码已经得到证明、公钥也相同，直接重发当前的链，让它完成加入。
	if !inv.Chain.IsMember(identity.KeyIDOf(joinerPub)) {
		// 在副本上追加并提交，提交成功后才替换调用方的链，保证失败时链保持不变。
		next, err := chain.Load(inv.Chain.Records())
		if err != nil {
			_ = wire.Write(rw, &pb.PairWelcome{Error: internalMsg})
			return err
		}
		if err := next.AddDevice(inv.Self, joinerPub, name); err != nil {
			_ = wire.Write(rw, &pb.PairWelcome{Error: "invalid device"})
			return err
		}
		if inv.Commit != nil {
			if err := inv.Commit(next.Records()); err != nil {
				_ = wire.Write(rw, &pb.PairWelcome{Error: internalMsg})
				return err
			}
		}
		*inv.Chain = *next
	}
	return writeWelcome(rw, exporter, inv)
}

// writeWelcome 把邀请方当前的链、发现密钥、中继配置和中继设置发给加入方（JOIN、MERGE）。
func writeWelcome(rw io.ReadWriter, exporter []byte, inv Invite) error {
	return wire.Write(rw, &pb.PairWelcome{
		Chain:         inv.Chain.Records(),
		DiscoveryKey:  inv.DiscoveryKey,
		InviterKey:    inv.Self.Public,
		RelayConfig:   inv.RelayConfig,
		RelaySettings: inv.RelaySettings,
		Signature:     ed25519.Sign(inv.Self.Private, signed(inviterContext, exporter)),
	})
}

// serveMerge 读加入方的来源圈 A，把 A 的当前成员逐台签进本机的圈 B，一次提交后发出欢迎消息（MERGE）。
// 返回本次追加的设备名（按 A 的成员顺序）；A 的成员都已在 B 里时（欢迎消息丢了之后的重配）不追加，直接重发链。
func serveMerge(rw io.ReadWriter, exporter []byte, inv Invite, joinerKeyID string, cs *pb.CircleState) ([]string, error) {
	var m pb.PairMerge
	if err := wire.ReadMax(rw, &m, wire.MaxMessageSize); err != nil {
		return nil, fmt.Errorf("pairing: read joiner's circle: %w", err)
	}
	refuse := func(msg string, err error) ([]string, error) {
		_ = wire.Write(rw, &pb.PairWelcome{Error: msg})
		return nil, err
	}
	a, err := chain.Load(m.Chain)
	if err != nil {
		return refuse("invalid chain", fmt.Errorf("pairing: invalid chain from joiner: %w", err))
	}
	// A 必须是加入方在 PairConfirm 里报的那个圈、不是本机的圈，并且加入方是它的当前成员。
	if !bytes.Equal(circleID(a), cs.GetCircleId()) || bytes.Equal(circleID(a), circleID(inv.Chain)) || !a.IsMember(joinerKeyID) {
		return refuse("invalid chain", errors.New("pairing: chain from joiner is not its own circle"))
	}
	// 在副本上追加并提交，提交成功后才替换调用方的链，保证失败时链保持不变。
	next, err := chain.Load(inv.Chain.Records())
	if err != nil {
		return refuse(internalMsg, err)
	}
	var added []string
	for _, member := range a.Members() {
		if next.IsMember(member.KeyID) {
			continue
		}
		if err := next.AddDevice(inv.Self, member.PublicKey, member.Name); err != nil {
			return refuse("invalid device", err)
		}
		added = append(added, member.Name)
	}
	if len(added) > 0 {
		if inv.Commit != nil {
			if err := inv.Commit(next.Records()); err != nil {
				return refuse(internalMsg, err)
			}
		}
		*inv.Chain = *next
	}
	if err := writeWelcome(rw, exporter, inv); err != nil {
		return nil, err
	}
	return added, nil
}

// serveAdopt 读加入方发来的圈子，校验后交给 inv.Adopt 换圈，并用 PairAck 回复结果（ADOPT）。
func serveAdopt(rw io.ReadWriter, exporter []byte, inv Invite, joinerPub ed25519.PublicKey) error {
	var w pb.PairWelcome
	if err := wire.ReadMax(rw, &w, wire.MaxMessageSize); err != nil {
		return fmt.Errorf("pairing: read joiner's circle: %w", err)
	}
	if w.Error != "" {
		return fmt.Errorf("pairing: joiner could not add this device: %s", w.Error)
	}
	ack := func(msg string) { _ = wire.Write(rw, &pb.PairAck{Error: msg}) }
	// 公钥必须是 PairConfirm 里已验证过的那把，签名用单独的上下文，不能拿正向的欢迎消息冒充。
	if !bytes.Equal(w.InviterKey, joinerPub) ||
		!ed25519.Verify(joinerPub, signed(adopterContext, exporter), w.Signature) {
		ack("invalid identity")
		return errors.New("pairing: joiner's circle proof is invalid")
	}
	c, err := chain.Load(w.Chain)
	if err != nil {
		ack("invalid chain")
		return fmt.Errorf("pairing: invalid chain from joiner: %w", err)
	}
	// 防御：和本机同一个圈的链（哪怕加入方谎报了 circle_id）不能拿来换圈，否则旧副本会覆盖本机的移除记录。
	if bytes.Equal(circleID(c), circleID(inv.Chain)) {
		ack("same circle")
		return errors.New("pairing: chain from joiner is a copy of this device's own circle")
	}
	joinerKeyID := identity.KeyIDOf(joinerPub)
	if !c.IsMember(joinerKeyID) || !c.IsMember(inv.Self.KeyID) {
		ack("invalid chain")
		return errors.New("pairing: chain from joiner does not contain both devices")
	}
	if len(w.DiscoveryKey) != 32 {
		ack("invalid discovery key")
		return errors.New("pairing: invalid discovery key")
	}
	if err := inv.Adopt(&Adopted{Chain: c, DiscoveryKey: w.DiscoveryKey, RelayConfig: w.RelayConfig,
		RelaySettings: w.RelaySettings, JoinerKeyID: joinerKeyID}); err != nil {
		ack(internalMsg)
		return err
	}
	// 已经换圈，回复送不到也算成功：加入方会报错，用户再配一次时双方已在同一个圈里。
	ack("")
	return nil
}

// Join 在加入方执行配对。rw 是本机在配对连接上打开的第一条流；exporter 同 Serve。
func Join(rw io.ReadWriter, exporter []byte, req JoinRequest) (*Joined, error) {
	if len(exporter) != ExporterLen {
		return nil, fmt.Errorf("pairing: exporter must be %d bytes", ExporterLen)
	}
	msgA, st, err := cpace.Start(req.Code, contextInfo(exporter))
	if err != nil {
		return nil, fmt.Errorf("pairing: cpace: %w", err)
	}
	if err := wire.Write(rw, &pb.PairHello{CpaceMsgA: msgA}); err != nil {
		return nil, err
	}
	var ch pb.PairChallenge
	if err := wire.ReadMax(rw, &ch, maxPreAuthSize); err != nil {
		return nil, fmt.Errorf("pairing: read challenge: %w", err)
	}
	key, err := st.Finish(ch.CpaceMsgB)
	if err != nil {
		return nil, fmt.Errorf("pairing: cpace: %w", err)
	}
	wantResp, err := confirmTag(key, roleResponder)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(ch.ResponderTag, wantResp) {
		return nil, ErrWrongCode // 不发出任何身份信息
	}
	// 本机的链里已经没有本机（被移除了），等同于不在圈里：报 0，让邀请方按 JOIN 处理。
	members := 0
	if req.Chain != nil && req.Chain.IsMember(req.Self.KeyID) {
		members = len(req.Chain.Members())
	}
	circle2 := slices.Contains(ch.Features, FeatureCircle2) && slices.Contains(req.Features, FeatureCircle2)
	// 本机圈里还有其他设备，旧版本的邀请方只会把本机拉进它的圈：停在这里，什么都不发、不提交。
	if !circle2 && members > 1 {
		return nil, ErrInviterOutdated
	}
	myTag, err := confirmTag(key, roleInitiator)
	if err != nil {
		return nil, err
	}
	confirm := &pb.PairConfirm{
		InitiatorTag: myTag,
		DeviceKey:    req.Self.Public,
		DeviceName:   req.Name,
		Signature:    ed25519.Sign(req.Self.Private, signed(joinerContext, exporter)),
		Features:     req.Features,
	}
	if circle2 {
		confirm.Circle = &pb.CircleState{Members: uint32(members), CircleId: circleID(req.Chain), MergeOk: req.MergeOK}
	}
	if err := wire.Write(rw, confirm); err != nil {
		return nil, err
	}
	if !circle2 {
		j, err := readWelcome(rw, exporter, req.Self, nil)
		if err != nil {
			return nil, err
		}
		// 旧邀请方可能原样重发一条比本机旧的同圈链：同样要检查前缀。
		if err := checkSameCircle(req.Chain, j.Chain); err != nil {
			return nil, err
		}
		return j, nil
	}

	var d pb.PairDecision
	if err := wire.Read(rw, &d); err != nil {
		return nil, fmt.Errorf("pairing: read decision: %w", err)
	}
	if d.Error != "" {
		if e, ok := refusals[d.Error]; ok {
			return nil, e
		}
		return nil, fmt.Errorf("pairing: inviter refused: %s", d.Error)
	}
	if len(d.InviterKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(ed25519.PublicKey(d.InviterKey), signed(inviterContext, exporter), d.Signature) {
		return nil, errors.New("pairing: inviter identity proof is invalid")
	}
	switch d.Mode {
	case pb.PairDecision_JOIN:
		j, err := readWelcome(rw, exporter, req.Self, d.InviterKey)
		if err != nil {
			return nil, err
		}
		// 本机圈里有其他设备时，只有同一个圈（本机拿的是旧链）才该 JOIN；换到别的圈会丢下这些设备（设计方案 1）。
		// 正常的邀请方不会这么判，可能是旧的判断或者恶意的邀请方：拒绝，本机什么都不改。
		if members > 1 && !bytes.Equal(circleID(j.Chain), circleID(req.Chain)) {
			return nil, ErrUnexpectedJoin
		}
		// 同一个圈：只接受以本机的链为前缀的链。
		if err := checkSameCircle(req.Chain, j.Chain); err != nil {
			return nil, err
		}
		j.InviterName = d.InviterName
		return j, nil
	case pb.PairDecision_ADOPT:
		return joinAdopt(rw, exporter, req, &d)
	case pb.PairDecision_MERGE:
		return joinMerge(rw, exporter, req, &d)
	default:
		return nil, fmt.Errorf("pairing: unsupported pairing mode %v", d.Mode)
	}
}

// readWelcome 读邀请方的欢迎消息并校验（JOIN）。inviterKey 非空时，欢迎消息里的公钥必须与它相同。
func readWelcome(rw io.ReadWriter, exporter []byte, self *identity.Identity, inviterKey []byte) (*Joined, error) {
	var welcome pb.PairWelcome
	if err := wire.Read(rw, &welcome); err != nil {
		return nil, fmt.Errorf("pairing: read welcome: %w", err)
	}
	switch {
	case welcome.Error == wrongCodeMsg:
		return nil, ErrWrongCode
	case welcome.Error != "":
		return nil, fmt.Errorf("pairing: inviter refused: %s", welcome.Error)
	}
	if len(welcome.InviterKey) != ed25519.PublicKeySize ||
		(inviterKey != nil && !bytes.Equal(welcome.InviterKey, inviterKey)) ||
		!ed25519.Verify(ed25519.PublicKey(welcome.InviterKey), signed(inviterContext, exporter), welcome.Signature) {
		return nil, errors.New("pairing: inviter identity proof is invalid")
	}
	inviterKeyID := identity.KeyIDOf(ed25519.PublicKey(welcome.InviterKey))
	c, err := chain.Load(welcome.Chain)
	if err != nil {
		return nil, fmt.Errorf("pairing: invalid chain from inviter: %w", err)
	}
	if !c.IsMember(self.KeyID) || !c.IsMember(inviterKeyID) {
		return nil, errors.New("pairing: chain from inviter does not contain both devices")
	}
	if len(welcome.DiscoveryKey) != 32 {
		return nil, errors.New("pairing: invalid discovery key")
	}
	inviter, _ := c.Member(inviterKeyID)
	return &Joined{
		Chain: c, DiscoveryKey: welcome.DiscoveryKey, InviterKeyID: inviterKeyID, InviterName: inviter.Name,
		RelayConfig: welcome.RelayConfig, RelaySettings: welcome.RelaySettings, Mode: ModeJoin,
	}, nil
}

// joinAdopt 把邀请方加进本机的圈，发给它，等它换圈后回复（ADOPT）。
func joinAdopt(rw io.ReadWriter, exporter []byte, req JoinRequest, d *pb.PairDecision) (*Joined, error) {
	fail := func(msg string, err error) (*Joined, error) {
		_ = wire.Write(rw, &pb.PairWelcome{Error: msg})
		return nil, err
	}
	if req.Chain == nil {
		return fail(internalMsg, errors.New("pairing: inviter asked to join this device's circle, but there is none"))
	}
	inviterPub := ed25519.PublicKey(d.InviterKey)
	inviterKeyID := identity.KeyIDOf(inviterPub)
	// 在副本上追加，不修改调用方的链。邀请方已是成员，说明上一次提交了、但它没换成圈：跳过追加，直接重发链。
	next, err := chain.Load(req.Chain.Records())
	if err != nil {
		return fail(internalMsg, err)
	}
	if !next.IsMember(inviterKeyID) {
		if err := next.AddDevice(req.Self, inviterPub, d.InviterName); err != nil {
			return fail("invalid device", err)
		}
		if req.Commit != nil {
			if err := req.Commit(next.Records()); err != nil {
				return fail(internalMsg, err)
			}
		}
	}
	if err := wire.Write(rw, &pb.PairWelcome{
		Chain:         next.Records(),
		DiscoveryKey:  req.DiscoveryKey,
		InviterKey:    req.Self.Public, // 反向加入：填本机（加入方）的公钥
		RelayConfig:   req.RelayConfig,
		RelaySettings: req.RelaySettings,
		Signature:     ed25519.Sign(req.Self.Private, signed(adopterContext, exporter)),
	}); err != nil {
		return nil, err
	}
	var ack pb.PairAck
	if err := wire.Read(rw, &ack); err != nil {
		return nil, fmt.Errorf("pairing: read ack: %w", err)
	}
	if ack.Error != "" {
		return nil, fmt.Errorf("pairing: inviter could not switch: %s", ack.Error)
	}
	return &Joined{
		Chain: next, DiscoveryKey: req.DiscoveryKey, InviterKeyID: inviterKeyID, InviterName: d.InviterName,
		RelayConfig: req.RelayConfig, RelaySettings: req.RelaySettings, Mode: ModeAdopt,
	}, nil
}

// joinMerge 把本机的来源圈 A 发给邀请方，读回它签进了 A 全部成员的链 B′（MERGE）。不提交、不修改调用方的链。
func joinMerge(rw io.ReadWriter, exporter []byte, req JoinRequest, d *pb.PairDecision) (*Joined, error) {
	// 防御：正常的邀请方只在本机报了 merge_ok、并且本机在圈里时才会判 MERGE。
	if !req.MergeOK || req.Chain == nil {
		return nil, ErrMergeNotConfirmed
	}
	if err := wire.Write(rw, &pb.PairMerge{Chain: req.Chain.Records()}); err != nil {
		return nil, err
	}
	j, err := readWelcome(rw, exporter, req.Self, d.InviterKey)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(circleID(j.Chain), circleID(req.Chain)) {
		return nil, errors.New("pairing: merged chain from inviter is this device's own circle")
	}
	// A 的每个当前成员都要在 B′ 里，否则换过去会丢下它们（设计方案 1）。
	for _, m := range req.Chain.Members() {
		if !j.Chain.IsMember(m.KeyID) {
			return nil, fmt.Errorf("pairing: merged chain from inviter does not contain %q", m.Name)
		}
	}
	j.InviterName = d.InviterName
	j.Mode = ModeMerge
	return j, nil
}

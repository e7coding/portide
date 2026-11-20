// Package chain 实现设备圈的成员签名链（设计方案 3.2）：只能追加，每条记录由当时的成员设备签名。
// 记录有四种：创建设备圈、加入设备、移除设备（设计方案 3.5）、改名（M3c）。
package chain

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"portide/core/identity"
	"portide/core/pb"
)

const signContext = "portide-chain-v1\x00"

// keyCheckContext 是发现密钥校验值的域分隔前缀。
const keyCheckContext = "portide-discovery-key-check-v1\x00"

// KeyCheckSize 是发现密钥校验值的字节数。
const KeyCheckSize = sha256.Size

var (
	// ErrFork 表示两条链在某个位置出现了分叉，需要用户介入（设计方案 10）。
	ErrFork = errors.New("chain: chains have diverged")
	// ErrOtherCircle 表示对端属于另一个设备圈。
	ErrOtherCircle = errors.New("chain: peer belongs to a different circle")
	// ErrTooManyRenames 表示设备圈的改名记录已达上限 MaxRenames。
	ErrTooManyRenames = errors.New("chain: too many renames")
)

// MaxRenames 是一个设备圈最多能新建的改名记录数。改名记录永久留在链上、每次握手都要传，
// 上限约 250 KB，离消息大小上限很远。只限制新建，加载已有的链时不检查。
// 额度是整个设备圈共用的：一个成员可以把它用光，之后谁都不能再改名，移除那个成员也恢复不了；
// 成员本来就是可信的（它还能移除设备），所以接受这个取舍。
const MaxRenames = 1000

// FeatureRename 是在握手和配对里声明"能理解改名记录"的功能名（M3c）。
// 旧版本不认识改名记录，会拒绝含有它的链。
const FeatureRename = "rename"

// MaxNameBytes 是设备名的最大字节数（约 32 个汉字）。
const MaxNameBytes = 96

// ValidName 校验设备名：非空、合法 UTF-8、不超过 MaxNameBytes 字节、不含控制字符。
// 设备名会永久写入签名链并随每次 Hello 传输，所以必须有界。
func ValidName(name string) error {
	if name == "" {
		return errors.New("device name must not be empty")
	}
	if len(name) > MaxNameBytes {
		return fmt.Errorf("device name is longer than %d bytes", MaxNameBytes)
	}
	if !utf8.ValidString(name) {
		return errors.New("device name is not valid utf-8")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("device name contains control characters")
		}
	}
	return nil
}

// Member 是设备圈中的一台设备。
type Member struct {
	KeyID     string
	PublicKey ed25519.PublicKey
	Name      string
	AddedSeq  uint64
	AddedAt   time.Time // 加入记录里的时间（签名者的时钟，只用于显示）
}

// Chain 是一条已校验的签名链。零值不可用，请使用 New 或 Load。
type Chain struct {
	records  [][]byte
	members  []Member
	index    map[string]int    // KeyID → members 下标
	former   map[string]Member // 已被移除、之后没有重新加入的设备（KeyID → 移除前的成员信息）
	keyCheck []byte            // 最近一条移除记录里的发现密钥校验值；没有移除过设备时为 nil
	removals []removalRecord   // 按顺序排列的每一条移除记录的位置和发现密钥校验值
	renames  int               // 改名记录的条数
}

// removalRecord 记下一条移除记录在 records 里的位置（seq）和它携带的发现密钥校验值。
type removalRecord struct {
	seq   int
	check []byte
}

// DiscoveryKeyCheck 返回发现密钥的校验值（写进移除记录）。发现密钥是 256 位随机数，
// 校验值不会泄露密钥本身，只用来让成员确认收到的新密钥就是移除时生成的那一把。
func DiscoveryKeyCheck(key []byte) []byte {
	sum := sha256.Sum256(append([]byte(keyCheckContext), key...))
	return sum[:]
}

// New 创建一个新的设备圈：创世记录由 creator 自己签名，creator 成为第一个成员。
func New(creator *identity.Identity, name string) (*Chain, error) {
	c := &Chain{index: map[string]int{}}
	body := &pb.RecordBody{
		Seq:      0,
		TimeUnix: time.Now().Unix(),
		Op:       &pb.RecordBody_Create{Create: &pb.CreateCircle{DeviceKey: creator.Public, DeviceName: name}},
	}
	if err := c.appendSigned(creator, body); err != nil {
		return nil, err
	}
	return c, nil
}

// Load 校验并加载一条完整的链。
func Load(records [][]byte) (*Chain, error) {
	if len(records) == 0 {
		return nil, errors.New("chain: empty chain")
	}
	c := &Chain{index: map[string]int{}}
	for i, raw := range records {
		if err := c.apply(raw); err != nil {
			return nil, fmt.Errorf("chain: record %d: %w", i, err)
		}
	}
	return c, nil
}

// Records 返回所有记录的原始字节（用于存储和同步）。
func (c *Chain) Records() [][]byte {
	out := make([][]byte, len(c.records))
	copy(out, c.records)
	return out
}

// Len 返回记录条数。
func (c *Chain) Len() int { return len(c.records) }

// CircleID 是创世记录的 SHA-256（大写十六进制），即"用户身份"。
func (c *Chain) CircleID() string {
	sum := sha256.Sum256(c.records[0])
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// IsMember 判断 KeyID 是否为当前成员。
func (c *Chain) IsMember(keyID string) bool {
	_, ok := c.index[keyID]
	return ok
}

// Member 按 KeyID 查找成员。
func (c *Chain) Member(keyID string) (Member, bool) {
	i, ok := c.index[keyID]
	if !ok {
		return Member{}, false
	}
	return c.members[i], true
}

// KeyCheck 返回最近一条移除记录里的发现密钥校验值；没有移除过设备时返回 nil，
// 此时设备圈仍在使用创建时的发现密钥。
func (c *Chain) KeyCheck() []byte {
	return append([]byte(nil), c.keyCheck...)
}

// PrefixFor 返回与发现密钥 key 一致的最长前缀：在第一条"校验值对不上 key"的移除记录之前截断
// （从最后一条对得上 key 的移除记录之后算起；key 是创建时的密钥时，截在第一条移除记录之前）。
// 用 key 主动连接时只出示这段前缀：对方即使是被移除、但还拿着这把旧密钥的设备，也看不到它之后的移除记录和新成员。
func (c *Chain) PrefixFor(key []byte) [][]byte {
	want := DiscoveryKeyCheck(key)
	j := 0
	for i, r := range c.removals {
		if bytes.Equal(r.check, want) {
			j = i + 1
		}
	}
	end := len(c.records)
	if j < len(c.removals) {
		end = c.removals[j].seq
	}
	out := make([][]byte, end)
	copy(out, c.records[:end])
	return out
}

// FormerMember 按 KeyID 查找已被移除的设备（用于显示历史和日志里的设备名）。
func (c *Chain) FormerMember(keyID string) (Member, bool) {
	m, ok := c.former[keyID]
	return m, ok
}

// Members 按加入顺序返回所有成员。
func (c *Chain) Members() []Member {
	out := make([]Member, len(c.members))
	copy(out, c.members)
	return out
}

// AddDevice 由成员 signer 签名追加"加入设备"记录。
func (c *Chain) AddDevice(signer *identity.Identity, device ed25519.PublicKey, name string) error {
	prev := sha256.Sum256(c.records[len(c.records)-1])
	body := &pb.RecordBody{
		Seq:      uint64(len(c.records)),
		PrevHash: prev[:],
		TimeUnix: time.Now().Unix(),
		Op:       &pb.RecordBody_AddDevice{AddDevice: &pb.AddDevice{DeviceKey: device, DeviceName: name}},
	}
	return c.appendSigned(signer, body)
}

// RemoveDevice 由成员 signer 签名追加"移除设备"记录，同时记下新发现密钥 discoveryKey 的校验值。
// 不能移除自己，被移除的必须是当前成员。
func (c *Chain) RemoveDevice(signer *identity.Identity, keyID string, discoveryKey []byte) error {
	m, ok := c.Member(keyID)
	if !ok {
		return errors.New("chain: device is not a member")
	}
	prev := sha256.Sum256(c.records[len(c.records)-1])
	body := &pb.RecordBody{
		Seq:      uint64(len(c.records)),
		PrevHash: prev[:],
		TimeUnix: time.Now().Unix(),
		Op: &pb.RecordBody_RemoveDevice{RemoveDevice: &pb.RemoveDevice{
			DeviceKey:         m.PublicKey,
			DiscoveryKeyCheck: DiscoveryKeyCheck(discoveryKey),
		}},
	}
	return c.appendSigned(signer, body)
}

// RenameDevice 由成员 signer 签名追加"改名"记录（M3c）：keyID 必须是当前成员，可以是 signer 自己。
func (c *Chain) RenameDevice(signer *identity.Identity, keyID, name string) error {
	m, ok := c.Member(keyID)
	if !ok {
		return errors.New("chain: device is not a member")
	}
	if err := ValidName(name); err != nil {
		return fmt.Errorf("chain: %w", err)
	}
	if c.renames >= MaxRenames {
		return ErrTooManyRenames
	}
	prev := sha256.Sum256(c.records[len(c.records)-1])
	body := &pb.RecordBody{
		Seq:      uint64(len(c.records)),
		PrevHash: prev[:],
		TimeUnix: time.Now().Unix(),
		Op:       &pb.RecordBody_RenameDevice{RenameDevice: &pb.RenameDevice{DeviceKey: m.PublicKey, DeviceName: name}},
	}
	return c.appendSigned(signer, body)
}

// Merge 用对端的链更新本链：对端链是本链的延伸则采纳；本链是对端链的延伸或两者相同则不变；否则返回 ErrFork。
func (c *Chain) Merge(other [][]byte) (changed bool, err error) {
	oc, err := Load(other)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(oc.records[0], c.records[0]) {
		return false, ErrOtherCircle
	}
	shorter, longer := c.records, oc.records
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	for i := range shorter {
		if !bytes.Equal(shorter[i], longer[i]) {
			return false, ErrFork
		}
	}
	if oc.Len() <= c.Len() {
		return false, nil
	}
	*c = *oc
	return true, nil
}

// MergeAs 同 Merge，但分叉的一边只有改名记录时自动收敛（改名可以离线做，所以常和别处的改名、配对、移除错开）：
//   - 一边只有改名、另一边有加入或移除记录：后者胜出；
//   - 两边都只有改名：按第一条分歧记录的哈希选出同一条胜出链（哈希小的胜）；
//   - 两边都有加入或移除记录：仍返回 ErrFork，链保持不变。
//
// 本链胜出时不变，由对方收敛后推过来；对方胜出时采用对方的链，再由 signer 把本链分歧部分里净改动过、
// 且与胜出链不同的名字重新签名追加（已被移除的设备跳过；signer 自己已不是成员时不追加）。
// 所以两边把同一台设备改成了不同的名字时，最后生效的是输的一方的名字：结果确定，两边一致。
//
// allowResign 为 nil 或对胜出链返回 true 时才补签（调用方据此挡住有成员读不了改名记录的情况）；
// 不补签或到了 MaxRenames 而没补签的名字通过 skipped 返回，链照样采用胜出的那条。
func (c *Chain) MergeAs(signer *identity.Identity, other [][]byte, allowResign func(winner *Chain) bool) (changed bool, skipped []string, err error) {
	changed, err = c.Merge(other)
	if !errors.Is(err, ErrFork) {
		return changed, nil, err
	}
	oc, err := Load(other)
	if err != nil {
		return false, nil, err
	}
	k := 0
	for bytes.Equal(c.records[k], oc.records[k]) {
		k++
	}
	oursRenames, theirsRenames := c.RenameOnlySince(k), oc.RenameOnlySince(k)
	switch {
	case !oursRenames && !theirsRenames:
		return false, nil, ErrFork
	case !oursRenames:
		return false, nil, nil
	case theirsRenames:
		ours, theirs := sha256.Sum256(c.records[k]), sha256.Sum256(oc.records[k])
		if bytes.Compare(ours[:], theirs[:]) < 0 {
			return false, nil, nil
		}
	}
	base, err := Load(c.records[:k])
	if err != nil {
		return false, nil, err
	}
	// 策略只在第一次真正需要补签时问（它可能要读数据库），而且问的是补签之前的胜出链。
	asked, allowed := false, false
	// 本链的分歧部分只有改名，所以本链和分歧前的成员相同。
	for _, m := range c.members {
		before, _ := base.Member(m.KeyID)
		now, ok := oc.Member(m.KeyID)
		if !ok || !oc.IsMember(signer.KeyID) || m.Name == before.Name || m.Name == now.Name {
			continue
		}
		if !asked {
			asked, allowed = true, allowResign == nil || allowResign(oc)
		}
		if !allowed {
			skipped = append(skipped, m.Name)
			continue
		}
		if err := oc.RenameDevice(signer, m.KeyID, m.Name); errors.Is(err, ErrTooManyRenames) {
			skipped = append(skipped, m.Name)
		} else if err != nil {
			return false, nil, err
		}
	}
	*c = *oc
	return true, skipped, nil
}

// RenameOnlySince 报告从第 seq 条起（含）是否全是改名记录；seq 不小于链长时为 true。
func (c *Chain) RenameOnlySince(seq int) bool {
	for _, raw := range c.records[min(seq, len(c.records)):] {
		if !isRename(raw) {
			return false
		}
	}
	return true
}

// HasRenames 报告链上是否有改名记录（不认识改名记录的旧版本无法加载这样的链）。
func (c *Chain) HasRenames() bool { return c.renames > 0 }

// RenameCount 返回链上改名记录的条数。
func (c *Chain) RenameCount() int { return c.renames }

// isRename 报告一条（已校验过的）记录是不是改名记录。
func isRename(raw []byte) bool {
	var rec pb.Record
	var body pb.RecordBody
	if proto.Unmarshal(raw, &rec) != nil || proto.Unmarshal(rec.Body, &body) != nil {
		return false
	}
	return body.GetRenameDevice() != nil
}

func (c *Chain) appendSigned(signer *identity.Identity, body *pb.RecordBody) error {
	bodyBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(body)
	if err != nil {
		return err
	}
	rec := &pb.Record{
		Body:      bodyBytes,
		Signer:    signer.Public,
		Signature: ed25519.Sign(signer.Private, append([]byte(signContext), bodyBytes...)),
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(rec)
	if err != nil {
		return err
	}
	return c.apply(raw)
}

// apply 校验一条记录并把它追加到链上。
func (c *Chain) apply(raw []byte) error {
	var rec pb.Record
	if err := proto.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("decode record: %w", err)
	}
	if len(rec.Signer) != ed25519.PublicKeySize {
		return errors.New("bad signer key")
	}
	signer := ed25519.PublicKey(rec.Signer)
	if !ed25519.Verify(signer, append([]byte(signContext), rec.Body...), rec.Signature) {
		return errors.New("bad signature")
	}
	var body pb.RecordBody
	if err := proto.Unmarshal(rec.Body, &body); err != nil {
		return fmt.Errorf("decode body: %w", err)
	}
	seq := uint64(len(c.records))
	if body.Seq != seq {
		return fmt.Errorf("seq %d, want %d", body.Seq, seq)
	}
	if seq == 0 {
		if len(body.PrevHash) != 0 {
			return errors.New("genesis must not have prev_hash")
		}
	} else {
		prev := sha256.Sum256(c.records[seq-1])
		if !bytes.Equal(body.PrevHash, prev[:]) {
			return errors.New("prev_hash mismatch")
		}
		if !c.IsMember(identity.KeyIDOf(signer)) {
			return errors.New("signer is not a member")
		}
	}

	var key []byte
	var name string
	switch op := body.Op.(type) {
	case *pb.RecordBody_RemoveDevice:
		if seq == 0 {
			return errors.New("genesis must be a create record")
		}
		return c.applyRemove(raw, signer, op.RemoveDevice)
	case *pb.RecordBody_RenameDevice:
		if seq == 0 {
			return errors.New("genesis must be a create record")
		}
		return c.applyRename(raw, op.RenameDevice)
	case *pb.RecordBody_Create:
		if seq != 0 {
			return errors.New("create is only allowed in the genesis record")
		}
		if !bytes.Equal(op.Create.DeviceKey, signer) {
			return errors.New("genesis must be signed by the creator")
		}
		key, name = op.Create.DeviceKey, op.Create.DeviceName
	case *pb.RecordBody_AddDevice:
		if seq == 0 {
			return errors.New("genesis must be a create record")
		}
		key, name = op.AddDevice.DeviceKey, op.AddDevice.DeviceName
	default:
		return errors.New("unknown operation")
	}
	if len(key) != ed25519.PublicKeySize {
		return errors.New("bad device key")
	}
	if err := ValidName(name); err != nil {
		return err
	}
	keyID := identity.KeyIDOf(key)
	if c.IsMember(keyID) {
		return fmt.Errorf("device %s is already a member", keyID)
	}
	c.records = append(c.records, raw)
	c.index[keyID] = len(c.members)
	c.members = append(c.members, Member{KeyID: keyID, PublicKey: ed25519.PublicKey(key), Name: name, AddedSeq: seq, AddedAt: time.Unix(body.TimeUnix, 0)})
	delete(c.former, keyID)
	return nil
}

// applyRename 校验并应用一条改名记录（签名、序号和 prev_hash 已由 apply 校验）。
func (c *Chain) applyRename(raw []byte, op *pb.RenameDevice) error {
	if len(op.DeviceKey) != ed25519.PublicKeySize {
		return errors.New("bad device key")
	}
	if err := ValidName(op.DeviceName); err != nil {
		return err
	}
	keyID := identity.KeyIDOf(op.DeviceKey)
	i, ok := c.index[keyID]
	if !ok {
		return fmt.Errorf("device %s is not a member", keyID)
	}
	c.records = append(c.records, raw)
	c.members[i].Name = op.DeviceName
	c.renames++
	return nil
}

// applyRemove 校验并应用一条移除记录（签名、序号和 prev_hash 已由 apply 校验）。
// 被移除的设备以后可以重新配对加入（新的"加入设备"记录）。
func (c *Chain) applyRemove(raw []byte, signer ed25519.PublicKey, op *pb.RemoveDevice) error {
	if len(op.DeviceKey) != ed25519.PublicKeySize {
		return errors.New("bad device key")
	}
	if len(op.DiscoveryKeyCheck) != KeyCheckSize {
		return errors.New("bad discovery key check")
	}
	keyID := identity.KeyIDOf(op.DeviceKey)
	if keyID == identity.KeyIDOf(signer) {
		return errors.New("a device cannot remove itself")
	}
	i, ok := c.index[keyID]
	if !ok {
		return fmt.Errorf("device %s is not a member", keyID)
	}
	c.records = append(c.records, raw)
	if c.former == nil {
		c.former = map[string]Member{}
	}
	c.former[keyID] = c.members[i]
	c.members = append(c.members[:i:i], c.members[i+1:]...)
	c.index = make(map[string]int, len(c.members))
	for j, m := range c.members {
		c.index[m.KeyID] = j
	}
	c.keyCheck = append([]byte(nil), op.DiscoveryKeyCheck...)
	c.removals = append(c.removals, removalRecord{seq: len(c.records) - 1, check: c.keyCheck})
	return nil
}

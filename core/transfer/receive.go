package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
	"lukechampine.com/blake3"

	"portide/core/pb"
	"portide/core/store"
	"portide/core/wire"
)

const (
	// partDir 是保存目录下存放未完成文件（.part）的子目录。
	partDir = ".portide-parts"
	// chunkSize 是写入临时文件的块大小（设计方案 5.2）。
	chunkSize = 1 << 20
	// spaceReserve 是磁盘空间预检时额外留出的余量。
	spaceReserve = 16 << 20
)

var (
	// ErrNoSpace 表示保存目录所在磁盘的剩余空间不够。
	ErrNoSpace = errors.New("transfer: not enough disk space")
	// errStream 表示数据流中途断开：保留临时文件，等续传。
	errStream = errors.New("transfer: data stream broken")
)

// AcceptOptions 配置一次接收。
type AcceptOptions struct {
	SaveDir string       // 保存目录（必须已存在）
	Store   *store.Store // 续传记录
	Peer    string       // 发送方 KeyID
	OnText  func(text string)
	OnFile  func(path string) // 文件收齐并改名后调用，参数是保存目录下的相对路径
}

// Incoming 是一次已接受的接收。并发安全：每个文件的数据流可以同时调用 ReceiveFile。
type Incoming struct {
	opts  AcceptOptions
	root  *os.Root
	key   store.IncomingKey
	offer *pb.Offer
	paths []string // 每个条目在保存目录下的实际路径；文本为空

	wg       sync.WaitGroup
	mu       sync.Mutex
	done     []bool
	busy     []bool
	finished bool
	got      []atomic.Int64 // 每个文件条目已经在本机的字节数（续传偏移加这次收到的），用于显示进度（M3b）
}

// Progress 返回这次接收已经在本机的字节数和总字节数（包括续传前收到的部分）。
func (in *Incoming) Progress() (done, total int64) {
	for i, it := range in.offer.Items {
		if it.Kind == pb.ItemKind_ITEM_KIND_FILE {
			done += in.got[i].Load()
			total += int64(it.Size)
		}
	}
	return done, total
}

// Accept 接受一个已通过 CheckOffer 的报价：恢复或建立续传记录、检查磁盘空间、创建文件夹、交付文本，
// 返回每个条目的续传偏移（与报价条目一一对应）。
func Accept(o *pb.Offer, opts AcceptOptions) (*Incoming, []uint64, error) {
	root, err := os.OpenRoot(opts.SaveDir)
	if err != nil {
		return nil, nil, err
	}
	in, offsets, err := accept(root, o, opts)
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	return in, offsets, nil
}

func accept(root *os.Root, o *pb.Offer, opts AcceptOptions) (*Incoming, []uint64, error) {
	key := store.IncomingKey{PeerKey: opts.Peer, TransferID: hex.EncodeToString(o.TransferId)}
	journal, err := opts.Store.IncomingItems(key)
	if err != nil {
		return nil, nil, err
	}
	n := len(o.Items)
	in := &Incoming{opts: opts, root: root, key: key, offer: o, paths: make([]string, n), done: make([]bool, n), busy: make([]bool, n), got: make([]atomic.Int64, n)}
	resumed := len(journal) > 0
	digest := offerDigest(o)
	if resumed {
		if len(journal) != n || journal[0].Digest != digest {
			return nil, nil, ErrBadOffer // 同一个传输 ID 的条目变了
		}
		for i := range o.Items {
			in.paths[i], in.done[i] = journal[i].Path, journal[i].Done
		}
	} else if err := in.resolve(); err != nil {
		return nil, nil, err
	}

	offsets := make([]uint64, n)
	var need uint64
	for i, it := range o.Items {
		if it.Kind != pb.ItemKind_ITEM_KIND_FILE {
			continue
		}
		if in.done[i] {
			offsets[i] = it.Size
			continue
		}
		if fi, err := root.Lstat(in.partName(i)); err == nil && fi.Mode().IsRegular() && uint64(fi.Size()) <= it.Size {
			offsets[i] = uint64(fi.Size())
		}
		need += it.Size - offsets[i]
	}
	for i := range offsets {
		in.got[i].Store(int64(offsets[i]))
	}
	if free, err := freeSpace(opts.SaveDir); err == nil && need+spaceReserve > free {
		return nil, nil, fmt.Errorf("%w: need %s, free %s", ErrNoSpace, FormatSize(int64(need)), FormatSize(int64(free)))
	}

	if !resumed {
		rec := map[int]string{}
		for i, p := range in.paths {
			rec[i] = p
		}
		if err := opts.Store.AddIncoming(key, digest, rec); err != nil {
			return nil, nil, err
		}
	}
	if err := root.MkdirAll(partDir, 0o700); err != nil {
		return nil, nil, err
	}
	for i, it := range o.Items {
		if in.done[i] {
			continue
		}
		switch it.Kind {
		case pb.ItemKind_ITEM_KIND_DIR:
			if err := root.MkdirAll(in.paths[i], 0o755); err != nil {
				return nil, nil, err
			}
			in.markDone(i)
		case pb.ItemKind_ITEM_KIND_FILE:
			if dir := path.Dir(in.paths[i]); dir != "." {
				if err := root.MkdirAll(dir, 0o755); err != nil {
					return nil, nil, err
				}
			}
			if it.Size == 0 {
				// 空文件没有数据流（续传偏移 0 等于大小，发送方会跳过），在这里直接创建。
				if err := in.createEmpty(i); err != nil {
					return nil, nil, err
				}
			}
		case pb.ItemKind_ITEM_KIND_TEXT:
			if opts.OnText != nil {
				opts.OnText(it.Text)
			}
			in.markDone(i)
		}
	}
	return in, offsets, nil
}

// offerDigest 是报价条目的摘要：续传时对方必须给出与第一次完全相同的条目（类型、路径、大小、修改时间）。
func offerDigest(o *pb.Offer) string {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(&pb.Offer{Items: o.Items})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// resolve 为新的接收确定每个条目的实际路径。
func (in *Incoming) resolve() error {
	var paths []string
	var isDir []bool
	var idx []int
	for i, it := range in.offer.Items {
		if it.Kind == pb.ItemKind_ITEM_KIND_TEXT {
			continue
		}
		p, err := CleanPath(it.Path)
		if err != nil {
			return ErrBadOffer
		}
		paths = append(paths, p)
		isDir = append(isDir, it.Kind == pb.ItemKind_ITEM_KIND_DIR)
		idx = append(idx, i)
	}
	// 其他尚未结束的接收（进行中或中断等续传）已经占用的顶层名字也算重名：它们的文件还没落盘，
	// 但续传完成后会用到这些名字。
	tops, err := in.opts.Store.IncomingTops()
	if err != nil {
		return err
	}
	busy := map[string]bool{}
	for _, t := range tops {
		busy[foldKey(t)] = true
	}
	exists := func(name string) bool {
		_, err := in.root.Lstat(name)
		return err == nil || !errors.Is(err, fs.ErrNotExist)
	}
	resolved, err := resolvePaths(paths, isDir, func(p string) bool { return p == partDir || busy[foldKey(p)] || exists(p) })
	if err != nil {
		return err
	}
	for j, i := range idx {
		in.paths[i] = resolved[j]
	}
	return nil
}

func (in *Incoming) markDone(i int) {
	in.mu.Lock()
	in.done[i] = true
	in.mu.Unlock()
	_ = in.opts.Store.MarkIncomingDone(in.key, i)
}

func (in *Incoming) partName(i int) string { return partName(in.key, i) }

func partName(k store.IncomingKey, i int) string {
	return fmt.Sprintf("%s/%.16s-%s-%d.part", partDir, k.PeerKey, k.TransferID, i)
}

// ReceiveFile 处理一条数据流（Envelope{FileData} 已被读出）：写入临时文件、核对哈希、改名为正式文件，
// 最后在流上回 FileAck。数据流中途断开时不回复、保留临时文件（等续传），并返回错误。
func (in *Incoming) ReceiveFile(str io.ReadWriter, fd *pb.FileData) error {
	i := int(fd.Item)
	if i < 0 || i >= len(in.offer.Items) || in.offer.Items[i].Kind != pb.ItemKind_ITEM_KIND_FILE || !bytes.Equal(fd.TransferId, in.offer.TransferId) {
		return in.ack(str, &pb.FileAck{Error: "unknown item"}, ErrBadOffer)
	}
	in.mu.Lock()
	if in.finished || in.done[i] || in.busy[i] {
		in.mu.Unlock()
		return in.ack(str, &pb.FileAck{Error: "item not expected"}, ErrBadOffer)
	}
	in.busy[i] = true
	in.wg.Add(1)
	in.mu.Unlock()
	defer func() {
		in.mu.Lock()
		in.busy[i] = false
		in.mu.Unlock()
		in.wg.Done()
	}()

	ack, err := in.receive(str, i, fd.Offset)
	if errors.Is(err, errStream) {
		return err // 不回复：对端已经断开或违反协议
	}
	if ack.Ok && in.opts.OnFile != nil {
		in.opts.OnFile(in.paths[i])
	}
	return in.ack(str, ack, err)
}

func (in *Incoming) ack(str io.Writer, a *pb.FileAck, err error) error {
	if werr := wire.Write(str, a); err == nil {
		err = werr
	}
	return err
}

// receive 写入并核对一个文件，返回要回复的 FileAck；返回 errStream 时不回复。
func (in *Incoming) receive(str io.Reader, i int, offset uint64) (*pb.FileAck, error) {
	it := in.offer.Items[i]
	part := in.partName(i)
	f, err := in.root.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return &pb.FileAck{Error: "cannot create file"}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return &pb.FileAck{Error: "cannot stat file"}, err
	}
	if offset > it.Size || (offset != 0 && offset != uint64(fi.Size())) {
		return &pb.FileAck{Error: "offset mismatch"}, ErrBadOffer
	}
	if offset == 0 {
		if err := f.Truncate(0); err != nil {
			return &pb.FileAck{Error: "cannot write file"}, err
		}
	}
	h := blake3.New(32, nil)
	// 续传：顺序重读已有部分，恢复整文件哈希（设计方案 5.2）。
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, int64(offset))); err != nil {
		return &pb.FileAck{Error: "cannot read partial file"}, err
	}
	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		return &pb.FileAck{Error: "cannot write file"}, err
	}
	in.got[i].Store(int64(offset)) // 从头重传（offset 为 0）时进度跟着回退
	src := &trackReader{r: io.LimitReader(str, int64(it.Size-offset)), count: &in.got[i]}
	n, err := io.CopyBuffer(io.MultiWriter(f, h), src, make([]byte, chunkSize))
	if src.err != nil || (err == nil && uint64(n) != it.Size-offset) {
		return nil, errStream
	}
	if err != nil {
		return &pb.FileAck{Error: "cannot write file"}, err
	}
	var end pb.FileEnd
	if err := wire.ReadMax(str, &end, 1024); err != nil {
		return nil, errStream
	}
	if err := f.Sync(); err != nil {
		return &pb.FileAck{Error: "cannot write file"}, err
	}
	f.Close()
	if !bytes.Equal(h.Sum(nil), end.Blake3) {
		_ = in.root.Remove(part)
		return &pb.FileAck{HashMismatch: true, Error: "hash mismatch"}, nil
	}
	final := in.paths[i]
	if _, err := in.root.Lstat(final); err == nil {
		return &pb.FileAck{Error: "destination already exists"}, fs.ErrExist
	}
	if it.MtimeNs != 0 {
		mt := time.Unix(0, it.MtimeNs)
		_ = in.root.Chtimes(part, mt, mt)
	}
	if err := in.root.Rename(part, final); err != nil {
		return &pb.FileAck{Error: "cannot move file into place"}, err
	}
	in.markDone(i)
	return &pb.FileAck{Ok: true}, nil
}

// trackReader 记录底层读取的错误，用来区分"数据流断开"和"写文件失败"；同时累计读到的字节数（接收进度）。
type trackReader struct {
	r     io.Reader
	err   error
	count *atomic.Int64
}

func (t *trackReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.count.Add(int64(n))
	if err != nil && err != io.EOF {
		t.err = err
	}
	return n, err
}

// Saved 返回已经完整收到并落盘的文件（包括续传前那次连接里收齐的）。
func (in *Incoming) Saved() []SavedFile {
	in.mu.Lock()
	defer in.mu.Unlock()
	var out []SavedFile
	for i, it := range in.offer.Items {
		if in.done[i] && it.Kind == pb.ItemKind_ITEM_KIND_FILE {
			out = append(out, SavedFile{Path: in.paths[i], Size: it.Size})
		}
	}
	return out
}

// Complete 报告是否所有条目都已收到。
func (in *Incoming) Complete() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, d := range in.done {
		if !d {
			return false
		}
	}
	return true
}

// Finish 结束这次接收：等待进行中的数据流退出；final 为 true（发送方结束或取消了传输）时删除未完成的临时文件和续传记录，
// 为 false（连接中断）时保留它们等续传。之后不能再调用 ReceiveFile。
func (in *Incoming) Finish(final bool) {
	in.mu.Lock()
	in.finished = true
	in.mu.Unlock()
	in.wg.Wait()
	if final {
		for i, d := range in.done {
			if !d && in.offer.Items[i].Kind == pb.ItemKind_ITEM_KIND_FILE {
				_ = in.root.Remove(in.partName(i))
			}
		}
		_ = in.opts.Store.DeleteIncoming(in.key)
	}
	in.root.Close()
}

// RemoveStale 删除 before 之前开始、一直没有结束的接收留下的临时文件和续传记录；skip 为非 nil 时，
// 对其返回 true 的 key 跳过不删——用来保护仍然活着的接收（见 Receiver.InUse），skip 为 nil 表示
// 没有任何接收是活的（例如守护进程刚启动，还没处理任何报价）。
func RemoveStale(saveDir string, st *store.Store, before time.Time, skip func(store.IncomingKey) bool) error {
	keys, err := st.StaleIncoming(before)
	if err != nil || len(keys) == 0 {
		return err
	}
	root, err := os.OpenRoot(saveDir)
	if err == nil {
		defer root.Close()
	}
	for _, k := range keys {
		if skip != nil && skip(k) {
			continue
		}
		if err := dropIncoming(root, st, k); err != nil {
			return err
		}
	}
	return nil
}

// dropIncoming 删除一次接收留下的 .part 临时文件和续传记录：RemoveStale（过期清理）和
// supersedeOrphans（同一台设备用新传输重新发送同名内容）共用。root 为 nil 时跳过删文件
// （保存目录打不开，但续传记录还是要删，不然名字会一直被占着）。
func dropIncoming(root *os.Root, st *store.Store, k store.IncomingKey) error {
	items, err := st.IncomingItems(k)
	if err != nil {
		return err
	}
	if root != nil {
		for i := range items {
			_ = root.Remove(partName(k, i))
		}
	}
	return st.DeleteIncoming(k)
}

func (in *Incoming) createEmpty(i int) error {
	f, err := in.root.OpenFile(in.paths[i], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	f.Close()
	if mt := in.offer.Items[i].MtimeNs; mt != 0 {
		_ = in.root.Chtimes(in.paths[i], time.Unix(0, mt), time.Unix(0, mt))
	}
	in.markDone(i)
	if in.opts.OnFile != nil {
		in.opts.OnFile(in.paths[i])
	}
	return nil
}

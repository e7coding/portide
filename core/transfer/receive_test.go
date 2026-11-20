package transfer

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lukechampine.com/blake3"

	"portide/core/pb"
	"portide/core/store"
	"portide/core/wire"
)

const testPeer = "AAAABBBBCCCCDDDDEEEEFFFF0000111122223333444455556666777788889999"

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func fileOffer(items ...*pb.Item) *pb.Offer {
	return &pb.Offer{TransferId: NewTransferID(), Items: items}
}

func file(p string, data []byte, mtime time.Time) *pb.Item {
	return &pb.Item{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: p, Size: uint64(len(data)), MtimeNs: mtime.UnixNano()}
}

// feed 模拟发送方的一条数据流：从 offset 开始发 data，再发 FileEnd（哈希为 sum，nil 表示正确的哈希）。
// cut 大于 0 时只发 cut 个字节就断开。返回接收方的 FileAck（断开时为 nil）和 ReceiveFile 的错误。
func feed(t *testing.T, in *Incoming, o *pb.Offer, item int, offset uint64, data, sum []byte, cut int) (*pb.FileAck, error) {
	t.Helper()
	recv, send := net.Pipe()
	errc := make(chan error, 1)
	go func() {
		errc <- in.ReceiveFile(recv, &pb.FileData{TransferId: o.TransferId, Item: uint32(item), Offset: offset})
		recv.Close()
	}()
	if sum == nil {
		h := blake3.Sum256(data)
		sum = h[:]
	}
	body := data[offset:]
	if cut > 0 {
		_, _ = send.Write(body[:cut])
		send.Close()
		return nil, <-errc
	}
	if _, err := send.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := wire.Write(send, &pb.FileEnd{Blake3: sum}); err != nil {
		t.Fatal(err)
	}
	var ack pb.FileAck
	if err := wire.Read(send, &ack); err != nil {
		t.Fatal(err)
	}
	send.Close()
	return &ack, <-errc
}

func TestAcceptReceivesFolderTextAndFile(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	data := []byte("meow meow")
	mtime := time.Date(2025, 5, 1, 12, 0, 0, 0, time.UTC)
	o := fileOffer(
		&pb.Item{Kind: pb.ItemKind_ITEM_KIND_DIR, Path: "照片"},
		&pb.Item{Kind: pb.ItemKind_ITEM_KIND_DIR, Path: "照片/空"},
		file("照片/猫.jpg", data, mtime),
		&pb.Item{Kind: pb.ItemKind_ITEM_KIND_TEXT, Text: "你好"},
	)
	var texts, files []string
	in, offsets, err := Accept(o, AcceptOptions{SaveDir: save, Store: st, Peer: testPeer,
		OnText: func(s string) { texts = append(texts, s) }, OnFile: func(p string) { files = append(files, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 4 || offsets[2] != 0 || len(texts) != 1 || texts[0] != "你好" {
		t.Fatalf("offsets %v texts %v", offsets, texts)
	}
	if fi, err := os.Stat(filepath.Join(save, "照片", "空")); err != nil || !fi.IsDir() {
		t.Fatalf("empty folder not created: %v", err)
	}
	if in.Complete() {
		t.Fatal("complete before the file arrived")
	}
	ack, err := feed(t, in, o, 2, 0, data, nil, 0)
	if err != nil || !ack.Ok {
		t.Fatalf("ack %v err %v", ack, err)
	}
	got, _ := os.ReadFile(filepath.Join(save, "照片", "猫.jpg"))
	fi, _ := os.Stat(filepath.Join(save, "照片", "猫.jpg"))
	if string(got) != string(data) || !fi.ModTime().Equal(mtime) {
		t.Fatalf("content %q mtime %v", got, fi.ModTime())
	}
	if !in.Complete() || len(files) != 1 || files[0] != "照片/猫.jpg" {
		t.Fatalf("complete=%v files=%v", in.Complete(), files)
	}
	in.Finish(true)
	if items, _ := st.IncomingItems(in.key); len(items) != 0 {
		t.Fatalf("journal left behind: %v", items)
	}
}

func TestResumeAfterBrokenStream(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	data := make([]byte, 3*chunkSize+123)
	for i := range data {
		data[i] = byte(i * 7)
	}
	o := fileOffer(file("big.bin", data, time.Now()))
	opts := AcceptOptions{SaveDir: save, Store: st, Peer: testPeer}

	in, _, err := Accept(o, opts)
	if err != nil {
		t.Fatal(err)
	}
	if ack, err := feed(t, in, o, 0, 0, data, nil, chunkSize+10); ack != nil || !errors.Is(err, errStream) {
		t.Fatalf("broken stream: ack %v err %v", ack, err)
	}
	in.Finish(false) // 连接中断：保留临时文件

	in, offsets, err := Accept(o, opts) // 发送方用同一个传输 ID 重新报价
	if err != nil {
		t.Fatal(err)
	}
	if offsets[0] != chunkSize+10 {
		t.Fatalf("resume offset = %d", offsets[0])
	}
	if ack, err := feed(t, in, o, 0, offsets[0], data, nil, 0); err != nil || !ack.Ok {
		t.Fatalf("resume: ack %v err %v", ack, err)
	}
	got, _ := os.ReadFile(filepath.Join(save, "big.bin"))
	if string(got) != string(data) {
		t.Fatal("resumed file differs")
	}
	in.Finish(true)
	if entries, _ := os.ReadDir(filepath.Join(save, partDir)); len(entries) != 0 {
		t.Fatalf("parts left: %v", entries)
	}
}

// 续传时对方换了条目（例如把文件改成文件夹）：拒绝，而不是信任新报价。
func TestResumeRejectsChangedOffer(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	data := []byte("some data")
	o := fileOffer(file("a.bin", data, time.Now()))
	opts := AcceptOptions{SaveDir: save, Store: st, Peer: testPeer}
	in, _, err := Accept(o, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = feed(t, in, o, 0, 0, data, nil, 4)
	in.Finish(false)

	changed := &pb.Offer{TransferId: o.TransferId, Items: []*pb.Item{{Kind: pb.ItemKind_ITEM_KIND_DIR, Path: "a.bin"}}}
	if _, _, err := Accept(changed, opts); !errors.Is(err, ErrBadOffer) {
		t.Fatalf("changed resume accepted: %v", err)
	}
	if _, _, err := Accept(o, opts); err != nil { // 原样的报价仍然可以续传
		t.Fatalf("original resume: %v", err)
	}
}

func TestHashMismatchRemovesPart(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	data := []byte("hello")
	o := fileOffer(file("a.txt", data, time.Now()))
	in, _, err := Accept(o, AcceptOptions{SaveDir: save, Store: st, Peer: testPeer})
	if err != nil {
		t.Fatal(err)
	}
	defer in.Finish(true)
	ack, err := feed(t, in, o, 0, 0, data, make([]byte, 32), 0)
	if err != nil || ack.Ok || !ack.HashMismatch {
		t.Fatalf("ack %v err %v", ack, err)
	}
	if _, err := os.Stat(filepath.Join(save, partName(in.key, 0))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("part kept after hash mismatch")
	}
	// 发送方从头重传一次。
	if ack, err := feed(t, in, o, 0, 0, data, nil, 0); err != nil || !ack.Ok {
		t.Fatalf("retry: ack %v err %v", ack, err)
	}
}

func TestExistingNamesAreKeptAcrossResume(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	must(t, os.WriteFile(filepath.Join(save, "a.txt"), []byte("old"), 0o644))
	data := []byte("new")
	o := fileOffer(file("a.txt", data, time.Now()), file("../../escape.txt", data, time.Now()))
	if _, _, err := Accept(o, AcceptOptions{SaveDir: save, Store: st, Peer: testPeer}); !errors.Is(err, ErrBadOffer) {
		t.Fatalf("traversal accepted: %v", err)
	}
	o = fileOffer(file("a.txt", data, time.Now()))
	opts := AcceptOptions{SaveDir: save, Store: st, Peer: testPeer}
	in, _, err := Accept(o, opts)
	if err != nil {
		t.Fatal(err)
	}
	in.Finish(false)
	in, _, err = Accept(o, opts)
	if err != nil {
		t.Fatal(err)
	}
	if ack, err := feed(t, in, o, 0, 0, data, nil, 0); err != nil || !ack.Ok {
		t.Fatalf("ack %v err %v", ack, err)
	}
	in.Finish(true)
	old, _ := os.ReadFile(filepath.Join(save, "a.txt"))
	got, _ := os.ReadFile(filepath.Join(save, "a (1).txt"))
	if string(old) != "old" || string(got) != "new" {
		t.Fatalf("old=%q new=%q", old, got)
	}
}

// 对方用新的传输 ID 重新发送同名内容（例如源文件变了，refreshOffer 换了新 ID）：旧的中断接收
// 已经被放弃，不应该继续占着这个名字，逼新的一份改名成 "big (1).bin"。
func TestOrphanedJournalDoesNotReserveName(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	opts := AcceptOptions{SaveDir: save, Store: st, Peer: testPeer}
	dataA := []byte("first content")
	oA := fileOffer(file("big.bin", dataA, time.Now()))
	inA, _, err := Accept(oA, opts)
	must(t, err)
	_, _ = feed(t, inA, oA, 0, 0, dataA, nil, 4) // 只收到一部分就断开
	inA.Finish(false)                            // 连接中断：保留 .part 和续传记录

	r := &Receiver{Store: st}
	dataB := []byte("second content, not the same as the first")
	oB := fileOffer(file("big.bin", dataB, time.Now()))
	r.supersedeOrphans(testPeer, save, oB)

	inB, _, err := Accept(oB, opts)
	must(t, err)
	if inB.paths[0] != "big.bin" {
		t.Fatalf("B got %q, want it to reuse the name", inB.paths[0])
	}
	if ack, err := feed(t, inB, oB, 0, 0, dataB, nil, 0); err != nil || !ack.Ok {
		t.Fatalf("feed B: ack %v err %v", ack, err)
	}
	inB.Finish(true)
	got, _ := os.ReadFile(filepath.Join(save, "big.bin"))
	if string(got) != string(dataB) {
		t.Fatalf("content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(save, partName(inA.key, 0))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("A's part not removed")
	}
	if items, _ := st.IncomingItems(inA.key); len(items) != 0 {
		t.Fatalf("A's journal not removed: %v", items)
	}
}

// 不同发送方的中断接收占用了同一个名字：不属于"同一台设备重新发送"，不能被丢弃。
func TestOrphanedJournalFromDifferentPeerIsNotDropped(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	const otherPeer = "9999888877776666555544443333222211110000ffffeeeeddddccccbbbbaaaa"
	dataA := []byte("other peer's content")
	oA := fileOffer(file("big.bin", dataA, time.Now()))
	inA, _, err := Accept(oA, AcceptOptions{SaveDir: save, Store: st, Peer: otherPeer})
	must(t, err)
	_, _ = feed(t, inA, oA, 0, 0, dataA, nil, 4)
	inA.Finish(false)

	r := &Receiver{Store: st}
	oB := fileOffer(file("big.bin", []byte("my content"), time.Now()))
	r.supersedeOrphans(testPeer, save, oB)

	inB, _, err := Accept(oB, AcceptOptions{SaveDir: save, Store: st, Peer: testPeer})
	must(t, err)
	if inB.paths[0] != "big (1).bin" {
		t.Fatalf("B got %q, the other peer's journal should still reserve big.bin", inB.paths[0])
	}
	inB.Finish(true)
	if items, _ := st.IncomingItems(inA.key); len(items) == 0 {
		t.Fatal("the other peer's journal was dropped")
	}
}

// A 的中断接收被对方重新连上、正处于决定阶段（busy：已经不是"孤儿"，但还没进 Accept/active）：
// supersedeOrphans 不能把它当成可以丢弃的孤儿，即使它的顶层名字和新报价 B 冲突。
func TestSupersedeOrphansSkipsReceiveInDecisionPhase(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	opts := AcceptOptions{SaveDir: save, Store: st, Peer: testPeer}
	dataA := []byte("first content")
	oA := fileOffer(file("big.bin", dataA, time.Now()))
	inA, _, err := Accept(oA, opts)
	must(t, err)
	_, _ = feed(t, inA, oA, 0, 0, dataA, nil, 4) // 只收到一部分就断开
	inA.Finish(false)                            // 连接中断：保留 .part 和续传记录

	r := &Receiver{Store: st}
	activeKeyA := inA.key.PeerKey + "/" + inA.key.TransferID
	r.markBusy(activeKeyA) // 模拟对方刚重新连上、正在决定阶段（还没进 Accept/active）
	defer r.clearBusy(activeKeyA)

	dataB := []byte("second content, not the same as the first")
	oB := fileOffer(file("big.bin", dataB, time.Now()))
	r.supersedeOrphans(testPeer, save, oB)

	if items, _ := st.IncomingItems(inA.key); len(items) == 0 {
		t.Fatal("A's journal dropped while it was in the decision phase")
	}
	if _, err := os.Stat(filepath.Join(save, partName(inA.key, 0))); err != nil {
		t.Fatalf("A's part removed while it was in the decision phase: %v", err)
	}

	inB, _, err := Accept(oB, opts)
	must(t, err)
	if inB.paths[0] != "big (1).bin" {
		t.Fatalf("B got %q, A's journal should still reserve big.bin", inB.paths[0])
	}
	inB.Finish(true)
}

func TestNotEnoughSpace(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	o := fileOffer(&pb.Item{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: "huge.bin", Size: 1 << 62})
	if _, _, err := Accept(o, AcceptOptions{SaveDir: save, Store: st, Peer: testPeer}); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("err = %v", err)
	}
	if keys, _ := st.StaleIncoming(time.Now().Add(time.Hour)); len(keys) != 0 {
		t.Fatal("journal written for a rejected offer")
	}
}

func TestRemoveStale(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	data := []byte("partial data")
	o := fileOffer(file("a.bin", data, time.Now()))
	in, _, err := Accept(o, AcceptOptions{SaveDir: save, Store: st, Peer: testPeer})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = feed(t, in, o, 0, 0, data, nil, 4)
	in.Finish(false)
	part := filepath.Join(save, partName(in.key, 0))
	if _, err := os.Stat(part); err != nil {
		t.Fatal("no part to clean up")
	}
	must(t, RemoveStale(save, st, time.Now().Add(-time.Hour), nil)) // 还不算过期
	if _, err := os.Stat(part); err != nil {
		t.Fatal("fresh part removed")
	}
	must(t, RemoveStale(save, st, time.Now().Add(time.Hour), nil))
	if _, err := os.Stat(part); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale part kept")
	}
	if items, _ := st.IncomingItems(in.key); len(items) != 0 {
		t.Fatal("stale journal kept")
	}
}

// 一个中断等续传的接收已经占用了 a.txt：另一个传输的同名文件要改名，两个都能完整落盘。
func TestNamesOfUnfinishedTransfersAreReserved(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	opts := AcceptOptions{SaveDir: save, Store: st, Peer: testPeer}
	first, second := []byte("first"), []byte("second")
	o1 := fileOffer(file("a.txt", first, time.Now()))
	in1, _, err := Accept(o1, opts)
	if err != nil {
		t.Fatal(err)
	}
	in1.Finish(false) // 中断：a.txt 还没落盘，但名字已经被占用

	o2 := fileOffer(file("A.TXT", second, time.Now()))
	in2, _, err := Accept(o2, opts)
	if err != nil {
		t.Fatal(err)
	}
	if in2.paths[0] != "A (1).TXT" {
		t.Fatalf("second transfer got %q", in2.paths[0])
	}
	if ack, err := feed(t, in2, o2, 0, 0, second, nil, 0); err != nil || !ack.Ok {
		t.Fatalf("second: ack %v err %v", ack, err)
	}
	in2.Finish(true)

	in1, _, err = Accept(o1, opts) // 第一个传输续传
	if err != nil {
		t.Fatal(err)
	}
	if ack, err := feed(t, in1, o1, 0, 0, first, nil, 0); err != nil || !ack.Ok {
		t.Fatalf("first: ack %v err %v", ack, err)
	}
	in1.Finish(true)
	a, _ := os.ReadFile(filepath.Join(save, "a.txt"))
	b, _ := os.ReadFile(filepath.Join(save, "A (1).TXT"))
	if string(a) != "first" || string(b) != "second" {
		t.Fatalf("a.txt=%q A (1).TXT=%q", a, b)
	}
}

// Progress 计入续传前已经在本机的字节；文件从头重传（偏移 0）时进度跟着回退，不会重复计算。
func TestIncomingProgress(t *testing.T) {
	save, st := t.TempDir(), openStore(t)
	data := make([]byte, 2*chunkSize)
	small := []byte("小文件")
	o := fileOffer(file("big.bin", data, time.Now()), file("small.txt", small, time.Now()))
	opts := AcceptOptions{SaveDir: save, Store: st, Peer: testPeer}
	total := int64(len(data) + len(small))

	in, _, err := Accept(o, opts)
	if err != nil {
		t.Fatal(err)
	}
	if done, tot := in.Progress(); done != 0 || tot != total {
		t.Fatalf("fresh: %d / %d", done, tot)
	}
	_, _ = feed(t, in, o, 0, 0, data, nil, chunkSize) // 收到一半就断开
	if done, _ := in.Progress(); done != chunkSize {
		t.Fatalf("after a broken stream: done = %d, want %d", done, chunkSize)
	}
	in.Finish(false)

	in, offsets, err := Accept(o, opts)
	if err != nil {
		t.Fatal(err)
	}
	if done, _ := in.Progress(); done != int64(offsets[0]) || done != chunkSize {
		t.Fatalf("resumed: done = %d, offsets %v", done, offsets)
	}
	// 对方选择从头重传（偏移 0）：进度回到 0 再往上加，最后正好等于总数。
	if ack, err := feed(t, in, o, 0, 0, data, nil, 0); err != nil || !ack.Ok {
		t.Fatalf("restart: ack %v err %v", ack, err)
	}
	if ack, err := feed(t, in, o, 1, 0, small, nil, 0); err != nil || !ack.Ok {
		t.Fatalf("small: ack %v err %v", ack, err)
	}
	if done, tot := in.Progress(); done != total || tot != total {
		t.Fatalf("complete: %d / %d", done, tot)
	}
	in.Finish(true)
}

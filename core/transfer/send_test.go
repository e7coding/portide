package transfer

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/pb"
	"portide/core/store"
	"portide/core/transport"
	"portide/core/wire"
)

// harness 是一对通过本机回环 QUIC 连接的设备：sender 一侧只用连接，receiver 一侧用 Receiver 分发流
// （与 node 的流路由做法相同）。
type harness struct {
	t      *testing.T
	save   string
	recv   *Receiver
	addr   string
	client *transport.Transport
	ends   chan Result
	stop   context.CancelFunc // 停止接收方（模拟接收方守护进程退出）

	mu    sync.Mutex
	texts []string
}

// decide 为空时总是接受；否则返回非空的拒绝原因表示拒绝。
func newHarness(t *testing.T, decide func(ctx context.Context) string) *harness {
	t.Helper()
	srv, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cli, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, save: t.TempDir(), addr: srv.Addr().String(), client: cli, ends: make(chan Result, 8)}
	h.recv = &Receiver{
		Store: openStore(t),
		Decide: func(ctx context.Context, _ string, _ *pb.Offer, _ bool) (string, string) {
			if decide != nil {
				if r := decide(ctx); r != "" {
					return "", r
				}
			}
			return h.save, ""
		},
		OnText: func(_, s string) { h.mu.Lock(); h.texts = append(h.texts, s); h.mu.Unlock() },
		OnEnd:  func(_ string, _ *pb.Offer, r Result) { h.ends <- r },
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.stop = cancel
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); cli.Close(); srv.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := srv.Accept(ctx)
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); h.serve(ctx, conn, &wg) }()
		}
	}()
	return h
}

func (h *harness) serve(ctx context.Context, conn *quic.Conn, wg *sync.WaitGroup) {
	for {
		str, err := conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			var env pb.Envelope
			if err := wire.Read(str, &env); err != nil {
				return
			}
			switch m := env.Msg.(type) {
			case *pb.Envelope_Offer:
				h.recv.HandleOffer(ctx, testPeer, str, m.Offer)
			case *pb.Envelope_FileData:
				h.recv.HandleFileData(testPeer, str, m.FileData)
			}
		}()
	}
}

func (h *harness) dial() *quic.Conn {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := h.client.DialMember(ctx, h.addr)
	if err != nil {
		h.t.Fatal(err)
	}
	return conn
}

func (h *harness) end() End { h.t.Helper(); return h.result().End }

func (h *harness) result() Result {
	h.t.Helper()
	select {
	case r := <-h.ends:
		return r
	case <-time.After(10 * time.Second):
		h.t.Fatal("receiver did not finish")
		return Result{}
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i/1000)
	}
	return b
}

func sameFile(t *testing.T, a, b string) {
	t.Helper()
	x, err1 := os.ReadFile(a)
	y, err2 := os.ReadFile(b)
	if err1 != nil || err2 != nil || !bytes.Equal(x, y) {
		t.Fatalf("%s and %s differ (%v %v)", a, b, err1, err2)
	}
}

func TestSendFolderFilesAndText(t *testing.T) {
	h := newHarness(t, nil)
	src := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(src, "项目", "sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(src, "项目", "big.bin"), randomBytes(5*chunkSize/2), 0o644))
	must(t, os.WriteFile(filepath.Join(src, "项目", "sub", "empty"), nil, 0o644))
	for i := 0; i < 6; i++ {
		must(t, os.WriteFile(filepath.Join(src, "项目", "sub", string(rune('a'+i))+".txt"), []byte{byte(i)}, 0o644))
	}
	var p Plan
	must(t, p.AddPath(filepath.Join(src, "项目")))
	must(t, p.AddText("你好，Portide", time.Now()))
	o, err := p.Offer(NewTransferID())
	must(t, err)

	var progress atomic.Int64
	failures, err := Send(context.Background(), h.dial(), o, p.Sources, func(n int64) { progress.Add(n) })
	if err != nil || len(failures) != 0 {
		t.Fatalf("Send: %v %v", failures, err)
	}
	if e := h.end(); e != EndComplete {
		t.Fatalf("end = %v", e)
	}
	if progress.Load() != p.Total() {
		t.Errorf("progress %d, total %d", progress.Load(), p.Total())
	}
	sameFile(t, filepath.Join(src, "项目", "big.bin"), filepath.Join(h.save, "项目", "big.bin"))
	sameFile(t, filepath.Join(src, "项目", "sub", "f.txt"), filepath.Join(h.save, "项目", "sub", "f.txt"))
	if fi, err := os.Stat(filepath.Join(h.save, "项目", "sub", "empty")); err != nil || fi.Size() != 0 {
		t.Fatalf("empty file: %v", err)
	}
	if len(h.texts) != 1 || h.texts[0] != "你好，Portide" {
		t.Errorf("texts = %v", h.texts)
	}
}

func TestSendResumesAfterConnectionLoss(t *testing.T) {
	h := newHarness(t, nil)
	src := filepath.Join(t.TempDir(), "movie.bin")
	must(t, os.WriteFile(src, randomBytes(12*chunkSize), 0o644))
	var p Plan
	must(t, p.AddPath(src))
	o, _ := p.Offer(NewTransferID())

	conn := h.dial()
	var sent atomic.Int64
	_, err := Send(context.Background(), conn, o, p.Sources, func(n int64) {
		if sent.Add(n) > 4*chunkSize {
			_ = conn.CloseWithError(0, "network gone") // 模拟网络中断
		}
	})
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if e := h.end(); e != EndInterrupted {
		t.Fatalf("end = %v", e)
	}
	parts, _ := os.ReadDir(filepath.Join(h.save, partDir))
	if len(parts) != 1 {
		t.Fatalf("parts = %v", parts)
	}
	info, _ := parts[0].Info()
	if info.Size() == 0 {
		t.Fatal("nothing received before the interruption")
	}

	var first int64 = -1
	var total atomic.Int64
	failures, err := Send(context.Background(), h.dial(), o, p.Sources, func(n int64) {
		if first < 0 {
			first = n
		}
		total.Add(n)
	})
	if err != nil || len(failures) != 0 {
		t.Fatalf("resume: %v %v", failures, err)
	}
	if first != info.Size() {
		t.Fatalf("resume started at %d, part had %d", first, info.Size())
	}
	if total.Load() != p.Total() {
		t.Errorf("progress %d, total %d", total.Load(), p.Total())
	}
	if e := h.end(); e != EndComplete {
		t.Fatalf("end = %v", e)
	}
	sameFile(t, src, filepath.Join(h.save, "movie.bin"))
}

func TestCancelRemovesPartialFiles(t *testing.T) {
	h := newHarness(t, nil)
	src := filepath.Join(t.TempDir(), "big.bin")
	must(t, os.WriteFile(src, randomBytes(12*chunkSize), 0o644))
	var p Plan
	must(t, p.AddPath(src))
	o, _ := p.Offer(NewTransferID())

	ctx, cancel := context.WithCancelCause(context.Background())
	var sent atomic.Int64
	_, err := Send(ctx, h.dial(), o, p.Sources, func(n int64) {
		if sent.Add(n) > 2*chunkSize {
			cancel(ErrUserCancel)
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if e := h.end(); e != EndCancelled {
		t.Fatalf("end = %v", e)
	}
	if _, err := os.Stat(filepath.Join(h.save, "big.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled file was kept")
	}
	if entries, _ := os.ReadDir(filepath.Join(h.save, partDir)); len(entries) != 0 {
		t.Fatalf("parts left: %v", entries)
	}
}

// ctx 取消但没有用户取消的原因（例如守护进程关闭）：按连接中断处理，接收方保留 .part 文件，
// 用同一报价重新发送时能从断点续传并完成。
func TestCtxCancelWithoutUserCauseIsTreatedAsInterrupted(t *testing.T) {
	h := newHarness(t, nil)
	src := filepath.Join(t.TempDir(), "big.bin")
	must(t, os.WriteFile(src, randomBytes(12*chunkSize), 0o644))
	var p Plan
	must(t, p.AddPath(src))
	o, _ := p.Offer(NewTransferID())

	ctx, cancel := context.WithCancel(context.Background())
	var sent atomic.Int64
	_, err := Send(ctx, h.dial(), o, p.Sources, func(n int64) {
		if sent.Add(n) > 2*chunkSize {
			cancel() // 没有带 ErrUserCancel 原因：不是用户取消
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if e := h.end(); e != EndInterrupted {
		t.Fatalf("end = %v", e)
	}
	parts, _ := os.ReadDir(filepath.Join(h.save, partDir))
	if len(parts) != 1 {
		t.Fatalf("part not kept: %v", parts)
	}
	info, _ := parts[0].Info()
	if info.Size() == 0 {
		t.Fatal("nothing received before the cancellation")
	}

	var first int64 = -1
	var total atomic.Int64
	failures, err := Send(context.Background(), h.dial(), o, p.Sources, func(n int64) {
		if first < 0 {
			first = n
		}
		total.Add(n)
	})
	if err != nil || len(failures) != 0 {
		t.Fatalf("resume: %v %v", failures, err)
	}
	if first != info.Size() {
		t.Fatalf("resume started at %d, part had %d", first, info.Size())
	}
	if total.Load() != p.Total() {
		t.Errorf("progress %d, total %d", total.Load(), p.Total())
	}
	if e := h.end(); e != EndComplete {
		t.Fatalf("end = %v", e)
	}
	sameFile(t, src, filepath.Join(h.save, "big.bin"))
}

func TestRejectedOffer(t *testing.T) {
	h := newHarness(t, func(context.Context) string { return ReasonDeclined })
	var p Plan
	must(t, p.AddText("hi", time.Now()))
	o, _ := p.Offer(NewTransferID())
	_, err := Send(context.Background(), h.dial(), o, p.Sources, func(int64) {})
	var rej *RejectedError
	if !errors.As(err, &rej) || rej.Reason != ReasonDeclined {
		t.Fatalf("err = %v", err)
	}
}

func TestChangedSourceFailsOnlyThatItem(t *testing.T) {
	h := newHarness(t, nil)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	must(t, os.WriteFile(a, []byte("aaa"), 0o644))
	must(t, os.WriteFile(b, []byte("bbb"), 0o644))
	var p Plan
	must(t, p.AddPath(a))
	must(t, p.AddPath(b))
	o, _ := p.Offer(NewTransferID())
	must(t, os.WriteFile(b, []byte("changed!"), 0o644)) // 报价之后被修改

	failures, err := Send(context.Background(), h.dial(), o, p.Sources, func(int64) {})
	if err != nil || len(failures) != 1 || failures[0].Path != "b.txt" || failures[0].Err != "source file changed" {
		t.Fatalf("failures %v err %v", failures, err)
	}
	r := h.result()
	if r.End != EndPartial || r.SaveDir != h.save || len(r.Saved) != 1 || r.Saved[0] != (SavedFile{Path: "a.txt", Size: 3}) {
		t.Fatalf("result = %+v", r)
	}
	sameFile(t, a, filepath.Join(h.save, "a.txt"))
}

// 接收方在询问用户时，发送方取消：接收方的 Decide 应立刻收到取消，不必等到超时。
func TestSenderCancelStopsReceiverDecision(t *testing.T) {
	deciding := make(chan struct{})
	stopped := make(chan struct{})
	h := newHarness(t, func(ctx context.Context) string {
		close(deciding)
		<-ctx.Done()
		close(stopped)
		return ReasonError
	})
	var p Plan
	must(t, p.AddText("hi", time.Now()))
	o, _ := p.Offer(NewTransferID())
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-deciding; cancel() }()
	if _, err := Send(ctx, h.dial(), o, p.Sources, func(int64) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver kept deciding after the sender cancelled")
	}
}

// 接收方守护进程在询问用户期间退出：发送方得到的是"连接中断"（之后会重试），而不是"对方出错"。
func TestReceiverShutdownDuringDecisionIsInterruption(t *testing.T) {
	deciding := make(chan struct{})
	h := newHarness(t, func(ctx context.Context) string {
		close(deciding)
		<-ctx.Done()
		return ReasonError
	})
	var p Plan
	must(t, p.AddText("hi", time.Now()))
	o, _ := p.Offer(NewTransferID())
	go func() { <-deciding; h.stop() }()
	if _, err := Send(context.Background(), h.dial(), o, p.Sources, func(int64) {}); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
}

// 续传在等待 Decide 返回期间（可能是在询问用户，也可能只是慢）还没登记进 r.active，但也不是
// "孤儿"：周期性清理不能把它的续传记录和 .part 文件当成过期数据删掉，否则这次续传结束后的
// Rename 会找不到记录而失败。
func TestRemoveStaleSkipsReceiveWaitingInDecide(t *testing.T) {
	proceed := make(chan struct{})
	var closeOnce sync.Once
	entered := make(chan struct{})
	var calls atomic.Int32
	h := newHarness(t, func(ctx context.Context) string {
		// 第一次调用是建立初始（会被打断）的接收，必须立即放行；第二次才是续传，这次卡住不返回
		// （模拟仍在询问用户，或者只是慢），直到测试放行。
		if calls.Add(1) == 2 {
			close(entered)
			<-proceed
		}
		return ""
	})
	// 测试提前 Fatal 时也别让 Decide 卡住，拖累 harness 自己的清理（它会等所有后台 goroutine 退出）：
	// 这个 Cleanup 必须在 newHarness 注册的 Cleanup 之后登记，LIFO 顺序下才会先于它运行。
	t.Cleanup(func() { closeOnce.Do(func() { close(proceed) }) })
	src := filepath.Join(t.TempDir(), "old.bin")
	must(t, os.WriteFile(src, randomBytes(3*chunkSize), 0o644))
	var p Plan
	must(t, p.AddPath(src))
	o, _ := p.Offer(NewTransferID())

	// 先建立一条中断的接收，制造一份将被判定为"过期"的续传记录。
	conn := h.dial()
	var sent atomic.Int64
	_, err := Send(context.Background(), conn, o, p.Sources, func(n int64) {
		if sent.Add(n) > chunkSize {
			_ = conn.CloseWithError(0, "network gone")
		}
	})
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if e := h.end(); e != EndInterrupted {
		t.Fatalf("end = %v", e)
	}

	// 对方重新连上续传：这次 Decide 卡住不返回（例如在询问用户）。
	resumeDone := make(chan error, 1)
	go func() {
		_, err := Send(context.Background(), h.dial(), o, p.Sources, func(int64) {})
		resumeDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("decide not entered")
	}

	key := store.IncomingKey{PeerKey: testPeer, TransferID: hex.EncodeToString(o.TransferId)}
	if !h.recv.InUse(key) {
		t.Fatal("InUse = false while a resume is waiting in Decide")
	}
	if err := RemoveStale(h.save, h.recv.Store, time.Now().Add(time.Hour), h.recv.InUse); err != nil {
		t.Fatal(err)
	}
	if items, _ := h.recv.Store.IncomingItems(key); len(items) == 0 {
		t.Fatal("stale journal removed while a resume was waiting in Decide")
	}
	if _, err := os.Stat(filepath.Join(h.save, partName(key, 0))); err != nil {
		t.Fatalf(".part removed while a resume was waiting in Decide: %v", err)
	}

	closeOnce.Do(func() { close(proceed) })
	if err := <-resumeDone; err != nil {
		t.Fatalf("resume: %v", err)
	}
	if e := h.end(); e != EndComplete {
		t.Fatalf("end = %v", e)
	}
}

// 一次正在进行中的接收（已经 Accept，在 r.active 里）同样不能被周期性清理删掉，即使它比
// 清理门槛更"老"。
func TestRemoveStaleSkipsActiveReceive(t *testing.T) {
	h := newHarness(t, nil)
	src := filepath.Join(t.TempDir(), "big.bin")
	must(t, os.WriteFile(src, randomBytes(6*chunkSize), 0o644))
	var p Plan
	must(t, p.AddPath(src))
	o, _ := p.Offer(NewTransferID())

	gate := make(chan struct{})
	var closeOnce sync.Once
	t.Cleanup(func() { closeOnce.Do(func() { close(gate) }) }) // 测试提前 Fatal 时也别让发送方卡住，拖累 harness 的清理
	first := true
	done := make(chan error, 1)
	go func() {
		_, err := Send(context.Background(), h.dial(), o, p.Sources, func(int64) {
			if first {
				first = false
				<-gate
			}
		})
		done <- err
	}()

	// 等待续传记录真正写进数据库（在 Accept 内部完成，这时登记进 r.active 也已经做完了，见 HandleOffer）。
	key := store.IncomingKey{PeerKey: testPeer, TransferID: hex.EncodeToString(o.TransferId)}
	deadline := time.Now().Add(5 * time.Second)
	for {
		items, _ := h.recv.Store.IncomingItems(key)
		if len(items) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("receive never became active")
		}
		time.Sleep(time.Millisecond)
	}
	if !h.recv.InUse(key) {
		t.Fatal("InUse = false for an active receive")
	}

	if err := RemoveStale(h.save, h.recv.Store, time.Now().Add(time.Hour), h.recv.InUse); err != nil {
		t.Fatal(err)
	}
	if items, _ := h.recv.Store.IncomingItems(key); len(items) == 0 {
		t.Fatal("stale journal removed while the receive was active")
	}

	closeOnce.Do(func() { close(gate) })
	if err := <-done; err != nil {
		t.Fatalf("send: %v", err)
	}
	if e := h.end(); e != EndComplete {
		t.Fatalf("end = %v", e)
	}
}

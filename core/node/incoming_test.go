package node

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"portide/core/relayserver"
	"portide/core/store"
)

// slowPair 让两台设备只能经限速的中继连接（512 KiB/s），几 MiB 的文件要传好几秒，测试能看到进行中的状态。
func slowPair(t *testing.T) (laptop, desktop *testDevice) {
	t.Helper()
	cfg := startTestRelayWith(t, relayserver.Limits{SessionRate: 512 << 10})
	return relayedPair(t, cfg, noDirect)
}

// 正在接收的传输出现在 IncomingTransfers 里，进度递增，期间定时发出 transfers 信号；收完之后消失。
func TestIncomingTransfersShowProgress(t *testing.T) {
	laptop, desktop := slowPair(t)
	rec := &topicRecorder{}
	desktop.n.opts.OnChange = rec.record

	content := bytes.Repeat([]byte("接收进度"), (2<<20)/len("接收进度")) // 约 2 MiB
	id, err := laptop.n.QueueSend(desktop.n.id.KeyID, plan(t, "", map[string]string{"进度.bin": string(content)}))
	if err != nil {
		t.Fatal(err)
	}
	var first IncomingTransfer
	eventually(t, "receive in progress", func() bool {
		in := desktop.n.IncomingTransfers()
		if len(in) == 1 && in[0].Done > 0 {
			first = in[0]
			return true
		}
		return false
	})
	if first.PeerKey != laptop.n.id.KeyID || first.PeerName != "笔记本" || first.Total != int64(len(content)) ||
		first.Summary == "" || len(first.TransferID) != 32 || first.Started.IsZero() || first.Done >= first.Total {
		t.Fatalf("incoming = %+v", first)
	}
	eventually(t, "progress grows", func() bool {
		in := desktop.n.IncomingTransfers()
		return len(in) == 0 || in[0].Done > first.Done
	})
	if !rec.seen(store.ChangeTransfers) {
		t.Fatal("receiving must signal transfers")
	}
	waitStatus(t, laptop, id, store.SendDone)
	eventually(t, "finished receive disappears", func() bool { return len(desktop.n.IncomingTransfers()) == 0 })
	if got := received(t, desktop, "进度.bin"); got != string(content) {
		t.Fatalf("received %d bytes, want %d", len(got), len(content))
	}
}

// 守护进程没有运行时没有接收（不会碰到空指针）。
func TestIncomingTransfersWithoutDaemon(t *testing.T) {
	d := newDevice(t, "笔记本")
	if in := d.n.IncomingTransfers(); len(in) != 0 {
		t.Fatalf("incoming = %+v", in)
	}
}

// 停止接收：临时文件删除、本机历史记为"已停止接收"；发送方重试时被拒绝，任务以"对方停止了接收"结束，不再重试。
func TestStopIncomingEndsTheSendersTask(t *testing.T) {
	laptop, desktop := slowPair(t)
	content := bytes.Repeat([]byte("停止接收"), (2<<20)/len("停止接收"))
	id, err := laptop.n.QueueSend(desktop.n.id.KeyID, plan(t, "", map[string]string{"停止.bin": string(content)}))
	if err != nil {
		t.Fatal(err)
	}
	var in IncomingTransfer
	eventually(t, "receive in progress", func() bool {
		list := desktop.n.IncomingTransfers()
		if len(list) == 1 && list[0].Done > 0 {
			in = list[0]
			return true
		}
		return false
	})
	if err := desktop.n.StopIncoming(in.PeerKey, in.TransferID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stopped receive disappears", func() bool { return len(desktop.n.IncomingTransfers()) == 0 })

	o := waitStatus(t, laptop, id, store.SendFailed)
	if o.Error != "对方停止了接收" {
		t.Fatalf("sender error = %q", o.Error)
	}
	dir, _ := desktop.n.SaveDir()
	if parts, _ := filepath.Glob(filepath.Join(dir, ".portide-parts", "*.part")); len(parts) != 0 {
		t.Fatalf("partial files left: %v", parts)
	}
	if _, err := os.Stat(filepath.Join(dir, "停止.bin")); err == nil {
		t.Fatal("a stopped file must not be saved")
	}
	h, err := desktop.n.History("", 10)
	if err != nil || len(h) != 1 || h[0].Status != store.MessageCancelled || h[0].Body.Error != "已停止接收" {
		t.Fatalf("receiver history = %+v, %v", h, err)
	}
	if err := desktop.n.StopIncoming(in.PeerKey, in.TransferID); !errors.Is(err, ErrNoSuchIncoming) {
		t.Fatalf("stopping again: err = %v", err)
	}
	if err := desktop.n.StopIncoming(in.PeerKey, "zz"); !errors.Is(err, ErrNoSuchIncoming) {
		t.Fatalf("bad transfer id: err = %v", err)
	}
}

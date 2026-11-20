package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/store"
	"portide/core/transfer"
)

// pairOnline 让两台设备组成设备圈并互相在线。logs 不为空时记录接收方（台式机）的日志。
func pairOnline(t *testing.T, logs *logRecorder) (laptop, desktop *testDevice) {
	t.Helper()
	laptop, desktop = newDevice(t, "笔记本"), newDevice(t, "台式机")
	if logs != nil {
		desktop.n.opts.Logf = logs.Logf
	}
	pairDevices(t, laptop, desktop)
	return laptop, desktop
}

// pairDevices 让两台还没启动的设备配对并都上线。
func pairDevices(t *testing.T, laptop, desktop *testDevice) {
	t.Helper()
	laptop.start(t)
	code, sid := invite(t, laptop)
	if _, err := desktop.n.Join(context.Background(), code, []string{laptop.n.Addr()}, false); err != nil {
		t.Fatalf("Join: %v", err)
	}
	eventually(t, "pair session done", func() bool {
		ps, _ := laptop.n.Store().GetPairSession(sid)
		return ps.Status == store.PairDone
	})
	desktop.start(t)
	eventually(t, "both online", func() bool {
		return online(t, laptop, desktop.n.Identity().KeyID) && online(t, desktop, laptop.n.Identity().KeyID)
	})
}

// liveConn 返回 from 到 to 当前保留的成员连接。
func liveConn(t *testing.T, from, to *testDevice) *quic.Conn {
	t.Helper()
	var mc *memberConn
	eventually(t, "member link", func() bool { mc = connPtr(from, to.n.Identity().KeyID); return mc != nil })
	return mc.conn
}

func plan(t *testing.T, text string, files map[string]string) *transfer.Plan {
	t.Helper()
	dir := t.TempDir()
	var p transfer.Plan
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := p.AddPath(path); err != nil {
			t.Fatal(err)
		}
	}
	if text != "" {
		if err := p.AddText(text, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return &p
}

func send(t *testing.T, conn *quic.Conn, p *transfer.Plan) ([]transfer.ItemError, error) {
	t.Helper()
	o, err := p.Offer(transfer.NewTransferID())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return transfer.Send(ctx, conn, o, p.Sources, func(int64) {})
}

func received(t *testing.T, d *testDevice, name string) string {
	t.Helper()
	dir, _ := d.n.SaveDir()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("not received: %v", err)
	}
	return string(b)
}

func TestParseReceivePolicy(t *testing.T) {
	for in, want := range map[string]ReceivePolicy{
		"":                {Mode: PolicyAuto},
		"auto":            {Mode: PolicyAuto},
		"ask":             {Mode: PolicyAsk},
		"ask-above:10485": {Mode: PolicyAskAbove, Above: 10485},
	} {
		got, err := ParseReceivePolicy(in)
		if err != nil || got != want {
			t.Errorf("ParseReceivePolicy(%q) = %+v, %v", in, got, err)
		}
		if in != "" && got.String() != in {
			t.Errorf("String() = %q, want %q", got.String(), in)
		}
	}
	for _, bad := range []string{"always", "ask-above:", "ask-above:-1", "ask-above:x"} {
		if _, err := ParseReceivePolicy(bad); err == nil {
			t.Errorf("ParseReceivePolicy(%q) accepted", bad)
		}
	}
	if !(ReceivePolicy{Mode: PolicyAskAbove, Above: 5}).needsAsk(6) || (ReceivePolicy{Mode: PolicyAskAbove, Above: 5}).needsAsk(5) {
		t.Error("ask-above threshold")
	}
}

func TestDaemonReceivesAutomatically(t *testing.T) {
	logs := &logRecorder{}
	laptop, desktop := pairOnline(t, logs)
	failures, err := send(t, liveConn(t, laptop, desktop), plan(t, "会议室 3 楼", map[string]string{"笔记.md": "# 待办"}))
	if err != nil || len(failures) != 0 {
		t.Fatalf("send: %v %v", failures, err)
	}
	if got := received(t, desktop, "笔记.md"); got != "# 待办" {
		t.Fatalf("content = %q", got)
	}
	eventually(t, "receiver logs the text and the result", func() bool {
		return logs.contains("收到「笔记本」的一段文本（7 字），用 portide history 查看") && logs.contains("已收到「笔记本」发来的 笔记.md")
	})
	if logs.contains("会议室 3 楼") {
		t.Fatal("received text content must not appear in the log")
	}
}

func TestDaemonAsksAboveThreshold(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	if err := desktop.n.SetReceivePolicy(ReceivePolicy{Mode: PolicyAskAbove, Above: 3}); err != nil {
		t.Fatal(err)
	}
	conn := liveConn(t, laptop, desktop)

	// 文本没有字节数，不超过阈值：直接接收。
	if _, err := send(t, conn, plan(t, "hi", nil)); err != nil {
		t.Fatalf("small offer: %v", err)
	}

	ask := func(decide bool) error {
		errc := make(chan error, 1)
		go func() {
			_, err := send(t, conn, plan(t, "", map[string]string{"big.txt": "0123456789"}))
			errc <- err
		}()
		var entries []store.InboxEntry
		eventually(t, "offer waits in the inbox", func() bool {
			entries, _ = desktop.n.Store().PendingInbox(time.Now())
			return len(entries) == 1
		})
		if e := entries[0]; e.PeerName != "笔记本" || !strings.Contains(e.Summary, "big.txt") || e.Total != 10 {
			t.Fatalf("inbox entry = %+v", e)
		}
		if err := desktop.n.Store().DecideInbox(entries[0].ID, decide); err != nil {
			t.Fatal(err)
		}
		err := <-errc
		if _, gerr := desktop.n.Store().GetInbox(entries[0].ID); gerr == nil {
			t.Fatal("inbox entry kept after the decision")
		}
		return err
	}
	var rej *transfer.RejectedError
	if err := ask(false); !errors.As(err, &rej) || rej.Reason != transfer.ReasonDeclined {
		t.Fatalf("declined offer: %v", err)
	}
	if err := ask(true); err != nil {
		t.Fatalf("accepted offer: %v", err)
	}
	if got := received(t, desktop, "big.txt"); got != "0123456789" {
		t.Fatalf("content = %q", got)
	}
}

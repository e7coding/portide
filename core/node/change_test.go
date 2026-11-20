package node

import (
	"slices"
	"sync"
	"testing"
	"time"

	"portide/core/store"
)

// topicRecorder 收集 OnChange 收到的主题（守护进程的多个 goroutine 都会调用它）。
type topicRecorder struct {
	mu     sync.Mutex
	topics []string
}

func (r *topicRecorder) record(topic string) {
	r.mu.Lock()
	r.topics = append(r.topics, topic)
	r.mu.Unlock()
}

func (r *topicRecorder) seen(topic string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.topics, topic)
}

// Options.OnChange 接到了数据库上：对方连上来时收到 devices，收到文本后收到 history。
func TestOnChangeReportsDaemonWrites(t *testing.T) {
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	laptop.start(t)
	joinCircle(t, laptop, desktop)

	rec := &topicRecorder{}
	desktop.n.Close()
	n, err := Open(Options{
		DataDir: desktop.dir, Keys: desktop.keys, ListenAddr: "127.0.0.1:0", DisableMDNS: true,
		PollInterval: 50 * time.Millisecond, Logf: t.Logf, OnChange: rec.record,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	desktop.n = n
	desktop.start(t)

	eventually(t, "devices signalled", func() bool { return rec.seen(store.ChangeDevices) })
	id, err := laptop.n.QueueSend(desktop.n.id.KeyID, plan(t, "你好", nil))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendDone)
	eventually(t, "history signalled", func() bool { return rec.seen(store.ChangeHistory) })
}

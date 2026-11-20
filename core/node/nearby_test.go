package node

import (
	"context"
	"testing"
	"time"
)

// 关闭了局域网发现的节点不会去浏览：立即返回 false，不等 4 秒的浏览时长。
func TestNearbyInvitersFalseWhenMDNSDisabled(t *testing.T) {
	d := newDevice(t, "笔记本")
	d.n.opts.DisableMDNS = true
	start := time.Now()
	if d.n.NearbyInviters(context.Background()) {
		t.Fatal("NearbyInviters = true with mDNS disabled")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("NearbyInviters took %v, want an immediate return", took)
	}
}

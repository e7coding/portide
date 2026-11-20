package app

import (
	"encoding/json"
	"fmt"
	"testing"

	"portide/core/chain"
	"portide/core/node"
	"portide/core/store"
)

// 历史里存的是当时的设备名；对方改名之后，时间线和搜索结果显示它现在的名字。已经不在设备圈里的设备照旧显示历史里的名字。
func TestHistoryShowsCurrentNames(t *testing.T) {
	laptop, desktop := pairedServices(t)
	n, err := laptop.node()
	if err != nil {
		t.Fatal(err)
	}
	add := func(peerKey, peerName, text string) int64 {
		b, _ := json.Marshal(node.HistoryBody{PeerName: peerName, Texts: []string{text}})
		id, err := n.Store().AddMessage(store.Message{Direction: store.MessageIn, PeerKey: peerKey, Status: store.MessageDone, Body: b})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	member := add(keyOf(t, desktop), "旧名字", "报告在这里")
	former := add("GONE", "被移除的平板", "报告的旧版本")

	items, err := laptop.Timeline("", 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, it := range items {
		if it.Kind == KindMessage {
			got[it.ID] = it.PeerName
		}
	}
	if got[member] != "台式机" || got[former] != "被移除的平板" {
		t.Fatalf("timeline names = %v", got)
	}
	results, err := laptop.Search("报告")
	if err != nil {
		t.Fatal(err)
	}
	found := map[int64]string{}
	for _, r := range results {
		found[r.HistoryID] = r.PeerName
	}
	if found[member] != "台式机" || found[former] != "被移除的平板" {
		t.Fatalf("search names = %v", found)
	}
}

func TestRenameLimitMessage(t *testing.T) {
	err := uiError(fmt.Errorf("node: %w", chain.ErrTooManyRenames))
	if err == nil || err.Error() != "改名次数已达上限（一个设备圈最多改名 1000 次）" {
		t.Fatalf("uiError = %v", err)
	}
}

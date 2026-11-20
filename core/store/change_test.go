package store

import (
	"reflect"
	"testing"
	"time"
)

// 每一类界面关心的写操作都要发出对应的变化信号；守护进程心跳这类频繁写入的元数据不发。
func TestWritesSignalTheirTopic(t *testing.T) {
	s := open(t)
	var got []string
	s.SetOnChange(func(topic string) { got = append(got, topic) })
	expect := func(what string, want ...string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: topics = %q, want %q", what, got, want)
		}
		got = nil
	}

	if _, err := s.AddMessage(Message{Direction: "out", PeerKey: "PEER", Status: "done", Body: []byte("hi"), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	expect("AddMessage", ChangeHistory)
	if _, err := s.ClearMessages(); err != nil {
		t.Fatal(err)
	}
	expect("ClearMessages", ChangeHistory)

	if err := s.UpsertPeer(Peer{KeyID: "PEER", Online: true}); err != nil {
		t.Fatal(err)
	}
	expect("UpsertPeer", ChangeDevices)
	if err := s.SetAllOffline(); err != nil {
		t.Fatal(err)
	}
	expect("SetAllOffline", ChangeDevices)
	// 签名链变了，设备列表（成员、名字）也跟着变。存储层不解析记录，随便几个字节就行。
	if err := s.SaveChain([][]byte{{1}}); err != nil {
		t.Fatal(err)
	}
	expect("SaveChain", ChangeDevices)
	if err := s.AppendChain(1, [][]byte{{1}, {2}}); err != nil {
		t.Fatal(err)
	}
	expect("AppendChain", ChangeDevices)
	if err := s.ReplaceChain([][]byte{{1}, {2}}, [][]byte{{1}, {3}}); err != nil {
		t.Fatal(err)
	}
	expect("ReplaceChain", ChangeDevices)
	// 声明的功能决定能不能改名：变了才发信号，每次握手重复写同样的值不发。
	if err := s.SetPeerFeatures("PEER", []string{"rename"}); err != nil {
		t.Fatal(err)
	}
	expect("SetPeerFeatures", ChangeDevices)
	if err := s.SetPeerFeatures("PEER", []string{"rename"}); err != nil {
		t.Fatal(err)
	}
	expect("SetPeerFeatures unchanged")

	id, err := s.CreateOutgoing("PEER", []byte{1}, []byte("offer"), []byte("[]"), 10)
	if err != nil {
		t.Fatal(err)
	}
	expect("CreateOutgoing", ChangeTransfers)
	if err := s.SetOutgoingProgress(id, 5); err != nil {
		t.Fatal(err)
	}
	expect("SetOutgoingProgress", ChangeTransfers)
	if err := s.CancelOutgoing(id); err != nil {
		t.Fatal(err)
	}
	expect("CancelOutgoing", ChangeTransfers)

	inbox, err := s.CreateInbox("手机", "a.jpg", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expect("CreateInbox", ChangeInbox)
	if err := s.DecideInbox(inbox, true); err != nil {
		t.Fatal(err)
	}
	expect("DecideInbox", ChangeInbox)

	pair, err := s.CreatePairSession("11112222", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expect("CreatePairSession", ChangePairing)
	if err := s.FinishPairSession(pair, PairDone, "手机", ""); err != nil {
		t.Fatal(err)
	}
	expect("FinishPairSession", ChangePairing)

	for key, topic := range map[string]string{MetaRelayStatus: ChangeRelay, MetaSaveDir: ChangeSettings, MetaReceivePolicy: ChangeSettings, MetaHistoryDays: ChangeSettings, MetaQueueDays: ChangeSettings} {
		if err := s.SetMeta(key, "x"); err != nil {
			t.Fatal(err)
		}
		expect("SetMeta "+key, topic)
	}
	if err := s.SetSecretMeta(MetaRelayConfig, "x"); err != nil {
		t.Fatal(err)
	}
	expect("SetSecretMeta relay config", ChangeRelay)
	if err := s.DeleteMeta(MetaRelayConfig); err != nil {
		t.Fatal(err)
	}
	expect("DeleteMeta relay config", ChangeRelay)

	if err := s.SetMeta(MetaDaemonHeartbeat, "1"); err != nil {
		t.Fatal(err)
	}
	expect("heartbeat")
}

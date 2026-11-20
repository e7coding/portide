package transfer

import (
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"portide/core/pb"
	"portide/core/store"
)

// FuzzOffer 覆盖面向对端的报价处理管线（设计方案 11）：任意字节 → 反序列化 → CheckOffer → Accept。
// 重点检查 Accept 解析出的落盘路径始终安全：留在保存目录内，彼此不冲突（忽略大小写）。
//
// 每次执行都新建一个带数据库的保存目录开销很大（fuzzing 时每秒执行上万次），所以这里的续传记录
// 用一个在整个 fuzz 进程生命周期内共享的 Store；每次迭代仍然用全新的保存目录。
func FuzzOffer(f *testing.F) {
	st, err := store.Open(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { st.Close() })

	id := NewTransferID()
	seed := func(items ...*pb.Item) []byte {
		b, _ := proto.Marshal(&pb.Offer{TransferId: id, Items: items})
		return b
	}
	f.Add(seed(&pb.Item{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: "a.txt", Size: 3}))
	f.Add(seed(
		&pb.Item{Kind: pb.ItemKind_ITEM_KIND_DIR, Path: "照片"},
		&pb.Item{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: "照片/猫.jpg", Size: 3},
	))
	f.Add(seed(&pb.Item{Kind: pb.ItemKind_ITEM_KIND_TEXT, Text: "你好"}))
	f.Add(seed(
		&pb.Item{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: "A.txt", Size: 1},
		&pb.Item{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: "a.txt", Size: 1},
	))
	f.Add(seed(&pb.Item{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: "../x", Size: 1}))

	f.Fuzz(func(t *testing.T, data []byte) {
		var o pb.Offer
		if err := proto.Unmarshal(data, &o); err != nil {
			t.Skip()
		}
		if err := CheckOffer(&o); err != nil {
			t.Skip()
		}
		save := t.TempDir()
		in, _, err := Accept(&o, AcceptOptions{SaveDir: save, Store: st, Peer: testPeer})
		if err != nil {
			t.Skip() // ErrNoSpace、ErrBadOffer 等：本测试只关心不能 panic、不能逃出保存目录、不能冲突
		}
		seen := map[string]bool{}
		for i, it := range o.Items {
			if it.Kind == pb.ItemKind_ITEM_KIND_TEXT {
				continue
			}
			p := in.paths[i]
			if !filepath.IsLocal(filepath.FromSlash(p)) {
				t.Fatalf("resolved path escapes the save dir: %q", p)
			}
			k := foldKey(p)
			if seen[k] {
				t.Fatalf("duplicate resolved path: %q", p)
			}
			seen[k] = true
		}
		in.Finish(true)
	})
}

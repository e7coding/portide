package transfer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"portide/core/pb"
)

func TestNewTransferIDIsV7(t *testing.T) {
	a, b := NewTransferID(), NewTransferID()
	if len(a) != 16 || a[6]>>4 != 7 || a[8]>>6 != 2 || string(a) == string(b) {
		t.Fatalf("bad ids %x %x", a, b)
	}
}

func TestPlanCollectsFolderAndSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "照片")
	must(t, os.MkdirAll(filepath.Join(root, "2026", "空"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "2026", "猫.jpg"), []byte("meow"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644))
	if err := os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}

	var p Plan
	must(t, p.AddPath(root))
	must(t, p.AddPath(filepath.Join(dir, "a.txt")))
	got := map[string]pb.ItemKind{}
	for i, it := range p.Items {
		got[it.Path] = it.Kind
		if it.Kind == pb.ItemKind_ITEM_KIND_FILE && p.Sources[i].Path == "" {
			t.Errorf("%s has no source", it.Path)
		}
	}
	want := map[string]pb.ItemKind{
		"照片": pb.ItemKind_ITEM_KIND_DIR, "照片/2026": pb.ItemKind_ITEM_KIND_DIR, "照片/2026/空": pb.ItemKind_ITEM_KIND_DIR,
		"照片/2026/猫.jpg": pb.ItemKind_ITEM_KIND_FILE, "a.txt": pb.ItemKind_ITEM_KIND_FILE,
	}
	if len(got) != len(want) {
		t.Fatalf("items = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: kind %v, want %v", k, got[k], v)
		}
	}
	if len(p.Skipped) != 1 || !strings.HasSuffix(p.Skipped[0], "link") {
		t.Errorf("skipped = %v", p.Skipped)
	}
	if p.Total() != 9 {
		t.Errorf("total = %d", p.Total())
	}
	o, err := p.Offer(NewTransferID())
	if err != nil || CheckOffer(o) != nil {
		t.Fatalf("offer: %v / %v", err, CheckOffer(o))
	}
	if d, _ := Describe(o); d != "照片 等 2 项（2 个文件，9 B）" {
		t.Errorf("Describe = %q", d)
	}
}

func TestPlanText(t *testing.T) {
	var p Plan
	must(t, p.AddText("你好", time.Now()))
	must(t, p.AddText(strings.Repeat("x", MaxTextBytes+1), time.Date(2026, 10, 23, 9, 15, 0, 0, time.Local)))
	if p.Items[0].Kind != pb.ItemKind_ITEM_KIND_TEXT || p.Items[0].Text != "你好" {
		t.Errorf("short text item = %v", p.Items[0])
	}
	if it := p.Items[1]; it.Kind != pb.ItemKind_ITEM_KIND_FILE || it.Path != "文本-20261023-091500.txt" || len(p.Sources[1].Data) != MaxTextBytes+1 {
		t.Errorf("long text item = %v", it)
	}
	o, _ := (&Plan{Items: p.Items[:1]}).Offer(NewTransferID())
	if d, _ := Describe(o); d != "文本「你好」" {
		t.Errorf("Describe = %q", d)
	}
}

func TestPlanLimit(t *testing.T) {
	p := Plan{Items: make([]*pb.Item, MaxItems), Sources: make([]Source, MaxItems)}
	if err := p.AddText("x", time.Now()); !errors.Is(err, ErrTooManyItems) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckOffer(t *testing.T) {
	id := NewTransferID()
	file := func(p string) *pb.Item { return &pb.Item{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: p, Size: 1} }
	good := &pb.Offer{TransferId: id, Items: []*pb.Item{file("a.txt"), {Kind: pb.ItemKind_ITEM_KIND_TEXT, Text: "hi"}}}
	if err := CheckOffer(good); err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]*pb.Offer{
		"short id":     {TransferId: id[:8], Items: []*pb.Item{file("a")}},
		"no items":     {TransferId: id},
		"traversal":    {TransferId: id, Items: []*pb.Item{file("../a")}},
		"absolute":     {TransferId: id, Items: []*pb.Item{file("/etc/passwd")}},
		"unknown kind": {TransferId: id, Items: []*pb.Item{{Kind: 9, Path: "a"}}},
		"sized dir":    {TransferId: id, Items: []*pb.Item{{Kind: pb.ItemKind_ITEM_KIND_DIR, Path: "d", Size: 5}}},
		"text w/ path": {TransferId: id, Items: []*pb.Item{{Kind: pb.ItemKind_ITEM_KIND_TEXT, Path: "a", Text: "x"}}},
		"long text":    {TransferId: id, Items: []*pb.Item{{Kind: pb.ItemKind_ITEM_KIND_TEXT, Text: strings.Repeat("x", MaxTextBytes+1)}}},
	} {
		if err := CheckOffer(o); !errors.Is(err, ErrBadOffer) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCheckOfferRejectsOversizedTotal(t *testing.T) {
	id := NewTransferID()
	big := &pb.Item{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: "a.bin", Size: 1 << 62}
	big2 := &pb.Item{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: "b.bin", Size: 1 << 62}
	o := &pb.Offer{TransferId: id, Items: []*pb.Item{big, big2}}
	if err := CheckOffer(o); !errors.Is(err, ErrBadOffer) {
		t.Fatalf("err = %v, want ErrBadOffer", err)
	}
}

func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KB", 5 << 30: "5.0 GB"} {
		if got := FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

package app

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"portide/core/node"
	"portide/core/store"
)

func writeImage(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	var err error
	if strings.HasSuffix(path, ".png") {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// decodeDataURL 解出缩略图，返回 MIME 类型和尺寸。
func decodeDataURL(t *testing.T, url string) (string, image.Point) {
	t.Helper()
	head, data, ok := strings.Cut(url, ";base64,")
	if !ok {
		t.Fatalf("not a data URL: %.40q", url)
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(head, "data:"), img.Bounds().Size()
}

func TestThumbnail(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "笔记本")
	dir := t.TempDir()
	writeImage(t, filepath.Join(dir, "宽.png"), 400, 200)
	writeImage(t, filepath.Join(dir, "高.jpg"), 90, 300)
	writeImage(t, filepath.Join(dir, "小.png"), 40, 30)
	_ = os.WriteFile(filepath.Join(dir, "说明.txt"), []byte("x"), 0o644)
	id := addEntry(t, s, store.MessageIn, node.HistoryBody{PeerName: "手机", Dir: dir, Files: []node.HistoryFile{
		{Path: "宽.png"}, {Path: "高.jpg"}, {Path: "小.png"}, {Path: "说明.txt"}, {Path: "不见了.png"},
	}})

	for i, c := range []struct {
		mime string
		size image.Point
	}{{"image/png", image.Pt(160, 80)}, {"image/jpeg", image.Pt(48, 160)}, {"image/png", image.Pt(40, 30)}} {
		url, err := s.Thumbnail(id, i)
		if err != nil {
			t.Fatal(err)
		}
		if mime, size := decodeDataURL(t, url); mime != c.mime || size != c.size {
			t.Errorf("thumbnail %d = %s %v, want %s %v", i, mime, size, c.mime, c.size)
		}
	}
	for _, i := range []int{3, 4, 9} { // 不是图片、文件不在了、序号越界：都返回空，不报错
		if url, err := s.Thumbnail(id, i); url != "" || err != nil {
			t.Errorf("thumbnail %d = %.20q %v, want empty", i, url, err)
		}
	}
	if s.thumbs.order.Len() != 3 {
		t.Fatalf("cache holds %d thumbnails, want 3", s.thumbs.order.Len())
	}
	first, _ := s.Thumbnail(id, 0)
	if again, _ := s.Thumbnail(id, 0); again != first || s.thumbs.order.Len() != 3 {
		t.Fatal("a second request must be served from the cache")
	}
}

func TestThumbCacheEvicts(t *testing.T) {
	var c thumbCache
	for i := range thumbCacheSize + 5 {
		c.put(string(rune('a'+i%26))+strings.Repeat("x", i), "u")
	}
	if c.order.Len() != thumbCacheSize || len(c.items) != thumbCacheSize {
		t.Fatalf("cache size = %d / %d", c.order.Len(), len(c.items))
	}
	if _, ok := c.get("a"); ok {
		t.Fatal("the oldest entry must be evicted")
	}
}

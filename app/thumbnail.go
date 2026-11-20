package app

import (
	"bytes"
	"container/list"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // 注册 GIF 解码器
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// 缩略图（M3c 设计 5.4）。
const (
	thumbEdge      = 160      // 缩略图的最长边（像素）
	thumbCacheSize = 200      // 内存里缓存最近多少张
	thumbMaxPixels = 50 << 20 // 原图超过这么多像素就不做（防止解码超大图片占满内存）
	thumbSamples   = 4        // 每个缩略图像素在原图里取 thumbSamples×thumbSamples 个点求平均
)

// Thumbnail 返回历史里一个图片文件的缩略图（data URL）。只支持 JPEG、PNG、GIF；
// 其他格式、文件已被移走或图片太大时返回空串，界面显示类型图标。
func (s *Service) Thumbnail(historyID int64, index int) (string, error) {
	path, err := s.filePath(historyID, index)
	if err != nil {
		return "", nil // 文件不在了：显示图标即可，不是错误
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".gif" {
		return "", nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", nil
	}
	key := fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().UnixNano())
	if url, ok := s.thumbs.get(key); ok {
		return url, nil
	}
	url := makeThumbnail(path)
	s.thumbs.put(key, url)
	return url, nil
}

// makeThumbnail 读出图片并缩小；失败时返回空串。PNG 和 GIF 输出 PNG（保留透明），JPEG 输出 JPEG。
func makeThumbnail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > thumbMaxPixels {
		return ""
	}
	if _, err := f.Seek(0, 0); err != nil {
		return ""
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return ""
	}
	dst := shrink(src, thumbEdge)
	var buf bytes.Buffer
	mime := "image/png"
	if format == "jpeg" {
		mime = "image/jpeg"
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82})
	} else {
		err = png.Encode(&buf, dst)
	}
	if err != nil {
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// shrink 把图片等比缩小到最长边不超过 edge（本来就小的不放大），每个像素取原图对应区域里若干点的平均。
func shrink(src image.Image, edge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	tw, th := w, h
	if w > edge || h > edge {
		if w >= h {
			tw, th = edge, max(1, h*edge/w)
		} else {
			tw, th = max(1, w*edge/h), edge
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, tw, th))
	for y := range th {
		for x := range tw {
			var r, g, bl, a uint32
			for sy := range thumbSamples {
				for sx := range thumbSamples {
					px := b.Min.X + (x*thumbSamples+sx)*w/(tw*thumbSamples)
					py := b.Min.Y + (y*thumbSamples+sy)*h/(th*thumbSamples)
					c := color.NRGBAModel.Convert(src.At(px, py)).(color.NRGBA)
					r, g, bl, a = r+uint32(c.R), g+uint32(c.G), bl+uint32(c.B), a+uint32(c.A)
				}
			}
			const n = thumbSamples * thumbSamples
			dst.SetNRGBA(x, y, color.NRGBA{R: uint8(r / n), G: uint8(g / n), B: uint8(bl / n), A: uint8(a / n)})
		}
	}
	return dst
}

// thumbCache 是缩略图的 LRU 缓存（键含文件大小和修改时间：文件被替换后重新生成）。
type thumbCache struct {
	mu    sync.Mutex
	order *list.List // 最近用过的在前，元素是 thumbEntry
	items map[string]*list.Element
}

type thumbEntry struct{ key, url string }

func (c *thumbCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(thumbEntry).url, true
	}
	return "", false
}

func (c *thumbCache) put(key, url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.order, c.items = list.New(), map[string]*list.Element{}
	}
	if e, ok := c.items[key]; ok {
		c.order.MoveToFront(e)
		return
	}
	c.items[key] = c.order.PushFront(thumbEntry{key, url})
	if c.order.Len() > thumbCacheSize {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(thumbEntry).key)
	}
}

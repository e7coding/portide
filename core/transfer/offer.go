package transfer

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"portide/core/i18n"
	"portide/core/pb"
	"portide/core/wire"
)

// 报价限制。
const (
	MaxItems     = 20000    // 一次传输最多的条目数
	MaxTextBytes = 64 << 10 // 放在报价里的文本上限；更长的文本按文件发送（设计方案 5.2）
)

var (
	// ErrTooManyItems 表示条目超过 MaxItems。
	ErrTooManyItems = fmt.Errorf("transfer: more than %d items", MaxItems)
	// ErrBadOffer 表示对端的报价不合规。
	ErrBadOffer = errors.New("transfer: malformed offer")
)

// NewTransferID 生成一个 UUID v7（RFC 9562）：前 48 位是毫秒时间戳，其余随机。
func NewTransferID() []byte {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (40 - 8*i))
	}
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	return b[:]
}

// Source 是发送方一个条目的内容来源：本机文件，或内存中的数据（超长文本）。文件夹和短文本没有来源。
type Source struct {
	Path string `json:"path,omitempty"`
	Data []byte `json:"data,omitempty"`
}

// Plan 是待发送的条目清单，Items 与 Sources 一一对应。
type Plan struct {
	Items   []*pb.Item
	Sources []Source
	Skipped []string // 无法发送而跳过的路径（符号链接、设备文件、读不了的目录）
}

func (p *Plan) add(it *pb.Item, src Source) error {
	if len(p.Items) >= MaxItems {
		return ErrTooManyItems
	}
	p.Items = append(p.Items, it)
	p.Sources = append(p.Sources, src)
	return nil
}

// AddPath 加入一个文件或文件夹（递归）。条目路径以它的名字为顶层，例如 "照片/2026/猫.jpg"。
// 文件夹里的符号链接和特殊文件会跳过并记入 Skipped；参数本身是符号链接时跟随它。
func (p *Plan) AddPath(local string) error {
	abs, err := filepath.Abs(local)
	if err != nil {
		return err
	}
	name := filepath.Base(abs)
	if name == string(filepath.Separator) || name == "." || filepath.VolumeName(abs)+string(filepath.Separator) == abs {
		return fmt.Errorf("transfer: cannot send a filesystem root: %s", local)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	switch {
	case info.Mode().IsRegular():
		return p.add(fileItem(name, info), Source{Path: abs})
	case !info.IsDir():
		return fmt.Errorf("transfer: not a regular file or folder: %s", local)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p2 string, d fs.DirEntry, err error) error {
		rel, rerr := filepath.Rel(root, p2)
		if rerr != nil {
			return rerr
		}
		itemPath := path.Join(name, filepath.ToSlash(rel))
		if err != nil {
			p.Skipped = append(p.Skipped, p2)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			return p.add(&pb.Item{Kind: pb.ItemKind_ITEM_KIND_DIR, Path: itemPath}, Source{})
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				p.Skipped = append(p.Skipped, p2)
				return nil
			}
			return p.add(fileItem(itemPath, info), Source{Path: p2})
		default:
			p.Skipped = append(p.Skipped, p2)
			return nil
		}
	})
}

func fileItem(itemPath string, info fs.FileInfo) *pb.Item {
	mt, _, _ := strings.Cut(mime.TypeByExtension(path.Ext(itemPath)), ";")
	return &pb.Item{
		Kind:    pb.ItemKind_ITEM_KIND_FILE,
		Path:    itemPath,
		Size:    uint64(info.Size()),
		MtimeNs: info.ModTime().UnixNano(),
		Mime:    mt,
	}
}

// AddText 加入一段文本：不超过 MaxTextBytes 时放在报价里，否则作为 "文本-时间.txt" 文件发送。
func (p *Plan) AddText(text string, now time.Time) error {
	if !utf8.ValidString(text) {
		return errors.New("transfer: text is not valid UTF-8")
	}
	if len(text) <= MaxTextBytes {
		return p.add(&pb.Item{Kind: pb.ItemKind_ITEM_KIND_TEXT, Text: text}, Source{})
	}
	it := &pb.Item{
		Kind:    pb.ItemKind_ITEM_KIND_FILE,
		Path:    i18n.T("文本") + "-" + now.Format("20060102-150405") + ".txt",
		Size:    uint64(len(text)),
		MtimeNs: now.UnixNano(),
		Mime:    "text/plain",
	}
	return p.add(it, Source{Data: []byte(text)})
}

// Total 返回所有文件的总字节数。
func (p *Plan) Total() int64 { return totalSize(p.Items) }

// Offer 生成报价；报价超过单条消息上限时报错（条目太多或路径太长）。
func (p *Plan) Offer(transferID []byte) (*pb.Offer, error) {
	if len(p.Items) == 0 {
		return nil, errors.New("transfer: nothing to send")
	}
	o := &pb.Offer{TransferId: transferID, Items: p.Items}
	if proto.Size(o) > wire.MaxMessageSize-1024 {
		return nil, errors.New("transfer: offer too large, send fewer files at a time")
	}
	return o, nil
}

func totalSize(items []*pb.Item) int64 {
	var n int64
	for _, it := range items {
		n += int64(it.Size)
	}
	return n
}

// CheckOffer 检查对端报价的结构：条目数量、类型与字段搭配、路径能否清理。
func CheckOffer(o *pb.Offer) error {
	if len(o.TransferId) != 16 || len(o.Items) == 0 || len(o.Items) > MaxItems {
		return ErrBadOffer
	}
	var total uint64
	for _, it := range o.Items {
		switch it.Kind {
		case pb.ItemKind_ITEM_KIND_TEXT:
			if it.Path != "" || it.Size != 0 || len(it.Text) > MaxTextBytes || !utf8.ValidString(it.Text) {
				return ErrBadOffer
			}
		case pb.ItemKind_ITEM_KIND_FILE, pb.ItemKind_ITEM_KIND_DIR:
			if it.Text != "" || (it.Kind == pb.ItemKind_ITEM_KIND_DIR && it.Size != 0) || it.Size > 1<<62 {
				return ErrBadOffer
			}
			if _, err := CleanPath(it.Path); err != nil {
				return ErrBadOffer
			}
			total += it.Size
			if total > 1<<62 {
				return ErrBadOffer // 总大小溢出防护：单项已经限制在 1<<62 以内，这里防止累加溢出或总量失控
			}
		default:
			return ErrBadOffer
		}
	}
	return nil
}

// Describe 用一句话描述报价内容，并返回总字节数，用于日志和询问提示。
// 例如："猫.jpg（1.2 MB）"、"照片（12 个文件，30.5 MB）"、"a.txt 等 3 项（5 个文件，2.0 KB）"、"文本「你好」"。
func Describe(o *pb.Offer) (string, int64) {
	var tops []string
	seen := map[string]bool{}
	files, texts := 0, 0
	firstText := ""
	singleFile := false // 唯一的顶层条目是一个文件
	for _, it := range o.Items {
		switch it.Kind {
		case pb.ItemKind_ITEM_KIND_TEXT:
			if texts == 0 {
				firstText = it.Text
			}
			texts++
			continue
		case pb.ItemKind_ITEM_KIND_FILE:
			files++
		}
		top, _, nested := strings.Cut(it.Path, "/")
		if !seen[top] {
			seen[top] = true
			tops = append(tops, top)
			singleFile = it.Kind == pb.ItemKind_ITEM_KIND_FILE && !nested
		}
	}
	total := totalSize(o.Items)
	var desc string
	switch {
	case len(tops) == 0:
		return i18n.Tf("文本「%s」", preview(firstText, 30)), total
	case len(tops) == 1 && singleFile:
		desc = i18n.Tf("%s（%s）", tops[0], FormatSize(total))
	case len(tops) == 1:
		desc = i18n.Tf("%s（%d 个文件，%s）", tops[0], files, FormatSize(total))
	default:
		desc = i18n.Tf("%s 等 %d 项（%d 个文件，%s）", tops[0], len(tops), files, FormatSize(total))
	}
	if texts > 0 {
		desc += i18n.Tf(" 和 %d 段文本", texts)
	}
	return desc, total
}

func preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// ParseSize 解析 "100MB"、"1.5G"、"512k"、"2048" 这样的大小（按 1024 进位）。
func ParseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	t = strings.TrimSuffix(t, "B")
	mult := int64(1)
	if t != "" {
		switch t[len(t)-1] {
		case 'K':
			mult, t = 1<<10, t[:len(t)-1]
		case 'M':
			mult, t = 1<<20, t[:len(t)-1]
		case 'G':
			mult, t = 1<<30, t[:len(t)-1]
		case 'T':
			mult, t = 1<<40, t[:len(t)-1]
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil || v < 0 || v*float64(mult) > 1<<62 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return int64(v * float64(mult)), nil
}

// FormatSize 把字节数格式化为 "512 B"、"1.5 MB" 这样的形式。
func FormatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, i := float64(n)/unit, 0
	for v >= unit && i < 4 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %cB", v, "KMGTP"[i])
}

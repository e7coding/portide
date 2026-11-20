package transfer

import (
	"errors"
	"io/fs"
	"os"

	"portide/core/pb"
)

// MissingError 表示开始发送前，某个来源文件已经被移动或删除（设计方案 5.3）。
type MissingError struct{ Path string }

func (e *MissingError) Error() string { return "transfer: source file missing: " + e.Path }

// Refresh 在开始（或重新开始）发送前核对每个文件来源（设计方案 5.3、10）：
//   - 文件被移动或删除：返回 *MissingError；
//   - 大小或修改时间变了：就地更新报价中的条目并返回 changed=true。调用方必须给报价换一个新的传输 ID，
//     否则接收方会因为条目摘要不同而拒绝续传。
//
// 内存中的来源（超长文本）不会变，不做检查。
//
// 返回非 nil 的 err 时，o 可能已经被部分修改（前面条目的 Size/MtimeNs 已经就地更新，
// 后面的条目因为出错还没处理）：调用方必须丢弃这个 o，不能继续使用。
func Refresh(o *pb.Offer, srcs []Source) (changed bool, err error) {
	if len(srcs) != len(o.Items) {
		return false, errors.New("transfer: sources do not match offer")
	}
	for i, it := range o.Items {
		if it.Kind != pb.ItemKind_ITEM_KIND_FILE || srcs[i].Path == "" {
			continue
		}
		fi, err := os.Stat(srcs[i].Path)
		if errors.Is(err, fs.ErrNotExist) || (err == nil && !fi.Mode().IsRegular()) {
			return false, &MissingError{Path: srcs[i].Path}
		}
		if err != nil {
			return false, err
		}
		if uint64(fi.Size()) != it.Size || fi.ModTime().UnixNano() != it.MtimeNs {
			it.Size = uint64(fi.Size())
			it.MtimeNs = fi.ModTime().UnixNano()
			changed = true
		}
	}
	return changed, nil
}

package app

import (
	"context"
	"errors"
	"path/filepath"

	"portide/core/appdir"
	"portide/core/chain"
	"portide/core/i18n"
	"portide/core/keystore"
	"portide/core/node"
	"portide/core/pairing"
)

// uiError 把核心返回的常见错误换成界面上显示的中文；不认识的错误原样返回。
func uiError(err error) error {
	if err == nil {
		return nil
	}
	if mapped := appdir.MapStoreErr(err); mapped != err {
		return mapped
	}
	switch {
	case errors.Is(err, keystore.ErrWrongPassphrase):
		return i18n.New("口令不对")
	case errors.Is(err, pairing.ErrWrongCode):
		return i18n.New("配对码不正确")
	case errors.Is(err, pairing.ErrSameCircle):
		return i18n.New("这两台设备已经在同一个设备圈里")
	case errors.Is(err, pairing.ErrInviterOutdated):
		return i18n.New("对方的 Portide 版本太旧，请先在那台设备上升级")
	case errors.Is(err, pairing.ErrMergeUnsupported):
		return i18n.New("对方的 Portide 版本不支持合并设备圈，请先在那台设备上升级")
	case errors.Is(err, pairing.ErrMergeNotConfirmed):
		return i18n.New("对方也有其他设备，需要合并两个设备圈")
	case errors.Is(err, pairing.ErrUnexpectedJoin):
		return i18n.New("对方要求这台设备离开现在的设备圈，已拒绝")
	case errors.Is(err, pairing.ErrStaleCircle):
		return i18n.New("这台设备记下的设备圈比对方新，请让对方先和圈里其他设备同步后再试")
	case errors.Is(err, node.ErrNoInviter):
		return i18n.New("局域网里没有找到正在等待配对的设备：请确认对方已打开\"添加设备\"，或者填写对方的地址")
	case errors.Is(err, node.ErrNotInCircle):
		return i18n.New("还没有加入设备圈")
	case errors.Is(err, node.ErrPortInUse):
		return i18n.New("端口已被占用：可能另一个 Portide 正在运行（包括命令行的 portide daemon），或者别的程序占用了这个端口")
	case errors.Is(err, node.ErrNoLeaveHelper):
		return i18n.New("设备圈里的其他设备都不在线，或者还没升级到支持这个功能的版本")
	case errors.Is(err, node.ErrNoName):
		return i18n.New("请先给这台设备起个名字")
	case errors.Is(err, chain.ErrTooManyRenames):
		return i18n.New("改名次数已达上限（一个设备圈最多改名 %d 次）", chain.MaxRenames)
	case errors.Is(err, context.DeadlineExceeded):
		return i18n.New("操作超时，请重试")
	}
	return err
}

// validName 检查设备名（和核心的规则一致），给出中文提示。
func validName(name string) error {
	if name == "" {
		return i18n.New("请填写设备名")
	}
	if chain.ValidName(name) != nil {
		return i18n.New("设备名无效：最长 %d 字节，不能包含控制字符", chain.MaxNameBytes)
	}
	return nil
}

// keysFile 是文件密钥库的路径（与 appdir.OpenKeys 一致）。
func keysFile(dataDir string) string { return filepath.Join(dataDir, "keys.json") }

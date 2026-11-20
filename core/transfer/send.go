package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"lukechampine.com/blake3"

	"portide/core/pb"
	"portide/core/wire"
)

const (
	// ReplyTimeout 是发送方等待报价应答的时长：接收方可能在询问用户（最多 2 分钟）。
	ReplyTimeout = 3 * time.Minute
	// ackTimeout 是发完一个文件后等待接收方确认的时长（接收方要先同步写盘）。
	ackTimeout = 2 * time.Minute
	// maxParallel 是同时发送的文件数（设计方案 5.1）。
	maxParallel = 4
	// CancelCode 是发送方取消传输时重置控制流使用的错误码。
	CancelCode quic.StreamErrorCode = 1
)

// 接收方拒绝报价的原因代码（OfferReply.reason）。
const (
	ReasonDeclined = "declined" // 用户拒绝
	ReasonTimeout  = "timeout"  // 等待用户确认超时
	ReasonNoSpace  = "no-space" // 磁盘空间不足
	ReasonInvalid  = "invalid"  // 报价不合规
	ReasonError    = "error"    // 接收方内部错误
	ReasonStopped  = "stopped"  // 接收方停止过这次接收（M3b）：发送方不要再试
)

// ErrInterrupted 表示连接在传输途中断开；同一报价可以在重新连上后续传。
var ErrInterrupted = errors.New("transfer: connection interrupted")

// ErrUserCancel 表示用户主动取消了传输。调用方用 context.WithCancelCause(…)(ErrUserCancel) 表示用户取消；
// 其他原因的取消（例如守护进程关闭）按连接中断处理，接收方会保留临时文件等续传。
var ErrUserCancel = errors.New("transfer: cancelled by user")

// RejectedError 表示接收方拒绝了报价。
type RejectedError struct{ Reason string }

func (e *RejectedError) Error() string { return "transfer: offer rejected: " + e.Reason }

// ItemError 是一个没能送达的条目；其他条目不受影响。
type ItemError struct {
	Path string
	Err  string
}

// Send 在成员连接 conn 上完成一次发送：报价、最多 4 个文件并行发送、逐个等待确认，最后发 TransferEnd。
// srcs 与报价条目一一对应。progress 在发出数据（或因续传跳过数据）后调用，参数是新增的字节数（重传时可能为负）。
//
// 返回没能送达的条目。连接中断时返回 ErrInterrupted；接收方拒绝时返回 *RejectedError；
// ctx 取消时通知接收方删除未完成的文件，并返回 ctx.Err()。
func Send(ctx context.Context, conn *quic.Conn, o *pb.Offer, srcs []Source, progress func(int64)) ([]ItemError, error) {
	if len(srcs) != len(o.Items) {
		return nil, errors.New("transfer: sources do not match offer")
	}
	ctrl, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, interrupted(ctx, err)
	}
	// 等待应答期间取消：直接重置控制流，接收方据此丢弃这次传输。
	stop := context.AfterFunc(ctx, func() { code := cancelCode(ctx); ctrl.CancelWrite(code); ctrl.CancelRead(code) })
	_ = ctrl.SetDeadline(time.Now().Add(ReplyTimeout))
	if err := wire.Write(ctrl, &pb.Envelope{Msg: &pb.Envelope_Offer{Offer: o}}); err != nil {
		stop()
		return nil, interrupted(ctx, err)
	}
	var reply pb.OfferReply
	err = wire.Read(ctrl, &reply)
	stopped := stop()
	if !stopped || err != nil {
		if stopped && err != nil {
			// ctx 没有取消，是读应答本身失败：重置控制流，不要让接收方一直等到连接断开。
			ctrl.CancelWrite(0)
			ctrl.CancelRead(0)
		}
		return nil, interrupted(ctx, err)
	}
	_ = ctrl.SetDeadline(time.Time{})
	if !reply.Accepted {
		ctrl.CancelRead(0)
		_ = ctrl.Close()
		return nil, &RejectedError{Reason: reply.Reason}
	}
	if len(reply.Offsets) != len(o.Items) {
		ctrl.CancelWrite(0)
		ctrl.CancelRead(0)
		return nil, errors.New("transfer: malformed offer reply")
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		failures []ItemError
		fatal    error
		wg       sync.WaitGroup
		sem      = make(chan struct{}, maxParallel)
	)
	for i, it := range o.Items {
		if it.Kind != pb.ItemKind_ITEM_KIND_FILE {
			continue
		}
		off := reply.Offsets[i]
		if off > it.Size {
			off = 0
		}
		if off > 0 {
			progress(int64(off))
		}
		if off == it.Size {
			continue // 对方已经有了
		}
		select {
		case sem <- struct{}{}:
		case <-sctx.Done():
		}
		if sctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			msg, err := sendFile(sctx, conn, o, i, off, srcs[i], progress)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil && fatal == nil:
				fatal = err
				cancel()
			case err == nil && msg != "":
				failures = append(failures, ItemError{Path: it.Path, Err: msg})
			}
		}()
	}
	wg.Wait()

	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), ErrUserCancel) {
			_ = ctrl.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_ = wire.Write(ctrl, &pb.TransferEnd{Cancelled: true})
			_ = ctrl.Close()
		} else {
			// 不是用户取消（例如守护进程正在关闭）：按连接中断处理，接收方保留临时文件等续传。
			ctrl.CancelWrite(0)
			ctrl.CancelRead(0)
		}
		return nil, ctx.Err()
	}
	if fatal != nil {
		ctrl.CancelWrite(0) // 不是取消：接收方保留临时文件，等续传
		return nil, interrupted(ctx, fatal)
	}
	_ = ctrl.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := wire.Write(ctrl, &pb.TransferEnd{}); err != nil {
		return nil, interrupted(ctx, err)
	}
	_ = ctrl.Close()
	return failures, nil
}

// cancelCode 决定取消/重置流时使用的错误码：用户取消用 CancelCode（IsCancel 据此识别），
// 其他原因的取消（例如守护进程关闭）用 0，让接收方当作连接中断、保留临时文件等续传。
func cancelCode(ctx context.Context) quic.StreamErrorCode {
	if errors.Is(context.Cause(ctx), ErrUserCancel) {
		return CancelCode
	}
	return 0
}

// interrupted 把流和连接错误归为"中断"（ctx 已取消时返回 ctx.Err()）。
func interrupted(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrInterrupted) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrInterrupted, err)
}

// sendFile 发送一个文件；哈希不一致时从头重传一次。返回非空的 msg 表示这个条目失败，
// 返回 err 表示连接出了问题（整个传输中断）。
func sendFile(ctx context.Context, conn *quic.Conn, o *pb.Offer, i int, offset uint64, src Source, progress func(int64)) (msg string, err error) {
	for attempt := 0; ; attempt++ {
		sent, msg, mismatch, err := sendOnce(ctx, conn, o, i, offset, src, progress)
		if err != nil || !mismatch {
			return msg, err
		}
		if attempt == 1 {
			return "hash mismatch after retry", nil
		}
		progress(-int64(offset) - sent) // 从头重传：撤回之前计入的进度
		offset = 0
	}
}

// sendOnce 在一条新数据流上发送文件的 [offset, size) 部分，返回本次发出的字节数。
func sendOnce(ctx context.Context, conn *quic.Conn, o *pb.Offer, i int, offset uint64, src Source, progress func(int64)) (sent int64, msg string, mismatch bool, err error) {
	it := o.Items[i]
	r, closeSrc, err := openSource(src, it)
	if err != nil {
		return 0, err.Error(), false, nil
	}
	defer closeSrc()

	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return 0, "", false, interrupted(ctx, err)
	}
	stop := context.AfterFunc(ctx, func() { code := cancelCode(ctx); str.CancelWrite(code); str.CancelRead(code) })
	defer stop()
	if err := wire.Write(str, &pb.Envelope{Msg: &pb.Envelope_FileData{FileData: &pb.FileData{TransferId: o.TransferId, Item: uint32(i), Offset: offset}}}); err != nil {
		return 0, "", false, interrupted(ctx, err)
	}
	h := blake3.New(32, nil)
	if _, err := io.Copy(h, io.NewSectionReader(r, 0, int64(offset))); err != nil {
		str.CancelWrite(0)
		return 0, "cannot read source file", false, nil
	}
	body := io.NewSectionReader(r, int64(offset), int64(it.Size-offset))
	buf := make([]byte, chunkSize)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if _, err := str.Write(buf[:n]); err != nil {
				return sent, "", false, interrupted(ctx, err)
			}
			sent += int64(n)
			progress(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			str.CancelWrite(0)
			return sent, "cannot read source file", false, nil
		}
	}
	if uint64(sent) != it.Size-offset {
		str.CancelWrite(0)
		return sent, "source file changed", false, nil
	}
	if err := wire.Write(str, &pb.FileEnd{Blake3: h.Sum(nil)}); err != nil {
		return sent, "", false, interrupted(ctx, err)
	}
	_ = str.Close()
	_ = str.SetReadDeadline(time.Now().Add(ackTimeout))
	var ack pb.FileAck
	if err := wire.ReadMax(str, &ack, 4096); err != nil {
		return sent, "", false, interrupted(ctx, err)
	}
	if ack.Ok {
		return sent, "", false, nil
	}
	return sent, ack.Error, ack.HashMismatch, nil
}

// openSource 打开条目的来源，并确认文件在报价之后没有变过（设计方案 10：源文件被修改）。
func openSource(src Source, it *pb.Item) (io.ReaderAt, func(), error) {
	if src.Path == "" {
		if uint64(len(src.Data)) != it.Size {
			return nil, nil, errors.New("source data does not match offer")
		}
		return bytes.NewReader(src.Data), func() {}, nil
	}
	f, err := os.Open(src.Path)
	if err != nil {
		return nil, nil, errors.New("cannot open source file")
	}
	fi, err := f.Stat()
	if err != nil || uint64(fi.Size()) != it.Size || fi.ModTime().UnixNano() != it.MtimeNs {
		f.Close()
		return nil, nil, errors.New("source file changed")
	}
	return f, func() { f.Close() }, nil
}

// IsCancel 报告接收方读控制流时得到的错误是否表示发送方取消了传输。
func IsCancel(err error) bool {
	var se *quic.StreamError
	return errors.As(err, &se) && se.Remote && se.ErrorCode == CancelCode
}

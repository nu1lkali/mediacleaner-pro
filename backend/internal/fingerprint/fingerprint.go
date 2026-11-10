// Package fingerprint 实现多阶段内容指纹。
//
//   - Quick：4 点采样 + xxHash64，仅用于收缩候选，不能作为相等证据；
//   - HeadTail：首尾各 1MB 的 BLAKE3，用于排除"同大小不同内容"；
//   - Full：分块流式 BLAKE3，支持取消与 IO 限速。
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"

	"github.com/cespare/xxhash/v2"
	"github.com/zeebo/blake3"

	"mediacleaner/internal/throttle"
)

// 算法名：与 quarantine_items.hash_algo 的取值一一对应。
const (
	AlgoBLAKE3 = "blake3"
	AlgoSHA256 = "sha256"
	AlgoNone   = "none"
)

// NewHasher 返回与算法名匹配的哈希器（nil = 不需要算哈希）。
// 隔离区复制时需要「边写边算」，所以这里要求返回可增量写入的 hash.Hash。
func NewHasher(algo string) hash.Hash {
	switch algo {
	case AlgoBLAKE3:
		return blake3.New()
	case AlgoSHA256:
		return sha256.New()
	}
	return nil
}

var ErrAborted = errors.New("hash aborted")

// Quick 采样指纹。小文件全量读；大文件取 4 段各 block 字节，偏移参与哈希。
func Quick(path string, size, block, small int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if size <= small {
		h := xxhash.New()
		buf := make([]byte, block)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				_, _ = h.Write(buf[:n])
			}
			if err == io.EOF {
				return hex.EncodeToString(h.Sum(nil)), nil
			}
			if err != nil {
				return "", err
			}
		}
	}

	offsets := []int64{0, max64(0, size/3-block/2), max64(0, (2*size)/3-block/2), max64(0, size-block)}
	h := xxhash.New()
	buf := make([]byte, block)
	var lenBuf [4]byte
	for _, off := range offsets {
		var offBuf [8]byte
		binary.LittleEndian.PutUint64(offBuf[:], uint64(off))
		_, _ = h.Write(offBuf[:])
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return "", err
		}
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return "", err
		}
		_, _ = h.Write(buf[:n])
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(n))
		_, _ = h.Write(lenBuf[:])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HeadTail 首尾窗口 BLAKE3；小文件等价全文哈希。
func HeadTail(path string, size, window int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := blake3.New()
	head := make([]byte, min64(window, size))
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	_, _ = h.Write(head[:n])
	if size > window {
		tail := min64(window, size-window)
		if _, err := f.Seek(size-tail, io.SeekStart); err != nil {
			return "", err
		}
		var lb [8]byte
		binary.LittleEndian.PutUint64(lb[:], uint64(tail))
		_, _ = h.Write(lb[:])
		tbuf := make([]byte, tail)
		m, err := io.ReadFull(f, tbuf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return "", err
		}
		_, _ = h.Write(tbuf[:m])
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16]), nil
}

// Full 全文 BLAKE3。abort 返回 false 时中止（用于暂停/取消）。
func Full(path string, chunk int64, abort func() bool, onBytes func(int64)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := blake3.New()
	buf := make([]byte, chunk)
	bucket := throttle.Get()
	for {
		if abort != nil && !abort() {
			return "", ErrAborted
		}
		bucket.Acquire(len(buf))
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			if onBytes != nil {
				onBytes(int64(n))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum), nil
}

// SHA256File 用于隔离区入库校验。
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SameBytes 逐块比较两个文件内容。
func SameBytes(a, b string, chunk int) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil || sa.Size() != sb.Size() {
		return false
	}
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba := make([]byte, chunk)
	bb := make([]byte, chunk)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb {
			return false
		}
		if na > 0 && !equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea == io.EOF || ea == io.ErrUnexpectedEOF {
			return true
		}
		if ea != nil || (eb != nil && eb != io.ErrUnexpectedEOF && eb != io.EOF) {
			return false
		}
	}
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

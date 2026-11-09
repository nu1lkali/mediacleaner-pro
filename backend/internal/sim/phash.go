// Package sim 提供感知哈希与相似媒体聚类。
package sim

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"math/bits"
	"os"
	"strings"

	xdraw "golang.org/x/image/draw"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const (
	dctSize = 32
	lowFreq = 8
)

var cosTable [lowFreq][dctSize]float64

func init() {
	for u := 0; u < lowFreq; u++ {
		for x := 0; x < dctSize; x++ {
			cosTable[u][x] = math.Cos(float64(2*x+1) * float64(u) * math.Pi / float64(2*dctSize))
		}
	}
}

// grayMatrix 把图片缩放为 32x32 灰度矩阵。
func grayMatrix(img image.Image) [dctSize][dctSize]float64 {
	var mat [dctSize][dctSize]float64
	dst := image.NewRGBA(image.Rect(0, 0, dctSize, dctSize))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	for y := 0; y < dctSize; y++ {
		for x := 0; x < dctSize; x++ {
			c := dst.RGBAAt(x, y)
			mat[y][x] = 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
		}
	}
	return mat
}

// PHash 计算 64bit 感知哈希。
func PHash(img image.Image) uint64 {
	mat := grayMatrix(img)
	var rows [dctSize][lowFreq]float64
	for y := 0; y < dctSize; y++ {
		for v := 0; v < lowFreq; v++ {
			var acc float64
			for x := 0; x < dctSize; x++ {
				acc += mat[y][x] * cosTable[v][x]
			}
			rows[y][v] = acc
		}
	}
	var coefs [lowFreq * lowFreq]float64
	idx := 0
	for u := 0; u < lowFreq; u++ {
		for v := 0; v < lowFreq; v++ {
			var acc float64
			for y := 0; y < dctSize; y++ {
				acc += rows[y][v] * cosTable[u][y]
			}
			coefs[idx] = acc
			idx++
		}
	}
	median := medianOf(coefs[1:])
	var h uint64
	for i := 1; i < len(coefs); i++ {
		if coefs[i] > median {
			h |= 1 << uint(63-i)
		}
	}
	return h
}

func medianOf(v []float64) float64 {
	cp := append([]float64(nil), v...)
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j] < cp[j-1]; j-- {
			cp[j], cp[j-1] = cp[j-1], cp[j]
		}
	}
	return cp[len(cp)/2]
}

// DHash 计算 64bit 差值哈希。
func DHash(img image.Image) uint64 {
	dst := image.NewRGBA(image.Rect(0, 0, 9, 8))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	var h uint64
	k := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			l := luminance(dst.RGBAAt(x, y))
			r := luminance(dst.RGBAAt(x+1, y))
			if l > r {
				h |= 1 << uint(63-k)
			}
			k++
		}
	}
	return h
}

// AHash 计算 64bit 均值哈希。
func AHash(img image.Image) uint64 {
	dst := image.NewRGBA(image.Rect(0, 0, 8, 8))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	var sum float64
	vals := make([]float64, 0, 64)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			v := luminance(dst.RGBAAt(x, y))
			vals = append(vals, v)
			sum += v
		}
	}
	avg := sum / 64
	var h uint64
	for i, v := range vals {
		if v >= avg {
			h |= 1 << uint(63-i)
		}
	}
	return h
}

func luminance(c color.RGBA) float64 {
	return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
}

// HashFile 返回 (phash, dhash, ahash) 的 16 进制字符串。
func HashFile(path string) (string, string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return "", "", "", err
	}
	return fmt.Sprintf("%016x", PHash(img)), fmt.Sprintf("%016x", DHash(img)), fmt.Sprintf("%016x", AHash(img)), nil
}

// Parse 解析 16 进制哈希；失败返回 0。
func Parse(h string) uint64 {
	var v uint64
	_, err := fmt.Sscanf(strings.TrimSpace(h), "%x", &v)
	if err != nil {
		return 0
	}
	return v
}

func Hamming(a, b uint64) int { return bits.OnesCount64(a ^ b) }

func HammingHex(a, b string) int { return Hamming(Parse(a), Parse(b)) }

func Similarity(dist int) float64 {
	s := 1.0 - float64(dist)/64.0
	if s < 0 {
		return 0
	}
	return s
}

// HashBytes 对内存中的图片数据（视频抽帧）计算 pHash。
// 第二个返回值表示解码是否成功。
func HashBytes(data []byte) (string, bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%016x", PHash(img)), true
}

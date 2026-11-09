// Package media 提供媒体元数据探测、缩略图与流式播放。
package media

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
)

// Info 是探测结果，字段与 file_media 表对齐。
type Info struct {
	DurationMS int64
	Width      int
	Height     int
	VideoCodec string
	AudioCodec string
	Bitrate    int64
	FPS        float64
	Container  string
	Source     string
	ExtraJSON  string
	ExifJSON   string
}

// 容器只读头部，IO 成本极低。

type box struct {
	typ        string
	start, end int64 // payload 范围
}

func iterBoxes(buf []byte, start, end int) []box {
	var out []box
	off := start
	for off+8 <= end {
		size := int(binary.BigEndian.Uint32(buf[off : off+4]))
		typ := string(buf[off+4 : off+8])
		hdr := 8
		if size == 1 {
			if off+16 > end {
				break
			}
			size = int(binary.BigEndian.Uint64(buf[off+8 : off+16]))
			hdr = 16
		} else if size == 0 {
			size = end - off
		}
		if size < hdr || off+size > end {
			break
		}
		out = append(out, box{typ: typ, start: int64(off + hdr), end: int64(off + size)})
		off += size
	}
	return out
}

func findBox(boxes []box, typ string) *box {
	for i := range boxes {
		if boxes[i].typ == typ {
			return &boxes[i]
		}
	}
	return nil
}

// ProbeMP4 解析 MP4/MOV：moov/mvhd/tkhd/mdhd/stsd/stts。
func ProbeMP4(path string, size int64) *Info {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var moovOff, moovLen int64 = -1, 0
	var off int64
	hdr := make([]byte, 16)
	for i := 0; i < 4096; i++ {
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			break
		}
		bsize := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		hsz := int64(8)
		if bsize == 1 {
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				break
			}
			bsize = int64(binary.BigEndian.Uint64(hdr[8:16]))
			hsz = 16
		} else if bsize == 0 {
			bsize = size - off
		}
		if typ == "moov" {
			moovOff, moovLen = off+hsz, bsize-hsz
			break
		}
		if bsize < hsz {
			break
		}
		off += bsize
		if off >= size {
			break
		}
	}
	if moovOff < 0 || moovLen <= 0 {
		return nil
	}
	if moovLen > 16<<20 {
		moovLen = 16 << 20
	}
	buf := make([]byte, moovLen)
	if _, err := f.ReadAt(buf, moovOff); err != nil && err != io.EOF {
		return nil
	}
	info := &Info{Container: "mp4", Source: "native"}
	top := iterBoxes(buf, 0, len(buf))
	found := false
	for _, b := range top {
		switch b.typ {
		case "mvhd":
			s := int(b.start)
			ver := buf[s]
			var ts, dur uint64
			if ver == 0 {
				ts = uint64(binary.BigEndian.Uint32(buf[s+12 : s+16]))
				dur = uint64(binary.BigEndian.Uint32(buf[s+16 : s+20]))
			} else {
				ts = uint64(binary.BigEndian.Uint32(buf[s+20 : s+24]))
				dur = binary.BigEndian.Uint64(buf[s+24 : s+32])
			}
			if ts > 0 {
				info.DurationMS = int64(dur * 1000 / ts)
			}
			found = true
		case "trak":
			parseTrak(buf, int(b.start), int(b.end), info)
		}
	}
	if !found && info.DurationMS == 0 {
		return nil
	}
	if d := float64(info.DurationMS) / 1000.0; d > 0 {
		info.Bitrate = int64(float64(size) * 8 / d)
	}
	return info
}

func parseTrak(buf []byte, start, end int, info *Info) {
	kids := iterBoxes(buf, start, end)
	tkhd := findBox(kids, "tkhd")
	var width, height int
	if tkhd != nil {
		e := int(tkhd.end)
		if e-8 >= 0 && e <= len(buf) {
			width = int(binary.BigEndian.Uint32(buf[e-8:e-4]) >> 16)
			height = int(binary.BigEndian.Uint32(buf[e-4:e]) >> 16)
		}
	}
	mdia := findBox(kids, "mdia")
	if mdia == nil {
		return
	}
	mdiaKids := iterBoxes(buf, int(mdia.start), int(mdia.end))
	var handler string
	var timescale, duration uint64
	var samples int64
	var codec string
	if mdhd := findBox(mdiaKids, "mdhd"); mdhd != nil {
		s := int(mdhd.start)
		if buf[s] == 0 {
			timescale = uint64(binary.BigEndian.Uint32(buf[s+12 : s+16]))
			duration = uint64(binary.BigEndian.Uint32(buf[s+16 : s+20]))
		} else {
			timescale = uint64(binary.BigEndian.Uint32(buf[s+20 : s+24]))
			duration = binary.BigEndian.Uint64(buf[s+24 : s+32])
		}
	}
	if hdlr := findBox(mdiaKids, "hdlr"); hdlr != nil {
		s := int(hdlr.start)
		if s+12 <= int(hdlr.end) {
			handler = string(buf[s+8 : s+12])
		}
	}
	if minf := findBox(mdiaKids, "minf"); minf != nil {
		minfKids := iterBoxes(buf, int(minf.start), int(minf.end))
		if stbl := findBox(minfKids, "stbl"); stbl != nil {
			stblKids := iterBoxes(buf, int(stbl.start), int(stbl.end))
			if stsd := findBox(stblKids, "stsd"); stsd != nil {
				s := int(stsd.start)
				if s+16 <= int(stsd.end) {
					codec = strings.TrimSpace(string(buf[s+12 : s+16]))
				}
			}
			if stts := findBox(stblKids, "stts"); stts != nil {
				s := int(stts.start)
				if s+8 <= int(stts.end) {
					entries := int(binary.BigEndian.Uint32(buf[s+4 : s+8]))
					pos := s + 8
					for k := 0; k < entries && pos+8 <= int(stts.end); k++ {
						samples += int64(binary.BigEndian.Uint32(buf[pos : pos+4]))
						pos += 8
					}
				}
			}
		}
	}
	switch handler {
	case "vide":
		if info.Width == 0 {
			info.Width = width
		}
		if info.Height == 0 {
			info.Height = height
		}
		if info.VideoCodec == "" {
			info.VideoCodec = codec
		}
		if duration > 0 && timescale > 0 && samples > 0 {
			info.FPS = float64(samples) / (float64(duration) / float64(timescale))
		}
	case "soun":
		if info.AudioCodec == "" {
			info.AudioCodec = codec
		}
	}
}

// ---------- MKV / WebM (EBML) ----------

type ebmlElem struct {
	id         uint64
	start, end int
}

func readVint(buf []byte, pos int, keepMarker bool) (uint64, int, bool) {
	if pos >= len(buf) || buf[pos] == 0 {
		return 0, pos, false
	}
	first := buf[pos]
	length := 1
	var mask byte = 0x80
	for length <= 8 && first&mask == 0 {
		mask >>= 1
		length++
	}
	if length > 8 || pos+length > len(buf) {
		return 0, pos, false
	}
	var v uint64
	if keepMarker {
		v = uint64(first)
	} else {
		v = uint64(first & (mask - 1))
	}
	for i := 1; i < length; i++ {
		v = v<<8 | uint64(buf[pos+i])
	}
	return v, pos + length, true
}

func ebmlElems(buf []byte, start, end int) []ebmlElem {
	var out []ebmlElem
	pos := start
	for pos < end {
		id, p, ok := readVint(buf, pos, true)
		if !ok {
			return out
		}
		sz, p2, ok := readVint(buf, p, false)
		if !ok {
			return out
		}
		if int64(p2)+int64(sz) > int64(end) {
			return out
		}
		out = append(out, ebmlElem{id: id, start: p2, end: p2 + int(sz)})
		pos = p2 + int(sz)
	}
	return out
}

const (
	idSegment   = 0x18538067
	idInfo      = 0x1549A966
	idTracks    = 0x1654AE6B
	idTrackEnty = 0xAE
	idTimeScale = 0x2AD7B1
	idDuration  = 0x4489
	idTrackType = 0x83
	idCodecID   = 0x86
	idVideo     = 0xE0
	idPixelW    = 0xB0
	idPixelH    = 0xBA
	idDefDur    = 0x23E383
)

// ProbeMKV 解析 MKV/WebM 头部（Segment/Info/Tracks）。
func ProbeMKV(path string, size int64) *Info {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	n := size
	if n > 4<<20 {
		n = 4 << 20
	}
	buf := make([]byte, n)
	read, _ := io.ReadFull(f, buf)
	buf = buf[:read]
	if len(buf) < 4 || buf[0] != 0x1A || buf[1] != 0x45 || buf[2] != 0xDF || buf[3] != 0xA3 {
		return nil
	}
	info := &Info{Container: "mkv", Source: "native"}
	scale := int64(1000000)
	var duration float64
	found := false

	var walk func(start, end, depth int)
	walk = func(start, end, depth int) {
		if depth > 6 {
			return
		}
		for _, e := range ebmlElems(buf, start, end) {
			switch e.id {
			case idSegment:
				walk(e.start, e.end, depth+1)
			case idInfo:
				for _, i := range ebmlElems(buf, e.start, e.end) {
					switch i.id {
					case idTimeScale:
						if v := beUint(buf[i.start:i.end]); v > 0 {
							scale = int64(v)
						}
					case idDuration:
						if i.end-i.start == 4 {
							duration = float64(mathFloat32(buf[i.start:i.end]))
						} else if i.end-i.start == 8 {
							duration = mathFloat64(buf[i.start:i.end])
						}
						found = true
					}
				}
			case idTracks:
				for _, t := range ebmlElems(buf, e.start, e.end) {
					if t.id == idTrackEnty {
						parseMKVTrack(buf, t.start, t.end, info)
					}
				}
			default:
				walk(e.start, e.end, depth+1)
			}
		}
	}
	walk(0, len(buf), 0)
	if !found && info.Width == 0 {
		return nil
	}
	if duration > 0 {
		info.DurationMS = int64(duration * float64(scale) / 1e6)
	}
	if d := float64(info.DurationMS) / 1000.0; d > 0 {
		info.Bitrate = int64(float64(size) * 8 / d)
	}
	return info
}

func parseMKVTrack(buf []byte, start, end int, info *Info) {
	var ttype int
	var codec string
	var w, h int
	var defDur uint64
	for _, e := range ebmlElems(buf, start, end) {
		switch e.id {
		case idTrackType:
			ttype = int(beUint(buf[e.start:e.end]))
		case idCodecID:
			codec = string(buf[e.start:e.end])
		case idDefDur:
			defDur = beUint(buf[e.start:e.end])
		case idVideo:
			for _, v := range ebmlElems(buf, e.start, e.end) {
				switch v.id {
				case idPixelW:
					w = int(beUint(buf[v.start:v.end]))
				case idPixelH:
					h = int(beUint(buf[v.start:v.end]))
				}
			}
		}
	}
	switch ttype {
	case 1:
		if info.Width == 0 {
			info.Width = w
			info.Height = h
		}
		if info.VideoCodec == "" && codec != "" {
			info.VideoCodec = strings.ToUpper(strings.TrimPrefix(codec, "V_"))
		}
		if defDur > 0 && info.FPS == 0 {
			info.FPS = 1e9 / float64(defDur)
		}
	case 2:
		if info.AudioCodec == "" && codec != "" {
			info.AudioCodec = strings.ToUpper(strings.TrimPrefix(codec, "A_"))
		}
	}
}

func beUint(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}

// ProbeAVI 解析 RIFF AVI 的 avih / strh。
func ProbeAVI(path string, size int64) *Info {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	n := size
	if n > 2<<20 {
		n = 2 << 20
	}
	buf := make([]byte, n)
	read, _ := io.ReadFull(f, buf)
	buf = buf[:read]
	if len(buf) < 12 || string(buf[0:4]) != "RIFF" || string(buf[8:12]) != "AVI " {
		return nil
	}
	idx := indexOf(buf, []byte("avih"))
	if idx < 0 || idx+8+40 > len(buf) {
		return nil
	}
	b := idx + 8
	microPerFrame := binary.LittleEndian.Uint32(buf[b : b+4])
	totalFrames := binary.LittleEndian.Uint32(buf[b+16 : b+20])
	width := binary.LittleEndian.Uint32(buf[b+32 : b+36])
	height := binary.LittleEndian.Uint32(buf[b+36 : b+40])
	info := &Info{Container: "avi", Source: "native", Width: int(width), Height: int(height)}
	if microPerFrame > 0 {
		info.FPS = 1e6 / float64(microPerFrame)
		info.DurationMS = int64(float64(totalFrames) * float64(microPerFrame) / 1000)
	}
	if i := indexOf(buf, []byte("strh")); i >= 0 && i+8+8 <= len(buf) {
		fcc := buf[i+8 : i+12]
		if string(fcc) == "vids" {
			codec := strings.TrimRight(string(buf[i+12:i+16]), "\x00 ")
			if codec != "" {
				info.VideoCodec = codec
			}
		}
	}
	if d := float64(info.DurationMS) / 1000.0; d > 0 {
		info.Bitrate = int64(float64(size) * 8 / d)
	}
	return info
}

func indexOf(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// ProbeNative 按扩展名选择内置解析器；不支持则返回 nil（绝不臆造数据）。
func ProbeNative(path, ext string, size int64) *Info {
	switch strings.ToLower(ext) {
	case ".mp4", ".m4v", ".mov":
		return ProbeMP4(path, size)
	case ".mkv", ".webm":
		return ProbeMKV(path, size)
	case ".avi":
		return ProbeAVI(path, size)
	}
	return nil
}

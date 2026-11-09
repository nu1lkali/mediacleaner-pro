package sim

import (
	"bytes"
	"math/bits"
)

// Item 是聚类输入：文件 ID、感知哈希、内容哈希（用于排除完全重复）。
type Item struct {
	FileID      int64
	Hash        string
	ContentHash string
}

// Cluster 是聚类结果：[(fileID, similarity)]，首元素相似度最高。
type Cluster []Member

type Member struct {
	FileID     int64
	Similarity float64
}

type unionFind struct{ p []int }

func newUF(n int) *unionFind {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &unionFind{p: p}
}

func (u *unionFind) find(x int) int {
	for u.p[x] != x {
		u.p[x] = u.p[u.p[x]]
		x = u.p[x]
	}
	return x
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.p[rb] = ra
	}
}

const (
	chunkBits = 16
	chunks    = 4
)

// ClusterByPHash 对图片 pHash 聚类。
//
//   - 规模 <= exactLimit：全量两两比较，结果精确；
//   - 更大规模：MIH 多索引哈希（4×16bit，半径 floor(t/4)）产生候选，再做精确过滤。
//
// 完全重复（content_hash 相同）的成员不计入相似度证据。
func ClusterByPHash(items []Item, threshold, exactLimit int) []Cluster {
	if len(items) < 2 {
		return nil
	}
	vals := make([]uint64, len(items))
	for i, it := range items {
		vals[i] = Parse(it.Hash)
	}
	uf := newUF(len(items))
	pairSim := map[[2]int]float64{}

	consider := func(a, b int) {
		if items[a].ContentHash != "" && items[a].ContentHash == items[b].ContentHash {
			return
		}
		d := bits.OnesCount64(vals[a] ^ vals[b])
		if d <= threshold {
			uf.union(a, b)
			key := [2]int{a, b}
			if a > b {
				key = [2]int{b, a}
			}
			pairSim[key] = Similarity(d)
		}
	}

	if len(items) <= exactLimit {
		for i := 0; i < len(items); i++ {
			for j := i + 1; j < len(items); j++ {
				consider(i, j)
			}
		}
	} else {
		radius := threshold / chunks
		if radius < 1 {
			radius = 1
		}
		if radius > 2 {
			radius = 2
		}
		buckets := map[uint32][]int{}
		add := func(idx int) {
			v := vals[idx]
			for c := 0; c < chunks; c++ {
				part := uint32((v >> uint(c*chunkBits)) & 0xFFFF)
				for _, variant := range variants(part, radius) {
					key := uint32(c)<<16 | variant
					buckets[key] = append(buckets[key], idx)
				}
			}
		}
		for i := range items {
			add(i)
		}
		compared := map[[2]int]struct{}{}
		for i := 0; i < len(items); i++ {
			v := vals[i]
			cands := map[int]struct{}{}
			for c := 0; c < chunks; c++ {
				part := uint32((v >> uint(c*chunkBits)) & 0xFFFF)
				for _, variant := range variants(part, radius) {
					for _, other := range buckets[uint32(c)<<16|variant] {
						cands[other] = struct{}{}
					}
				}
			}
			for other := range cands {
				if other == i {
					continue
				}
				key := [2]int{i, other}
				if i > other {
					key = [2]int{other, i}
				}
				if _, ok := compared[key]; ok {
					continue
				}
				compared[key] = struct{}{}
				consider(i, other)
			}
		}
	}

	groups := map[int][]int{}
	for i := range items {
		r := uf.find(i)
		groups[r] = append(groups[r], i)
	}
	out := make([]Cluster, 0, len(groups))
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		root := members[0]
		cl := make(Cluster, 0, len(members))
		for _, m := range members {
			sim := 1.0
			if m != root {
				key := [2]int{root, m}
				if root > m {
					key = [2]int{m, root}
				}
				if v, ok := pairSim[key]; ok {
					sim = v
				} else {
					sim = Similarity(bits.OnesCount64(vals[root] ^ vals[m]))
				}
			}
			cl = append(cl, Member{FileID: items[m].FileID, Similarity: round4(sim)})
		}
		sortMembers(cl)
		out = append(out, cl)
	}
	return out
}

func variants(v uint32, radius int) []uint32 {
	out := []uint32{v}
	if radius >= 1 {
		for i := 0; i < chunkBits; i++ {
			out = append(out, v^(1<<uint(i)))
		}
	}
	if radius >= 2 {
		for i := 0; i < chunkBits; i++ {
			for j := i + 1; j < chunkBits; j++ {
				out = append(out, v^(1<<uint(i))^(1<<uint(j)))
			}
		}
	}
	return out
}

func sortMembers(m []Member) {
	for i := 1; i < len(m); i++ {
		for j := i; j > 0 && m[j].Similarity > m[j-1].Similarity; j-- {
			m[j], m[j-1] = m[j-1], m[j]
		}
	}
}

func round4(f float64) float64 {
	return float64(int64(f*10000+0.5)) / 10000
}

// ---------- 视频关键帧指纹 ----------

// Frame 是一帧的 (时间戳 ms, pHash)。
type Frame struct {
	TS   int64  `json:"ts"`
	Hash string `json:"h"`
}

type VideoFingerprint struct {
	Frames []Frame `json:"frames"`
}

var positions = []float64{0.10, 0.30, 0.50, 0.70, 0.90}

// Compare 比较两个视频指纹：帧序列允许 ±1 偏移以容忍片头差异。
// 返回 (相似度, 命中帧数)。
func Compare(a, b VideoFingerprint, minHits int) (float64, int) {
	if len(a.Frames) == 0 || len(b.Frames) == 0 {
		return 0, 0
	}
	hits := 0
	total := 0.0
	used := map[int]bool{}
	for i, fa := range a.Frames {
		ia := Parse(fa.Hash)
		bestJ, bestD := -1, 65
		for j := i - 1; j <= i+1; j++ {
			if j < 0 || j >= len(b.Frames) || used[j] {
				continue
			}
			d := Hamming(ia, Parse(b.Frames[j].Hash))
			if d < bestD {
				bestD, bestJ = d, j
			}
		}
		if bestJ >= 0 {
			used[bestJ] = true
			if bestD <= 12 {
				hits++
				total += 1 - float64(bestD)/64.0
			}
		}
	}
	if hits < minHits {
		return 0, hits
	}
	return round4(total / float64(len(a.Frames))), hits
}

// VideoCandidatePairs 按时长（<=6%）与宽高比筛出候选对。
func VideoCandidatePairs(videos []VideoMeta, ratioTolerance float64) [][2]int {
	var pairs [][2]int
	for i := 0; i < len(videos); i++ {
		if videos[i].DurationMS == 0 {
			continue
		}
		for j := i + 1; j < len(videos); j++ {
			if videos[j].DurationMS == 0 {
				continue
			}
			d1, d2 := float64(videos[i].DurationMS), float64(videos[j].DurationMS)
			maxD := d1
			if d2 > maxD {
				maxD = d2
			}
			if (d1-d2)/maxD > ratioTolerance || (d2-d1)/maxD > ratioTolerance {
				continue
			}
			arA, arB := aspect(videos[i]), aspect(videos[j])
			if arA > 0 && arB > 0 {
				diff := arA - arB
				if diff < 0 {
					diff = -diff
				}
				m := arA
				if arB > m {
					m = arB
				}
				if diff/m > 0.12 {
					continue
				}
			}
			pairs = append(pairs, [2]int{i, j})
		}
	}
	return pairs
}

func aspect(v VideoMeta) float64 {
	if v.Height == 0 {
		return 0
	}
	return float64(v.Width) / float64(v.Height)
}

type VideoMeta struct {
	FileID     int64
	Path       string
	DurationMS int64
	Width      int
	Height     int
}

// ClusterVideos 对视频做相似聚类，返回与图片同构的结果。
func ClusterVideos(videos []VideoMeta, fps map[int64]VideoFingerprint, threshold float64, minHits, maxPairs int) []Cluster {
	pairs := VideoCandidatePairs(videos, 0.06)
	if maxPairs > 0 && len(pairs) > maxPairs {
		pairs = pairs[:maxPairs]
	}
	uf := newUF(len(videos))
	sims := map[[2]int]float64{}
	for _, p := range pairs {
		fa, okA := fps[videos[p[0]].FileID]
		fb, okB := fps[videos[p[1]].FileID]
		if !okA || !okB {
			continue
		}
		s, hits := Compare(fa, fb, minHits)
		if s >= threshold && hits >= minHits {
			uf.union(p[0], p[1])
			sims[p] = s
		}
	}
	groups := map[int][]int{}
	for i := range videos {
		r := uf.find(i)
		groups[r] = append(groups[r], i)
	}
	out := make([]Cluster, 0, len(groups))
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		root := members[0]
		cl := make(Cluster, 0, len(members))
		for _, m := range members {
			sim := 1.0
			if m != root {
				key := [2]int{root, m}
				if v, ok := sims[key]; ok {
					sim = v
				} else {
					key = [2]int{m, root}
					if v, ok := sims[key]; ok {
						sim = v
					} else {
						sim = threshold
					}
				}
			}
			cl = append(cl, Member{FileID: videos[m].FileID, Similarity: round4(sim)})
		}
		sortMembers(cl)
		out = append(out, cl)
	}
	return out
}

var _ = bytes.MinRead

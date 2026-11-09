package scan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"mediacleaner/internal/config"
	"mediacleaner/internal/fingerprint"
	"mediacleaner/internal/media"
	"mediacleaner/internal/sim"
	"mediacleaner/internal/store"
	"mediacleaner/internal/throttle"
)

// 任务状态
const (
	StatusPending  = "pending"
	StatusRunning  = "running"
	StatusPaused   = "paused"
	StatusCanceled = "canceled"
	StatusSuccess  = "succeeded"
	StatusFailed   = "failed"
)

// 流水线阶段
const (
	PhaseIndex   = "index"
	PhaseProbe   = "probe"
	PhaseQuick   = "quick"
	PhaseExact   = "exact"
	PhaseSimilar = "similar"
	PhaseGroup   = "group"
	PhaseDone    = "done"
)

var phaseLabels = map[string]string{
	PhaseIndex:            "索引文件元数据",
	PhaseProbe:            "读取媒体信息",
	PhaseQuick:            "快速采样指纹",
	PhaseExact:            "精确内容校验",
	PhaseSimilar + "·image": "图片相似度分析",
	PhaseSimilar + "·video": "视频关键帧分析",
	PhaseGroup:            "生成重复分组",
	PhaseDone:             "已完成",
}

// PhaseLabel 供前端展示。
func PhaseLabel(p string) string {
	if v, ok := phaseLabels[p]; ok {
		return v
	}
	return p
}

// 扫描预设：决定"跑到哪一层"，三个层级互相独立、可单独运行。
//
//	fast     仅完全重复。零 ffmpeg、零解码，纯元数据 + 采样指纹 + 全文校验。
//	          10 万文件的库通常几分钟内出结果，用来"快速查重复视频/文件"。
//	standard fast + 图片感知哈希（纯 Go 解码，不依赖 ffmpeg）。
//	full     standard + 视频关键帧相似度（需要 ffmpeg 抽帧，最慢，可按需开启）。
const (
	ProfileFast     = "fast"
	ProfileStandard = "standard"
	ProfileFull     = "full"
)

// Profiles 返回预设说明，供前端渲染选择卡片。
func Profiles() []map[string]any {
	return []map[string]any{
		{
			"id": ProfileFast, "name": "极速：仅完全重复",
			"desc": "只做元数据索引 + 采样指纹 + 全文校验，不调用 ffmpeg、不解码媒体。适合同名同内容文件、重复视频的快速排查。",
			"detect_exact": true, "detect_image": false, "detect_video": false,
			"need_ffmpeg": false,
		},
		{
			"id": ProfileStandard, "name": "标准：完全重复 + 相似图片",
			"desc": "在极速基础上增加图片 pHash 聚类（纯 Go 实现），可发现同图不同压缩/不同尺寸。",
			"detect_exact": true, "detect_image": true, "detect_video": false,
			"need_ffmpeg": false,
		},
		{
			"id": ProfileFull, "name": "深度：含相似视频",
			"desc": "再增加视频关键帧感知哈希，可发现转码/换封装/改码率的同一视频。需要 ffmpeg，耗时与视频数量线性相关。",
			"detect_exact": true, "detect_image": true, "detect_video": true,
			"need_ffmpeg": true,
		},
	}
}

// Options 是一次扫描的运行参数，持久化在 scan_tasks.options_json。
type Options struct {
	Mode      string  `json:"mode"`    // full | incremental
	Profile   string  `json:"profile"` // fast | standard | full
	RootIDs   []int64 `json:"root_ids"`

	// 三个检测能力可独立开关：显式传入时覆盖 Profile 的默认值。
	DetectExact        bool `json:"detect_exact"`
	DetectSimilarImage bool `json:"detect_similar_image"`
	DetectSimilarVideo bool `json:"detect_similar_video"`

	OnlyKinds      []string `json:"only_kinds"`
	MinFileSize    int64    `json:"min_file_size"`
	ExcludeDirs    []string `json:"exclude_dirs"`
	ExcludeNames   []string `json:"exclude_names"`
	MaxDepth       int      `json:"max_depth"`
	FollowSymlinks bool     `json:"follow_symlinks"`
	IncludeOther   bool     `json:"include_other"`
	DeepProbe      bool     `json:"deep_probe"` // 用 ffprobe 读取精确元数据（默认走内置解析器）
	Threads        int      `json:"threads"`
	LimitMBps      int      `json:"limit_mbps"`
}

// ApplyProfile 把预设展开为具体开关；显式布尔值优先。
func (o *Options) ApplyProfile(explicit bool) {
	if o.Profile == "" {
		o.Profile = ProfileFast
	}
	defExact, defImage, defVideo := true, false, false
	switch o.Profile {
	case ProfileStandard:
		defImage = true
	case ProfileFull:
		defImage, defVideo = true, true
	case ProfileFast:
		// 保持默认
	}
	if !explicit {
		o.DetectExact = defExact
		o.DetectSimilarImage = defImage
		o.DetectSimilarVideo = defVideo
	}
	if !o.DetectExact && !o.DetectSimilarImage && !o.DetectSimilarVideo {
		o.DetectExact = true // 至少跑一层，否则没有意义
	}
	// 相似检测依赖全文校验提供的 content_hash 去重，因此隐含要求 exact
	if o.DetectSimilarImage || o.DetectSimilarVideo {
		o.DetectExact = true
	}
}

func (o *Options) normalize(cfg *config.Config) {
	if o.Mode != "full" {
		o.Mode = "incremental"
	}
	o.ApplyProfile(false)
	if o.Threads <= 0 {
		o.Threads = cfg.Threads
	}
	if o.Threads > cfg.MaxThreads {
		o.Threads = cfg.MaxThreads
	}
	if o.MinFileSize < 0 {
		o.MinFileSize = 0
	}
}

// EnabledKinds 返回本次扫描需要构建的分组类型。
func (o *Options) EnabledKinds() []string {
	out := []string{}
	if o.DetectExact {
		out = append(out, GroupExact)
	}
	if o.DetectSimilarImage {
		out = append(out, GroupImage)
	}
	if o.DetectSimilarVideo {
		out = append(out, GroupVideo)
	}
	return out
}

// Progress 是推送给前端的实时进度。
type Progress struct {
	TaskID       int64   `json:"task_id"`
	Status       string  `json:"status"`
	Phase        string  `json:"phase"`
	PhaseLabel   string  `json:"phase_label"`
	CurrentPath  string  `json:"current_path"`
	FilesSeen    int64   `json:"files_seen"`
	FilesIndexed int64   `json:"files_indexed"`
	BytesScanned int64   `json:"bytes_scanned"`
	ErrorCount   int64   `json:"error_count"`
	PhaseDone    int64   `json:"phase_done"`
	PhaseTotal   int64   `json:"phase_total"`
	Percent      float64 `json:"percent"`
	ElapsedMS    int64   `json:"elapsed_ms"`
	Message      string  `json:"message"`
}

type Engine struct {
	st    *store.Store
	cfg   *config.Config
	roots *RootService

	queue chan int64

	mu      sync.Mutex
	current *runState
	subs    map[int64]chan Progress
	nextSub int64

	closed atomic.Bool
}

type runState struct {
	taskID int64
	ctx    context.Context
	cancel context.CancelFunc

	paused  atomic.Bool
	aborted atomic.Bool

	startedAt time.Time
	progress  Progress
	mu        sync.Mutex
}

func NewEngine(st *store.Store, cfg *config.Config, roots *RootService) *Engine {
	return &Engine{
		st: st, cfg: cfg, roots: roots,
		queue: make(chan int64, 32),
		subs:  map[int64]chan Progress{},
	}
}

// Start 启动后台 worker，并恢复上次异常中断的任务标记。
func (e *Engine) Start(ctx context.Context) {
	// 容器被强杀时 running 状态会残留，启动时统一修正
	_, _ = e.st.Exec(ctx, `UPDATE scan_tasks SET status='failed', message='服务重启导致中断'
		WHERE status IN ('running','pending')`)
	go e.loop(ctx)
}

func (e *Engine) loop(ctx context.Context) {
	for id := range e.queue {
		if ctx.Err() != nil {
			return
		}
		e.run(ctx, id)
	}
}

// Enqueue 把已落库的任务放入执行队列。
func (e *Engine) Enqueue(id int64) error {
	select {
	case e.queue <- id:
		return nil
	default:
		return errors.New("扫描队列已满，请稍后重试")
	}
}

// Subscribe 订阅进度事件，返回通道与取消函数。
func (e *Engine) Subscribe() (<-chan Progress, func()) {
	ch := make(chan Progress, 8)
	e.mu.Lock()
	e.nextSub++
	key := e.nextSub
	e.subs[key] = ch
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		if c, ok := e.subs[key]; ok {
			delete(e.subs, key)
			close(c)
		}
		e.mu.Unlock()
	}
}

func (e *Engine) publish(p Progress) {
	e.mu.Lock()
	for _, ch := range e.subs {
		select {
		case ch <- p:
		default: // 慢消费者直接丢帧，不阻塞扫描
		}
	}
	e.mu.Unlock()
}

// Control 处理暂停/恢复/取消。
func (e *Engine) Control(taskID int64, action string) error {
	e.mu.Lock()
	rs := e.current
	e.mu.Unlock()

	if rs == nil || rs.taskID != taskID {
		// 尚未开始的任务允许直接取消
		if action == "cancel" {
			_, err := e.st.Exec(context.Background(),
				`UPDATE scan_tasks SET status='canceled', finished_at=? WHERE id=? AND status='pending'`,
				store.NowMS(), taskID)
			return err
		}
		return errors.New("任务不在运行中")
	}
	switch action {
	case "pause":
		rs.paused.Store(true)
		rs.setStatus(StatusPaused)
		e.persist(rs, rs.snapshot())
	case "resume":
		rs.paused.Store(false)
		rs.setStatus(StatusRunning)
		e.persist(rs, rs.snapshot())
	case "cancel":
		rs.aborted.Store(true)
		rs.cancel()
	default:
		return errors.New("未知操作: " + action)
	}
	return nil
}

func (rs *runState) setStatus(s string) {
	rs.mu.Lock()
	rs.progress.Status = s
	rs.mu.Unlock()
}

func (rs *runState) snapshot() Progress {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	p := rs.progress
	p.ElapsedMS = time.Since(rs.startedAt).Milliseconds()
	p.PhaseLabel = PhaseLabel(p.Phase)
	if p.PhaseTotal > 0 {
		p.Percent = float64(p.PhaseDone) / float64(p.PhaseTotal) * 100
		if p.Percent > 100 {
			p.Percent = 100
		}
	}
	return p
}

func (rs *runState) update(fn func(p *Progress)) {
	rs.mu.Lock()
	fn(&rs.progress)
	rs.mu.Unlock()
}

// wait 是可中断的暂停闸门。
func (rs *runState) wait() error {
	for rs.paused.Load() {
		if err := rs.ctx.Err(); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return rs.ctx.Err()
}

func (e *Engine) persist(rs *runState, p Progress) {
	if rs == nil {
		return
	}
	pj, _ := json.Marshal(map[string]any{
		"phase": p.Phase, "phase_done": p.PhaseDone, "phase_total": p.PhaseTotal,
		"percent": p.Percent, "message": p.Message,
	})
	_, _ = e.st.Exec(context.Background(),
		`UPDATE scan_tasks SET status=?, phase=?, progress_json=?, current_path=?,
		   files_seen=?, files_indexed=?, bytes_scanned=?, error_count=?, message=?
		 WHERE id=?`,
		p.Status, p.Phase, string(pj), p.CurrentPath, p.FilesSeen, p.FilesIndexed,
		p.BytesScanned, p.ErrorCount, p.Message, rs.taskID)
	e.publish(p)
}

// ---------- 主流程 ----------

func (e *Engine) run(parent context.Context, taskID int64) {
	task, opts, err := e.loadTask(parent, taskID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	rs := &runState{
		taskID:    taskID,
		ctx:       ctx,
		cancel:    cancel,
		startedAt: time.Now(),
		progress: Progress{
			TaskID: taskID, Status: StatusRunning, Phase: PhaseIndex,
			PhaseLabel: PhaseLabel(PhaseIndex),
		},
	}
	e.mu.Lock()
	e.current = rs
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		if e.current == rs {
			e.current = nil
		}
		e.mu.Unlock()
	}()

	_, _ = e.st.Exec(ctx, `UPDATE scan_tasks SET status='running', started_at=? WHERE id=?`, store.NowMS(), taskID)

	if opts.LimitMBps > 0 {
		throttle.SetLimit(float64(opts.LimitMBps))
	} else {
		throttle.SetLimit(0)
	}

	// 定期落库，避免仅靠阶段切换时才更新进度
	stopPersist := make(chan struct{})
	go func() {
		t := time.NewTicker(900 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stopPersist:
				return
			case <-t.C:
				e.persist(rs, rs.snapshot())
			}
		}
	}()

	finalErr := e.pipeline(ctx, rs, task, opts)
	close(stopPersist)

	p := rs.snapshot()
	switch {
	case rs.aborted.Load() || errors.Is(finalErr, context.Canceled):
		p.Status = StatusCanceled
		p.Message = "已取消"
	case finalErr != nil:
		p.Status = StatusFailed
		p.Message = finalErr.Error()
	default:
		p.Status = StatusSuccess
		p.Phase = PhaseDone
		p.Message = fmt.Sprintf("完成：索引 %d 个文件，共 %s", p.FilesIndexed, FormatSize(p.BytesScanned))
	}
	e.persist(rs, p)
	_, _ = e.st.Exec(context.Background(), `UPDATE scan_tasks SET finished_at=? WHERE id=?`, store.NowMS(), taskID)
	throttle.SetLimit(0)
}

func (e *Engine) loadTask(ctx context.Context, id int64) (string, Options, error) {
	var (
		name    string
		raw     string
		rootRaw string
	)
	if err := e.st.Get(ctx, []any{&name, &raw, &rootRaw},
		`SELECT name, options_json, root_ids_json FROM scan_tasks WHERE id=?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", Options{}, err
		}
		return "", Options{}, err
	}
	var opts Options
	_ = json.Unmarshal([]byte(raw), &opts)
	if len(opts.RootIDs) == 0 {
		_ = json.Unmarshal([]byte(rootRaw), &opts.RootIDs)
	}
	opts.normalize(e.cfg)
	return name, opts, nil
}

func (e *Engine) pipeline(ctx context.Context, rs *runState, taskName string, opts Options) error {
	roots, err := e.targetRoots(ctx, opts.RootIDs)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return errors.New("没有可扫描的存储卷，请先在设置中添加")
	}

	// 阶段 1：索引（所有预设必做）
	if err := e.phaseIndex(ctx, rs, roots, opts); err != nil {
		return err
	}
	// 阶段 2：媒体元数据（内置解析器，零 ffmpeg；DeepProbe 时才调 ffprobe）
	if err := e.phaseProbe(ctx, rs, opts); err != nil {
		return err
	}
	// 阶段 3：快速指纹（exact 链路的第一步）
	if opts.DetectExact {
		if err := e.phaseQuick(ctx, rs, opts); err != nil {
			return err
		}
		// 阶段 4：精确校验
		if err := e.phaseExact(ctx, rs, opts); err != nil {
			return err
		}
	}
	// 阶段 5：相似度（图片 / 视频各自独立）
	if opts.DetectSimilarImage {
		if err := e.phaseImagePHash(ctx, rs, opts); err != nil {
			return err
		}
	}
	if opts.DetectSimilarVideo {
		if err := e.phaseVideoFrames(ctx, rs, opts); err != nil {
			return err
		}
	}
	// 阶段 6：分组（只构建已启用的类型）
	if err := e.phaseGroup(ctx, rs, opts); err != nil {
		return err
	}
	return nil
}

func (e *Engine) targetRoots(ctx context.Context, ids []int64) ([]Root, error) {
	all, err := e.roots.Enabled(ctx)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return all, nil
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := make([]Root, 0, len(ids))
	for _, r := range all {
		if want[r.ID] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (e *Engine) setPhase(rs *runState, phase string, total int64) {
	rs.update(func(p *Progress) {
		p.Phase = phase
		p.PhaseLabel = PhaseLabel(phase)
		p.PhaseDone = 0
		p.PhaseTotal = total
		p.Percent = 0
		p.Message = PhaseLabel(phase)
	})
}

// phaseIndex 遍历文件系统并入库。
func (e *Engine) phaseIndex(ctx context.Context, rs *runState, roots []Root, opts Options) error {
	e.setPhase(rs, PhaseIndex, 0)
	markMissing := opts.Mode == "full"

	for _, root := range roots {
		if err := rs.wait(); err != nil {
			return err
		}
		ix := NewIndexer(e.st, rs.taskID, e.cfg.BatchSize)
		walkOpts := WalkOptions{
			MaxDepth:       e.depthOrDefault(opts.MaxDepth),
			MinSize:        opts.MinFileSize,
			OnlyKinds:      opts.OnlyKinds,
			IncludeOther:   opts.IncludeOther,
			FollowSymlinks: opts.FollowSymlinks,
			ExcludeDirs:    opts.ExcludeDirs,
			ExcludeNames:   opts.ExcludeNames,
			OnDir: func(dir string) {
				rs.update(func(p *Progress) { p.CurrentPath = dir })
			},
			OnError: func(path string, err error) {
				rs.update(func(p *Progress) { p.ErrorCount++ })
				_, _ = e.st.Exec(ctx, `INSERT INTO scan_errors(task_id, path, code, message, ts) VALUES(?,?,?,?,?)`,
					rs.taskID, path, "walk", err.Error(), store.NowMS())
			},
		}
		emitErr := Walk(ctx, root.ID, root.RealPath, walkOpts, func(en Entry) error {
			if err := ix.Add(ctx, en); err != nil {
				return err
			}
			// 每 3000 个文件刷新一次进度，避免高频写库
			if seen, bytes := ix.Stats(); seen%3000 == 0 {
				rs.update(func(p *Progress) {
					p.FilesSeen = seen
					p.FilesIndexed = seen
					p.BytesScanned = bytes
					p.CurrentPath = en.Path
				})
				e.persist(rs, rs.snapshot())
			}
			return nil
		})
		_ = ix.Close(ctx, root.ID, markMissing && emitErr == nil)
		if emitErr != nil {
			if errors.Is(emitErr, context.Canceled) {
				return emitErr
			}
			return fmt.Errorf("遍历 %s 失败: %w", root.RealPath, emitErr)
		}
		seen, bytes := ix.Stats()
		rs.update(func(p *Progress) {
			p.FilesSeen += seen
			p.FilesIndexed += seen
			p.BytesScanned += bytes
			p.CurrentPath = root.RealPath
		})
	}

	// 遍历结束后补一次 inode 映射：同一卷内改名/移动不改变 inode，
	// 指纹可以直接继承，不必当成新文件重算哈希。
	// 纯优化：失败只记录，最坏情况退化成按路径判定（重新算哈希）。
	if _, err := ReconcileByInode(ctx, e.st, rs.taskID); err != nil {
		_, _ = e.st.Exec(ctx, `INSERT INTO scan_errors(task_id, path, code, message, ts) VALUES(?,?,?,?,?)`,
			rs.taskID, "", "reconcile", err.Error(), store.NowMS())
	}
	return nil
}

// phaseQuick 对候选文件计算快速采样指纹。
func (e *Engine) phaseQuick(ctx context.Context, rs *runState, opts Options) error {
	cands, err := QueryCandidates(ctx, e.st, opts.MinFileSize, firstRoot(opts.RootIDs))
	if err != nil {
		return err
	}
	need := make([]FileRef, 0, len(cands))
	for _, c := range cands {
		if c.QuickHash == "" {
			need = append(need, c)
		}
	}
	e.setPhase(rs, PhaseQuick, int64(len(need)))

	total := int64(len(cands))
	hw := newHashWriter(e.st, rs, total)
	err = parallelFor(ctx, opts.Threads, len(need), func(i int) error {
		if err := rs.wait(); err != nil {
			return err
		}
		f := need[i]
		h, err := fingerprint.Quick(f.Path, f.Size, e.cfg.QuickBlock, e.cfg.QuickSmall)
		if err != nil {
			hw.recordError(ctx, f.Path, "quick", err)
			return nil
		}
		return hw.setQuick(ctx, f.ID, h)
	})
	if err != nil {
		return err
	}
	return hw.flush(ctx)
}

// phaseExact 先比首尾窗口，再全文 BLAKE3，得到可信的 content_hash。
func (e *Engine) phaseExact(ctx context.Context, rs *runState, opts Options) error {
	cands, err := QueryCandidates(ctx, e.st, opts.MinFileSize, firstRoot(opts.RootIDs))
	if err != nil {
		return err
	}
	// 按 (size, quick_hash) 分桶，只有桶内 ≥2 才值得计算
	buckets := map[string][]FileRef{}
	key := func(f FileRef) string {
		return fmt.Sprintf("%d|%s", f.Size, f.QuickHash)
	}
	for _, c := range cands {
		if c.QuickHash == "" {
			continue
		}
		buckets[key(c)] = append(buckets[key(c)], c)
	}
	var todo []FileRef
	for _, group := range buckets {
		if len(group) < 2 {
			continue
		}
		for _, f := range group {
			if f.Content == "" {
				todo = append(todo, f)
			}
		}
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].Size > todo[j].Size })
	e.setPhase(rs, PhaseExact, int64(len(todo)))

	hw := newHashWriter(e.st, rs, int64(len(cands)))
	err = parallelFor(ctx, opts.Threads, len(todo), func(i int) error {
		if err := rs.wait(); err != nil {
			return err
		}
		f := todo[i]
		if _, err := fingerprint.HeadTail(f.Path, f.Size, e.cfg.ExactHeadTail); err != nil {
			hw.recordError(ctx, f.Path, "headtail", err)
			return nil
		}
		abort := func() bool { return rs.ctx.Err() == nil }
		sum, err := fingerprint.Full(f.Path, e.cfg.ExactChunk, abort, nil)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, fingerprint.ErrAborted) {
				return err
			}
			hw.recordError(ctx, f.Path, "full", err)
			return nil
		}
		return hw.setContent(ctx, f.ID, sum, "full")
	})
	if err != nil {
		return err
	}
	return hw.flush(ctx)
}

// phaseSimilar 计算图片 pHash 与视频关键帧指纹（保留给内部复用）。
func (e *Engine) phaseSimilar(ctx context.Context, rs *runState, opts Options) error {
	if err := e.phaseImagePHash(ctx, rs, opts); err != nil {
		return err
	}
	return e.phaseVideoFrames(ctx, rs, opts)
}

// phaseProbe 补齐缺失的媒体元数据。
//
// 默认只读文件头：MP4/MKV/AVI 走内置解析器，图片走标准库 DecodeConfig，
// 单个文件通常 < 1ms，且完全不依赖 ffmpeg。开启 DeepProbe 后才调用 ffprobe
// （能覆盖更多容器与精确码率，但每个文件会 fork 一个进程）。
func (e *Engine) phaseProbe(ctx context.Context, rs *runState, opts Options) error {
	rows, err := e.st.Select(ctx, `
		SELECT f.id, f.path, f.size, f.media_kind, f.ext
		FROM files f LEFT JOIN file_media m ON m.file_id=f.id
		WHERE f.state='active' AND m.file_id IS NULL
		  AND f.media_kind IN ('video','audio','image')
		ORDER BY f.size DESC`)
	if err != nil {
		return err
	}
	type ref struct {
		id   int64
		path string
		size int64
		kind string
		ext  string
	}
	var todo []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.path, &r.size, &r.kind, &r.ext); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(todo) == 0 {
		return nil
	}

	e.setPhase(rs, PhaseProbe, int64(len(todo)))
	hw := newHashWriter(e.st, rs, int64(len(todo)))
	useFFprobe := opts.DeepProbe && media.FFprobeAvailable()
	err = parallelFor(ctx, opts.Threads, len(todo), func(i int) error {
		if err := rs.wait(); err != nil {
			return err
		}
		r := todo[i]
		var info media.Info
		if r.kind == "image" {
			info = media.ProbeFile(r.path, "image", r.size, r.ext)
		} else if useFFprobe {
			info = media.ProbeFile(r.path, r.kind, r.size, r.ext)
		} else if n := media.ProbeNative(r.path, r.ext, r.size); n != nil {
			info = *n
		}
		if info.Source == "" || info.Source == "none" {
			// 无法解析的文件不写空记录，避免反复重试；留 NULL 供下次 DeepProbe 再试
			return hw.step(ctx)
		}
		_, err := e.st.Exec(ctx, `
			INSERT INTO file_media(file_id, duration_ms, width, height, video_codec, audio_codec,
			                       bitrate, fps, container, source, extra_json, exif_json, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(file_id) DO UPDATE SET
			  duration_ms=excluded.duration_ms, width=excluded.width, height=excluded.height,
			  video_codec=excluded.video_codec, audio_codec=excluded.audio_codec,
			  bitrate=excluded.bitrate, fps=excluded.fps, container=excluded.container,
			  source=excluded.source, extra_json=excluded.extra_json,
			  exif_json=excluded.exif_json, updated_at=excluded.updated_at`,
			r.id, info.DurationMS, info.Width, info.Height, info.VideoCodec, info.AudioCodec,
			info.Bitrate, info.FPS, info.Container, info.Source,
			nullStr(info.ExtraJSON), nullStr(info.ExifJSON), store.NowMS())
		if err != nil {
			hw.recordError(ctx, r.path, "probe", err)
		}
		if r.kind == "video" && info.DurationMS > 0 {
			_, _ = e.st.Exec(ctx, `UPDATE files SET quality_score=? WHERE id=?`,
				float64(info.Width*info.Height), r.id)
		}
		return hw.step(ctx)
	})
	if err != nil {
		return err
	}
	return hw.flush(ctx)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (e *Engine) phaseImagePHash(ctx context.Context, rs *runState, opts Options) error {
	rows, err := e.st.Select(ctx, `
		SELECT id, path, size FROM files
		WHERE state='active' AND media_kind='image' AND (phash IS NULL OR phash='')
		ORDER BY size DESC LIMIT ?`, e.cfg.SimilarImageMax)
	if err != nil {
		return err
	}
	type ref struct {
		id   int64
		path string
		size int64
	}
	var todo []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.path, &r.size); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()

	e.setPhase(rs, PhaseSimilar+"·image", int64(len(todo)))
	hw := newHashWriter(e.st, rs, int64(len(todo)))
	err = parallelFor(ctx, opts.Threads, len(todo), func(i int) error {
		if err := rs.wait(); err != nil {
			return err
		}
		h, _, _, err := sim.HashFile(todo[i].path)
		if err != nil {
			// 损坏图片/不支持格式：记录但不中断
			hw.recordError(ctx, todo[i].path, "phash", err)
			return nil
		}
		return hw.setPHash(ctx, todo[i].id, h)
	})
	if err != nil {
		return err
	}
	return hw.flush(ctx)
}

func (e *Engine) phaseVideoFrames(ctx context.Context, rs *runState, opts Options) error {
	if !media.FFmpegAvailable() {
		rs.update(func(p *Progress) { p.Message = "未检测到 ffmpeg，跳过视频相似度分析" })
		return nil
	}
	rows, err := e.st.Select(ctx, `
		SELECT f.id, f.path, COALESCE(m.duration_ms,0)
		FROM files f LEFT JOIN file_media m ON m.file_id=f.id
		WHERE f.state='active' AND f.media_kind='video'
		  AND (f.phash_frames IS NULL OR f.phash_frames='')
		ORDER BY f.size DESC LIMIT ?`, e.cfg.SimilarVideoMax)
	if err != nil {
		return err
	}
	type ref struct {
		id   int64
		path string
		dur  int64
	}
	var todo []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.path, &r.dur); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()

	e.setPhase(rs, PhaseSimilar+"·video", int64(len(todo)))
	hw := newHashWriter(e.st, rs, int64(len(todo)))
	err = parallelFor(ctx, maxInt(1, opts.Threads/2), len(todo), func(i int) error {
		if err := rs.wait(); err != nil {
			return err
		}
		r := todo[i]
		dur := r.dur
		if dur == 0 {
			info := media.ProbeFile(r.path, "video", 0, ExtOf(r.path))
			dur = info.DurationMS
			if dur > 0 {
				_, _ = e.st.Exec(ctx, `INSERT INTO file_media(file_id, duration_ms, width, height, video_codec,
					audio_codec, bitrate, fps, container, source, updated_at)
					VALUES(?,?,?,?,?,?,?,?,?,?,?)
					ON CONFLICT(file_id) DO UPDATE SET duration_ms=excluded.duration_ms, updated_at=excluded.updated_at`,
					r.id, dur, info.Width, info.Height, info.VideoCodec, info.AudioCodec,
					info.Bitrate, info.FPS, info.Container, info.Source, store.NowMS())
			}
		}
		if dur <= 0 {
			hw.recordError(ctx, r.path, "frames", errors.New("无法获取时长"))
			return nil
		}
		fp, err := sim.VideoFrames(media.FFmpegPath(), r.path, dur, e.cfg.CacheDir)
		if err != nil {
			hw.recordError(ctx, r.path, "frames", err)
			return nil
		}
		if len(fp.Frames) == 0 {
			return nil
		}
		b, _ := json.Marshal(fp)
		return hw.setFrames(ctx, r.id, string(b))
	})
	if err != nil {
		return err
	}
	return hw.flush(ctx)
}

func (e *Engine) phaseGroup(ctx context.Context, rs *runState, opts Options) error {
	kinds := opts.EnabledKinds()
	e.setPhase(rs, PhaseGroup, int64(len(kinds)+1))
	return e.buildGroups(ctx, rs, kinds)
}

// ---------- 工具 ----------

// depthOrDefault 处理扫描深度：0 表示不限（但受系统上限保护）。
func (e *Engine) depthOrDefault(v int) int {
	if v <= 0 {
		return e.cfg.MaxScanDepth
	}
	if v > e.cfg.MaxScanDepth {
		return e.cfg.MaxScanDepth
	}
	return v
}

func firstRoot(ids []int64) int64 {
	if len(ids) == 1 {
		return ids[0]
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// parallelFor 是带错误短路的有界并发执行器。
func parallelFor(ctx context.Context, workers, total int, fn func(i int) error) error {
	if total == 0 {
		return nil
	}
	if workers <= 0 {
		workers = 1
	}
	if workers > total {
		workers = total
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		idx     int64 = -1
		errOnce sync.Once
		firstErr error
		failMu  sync.Mutex
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(atomic.AddInt64(&idx, 1))
				if i >= total || ctx.Err() != nil {
					return
				}
				if err := fn(i); err != nil {
					failMu.Lock()
					errOnce.Do(func() { firstErr = err })
					failMu.Unlock()
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	failMu.Lock()
	defer failMu.Unlock()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// hashWriter 批量写入指纹结果，减少 SQLite 写放大。
type hashWriter struct {
	st  *store.Store
	rs  *runState
	tx  *sql.Tx
	n   int
	tot int64
	done int64

	mu sync.Mutex
}

func newHashWriter(st *store.Store, rs *runState, total int64) *hashWriter {
	return &hashWriter{st: st, rs: rs, tot: total}
}

func (w *hashWriter) begin(ctx context.Context) error {
	if w.tx != nil {
		return nil
	}
	tx, err := w.st.RW().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	w.tx = tx
	return nil
}

func (w *hashWriter) step(ctx context.Context) error {
	w.done++
	if w.done%50 == 0 {
		w.rs.update(func(p *Progress) {
			p.PhaseDone = w.done
			p.PhaseTotal = w.tot
		})
	}
	return nil
}

func (w *hashWriter) exec(ctx context.Context, q string, args ...any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.begin(ctx); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(ctx, q, args...); err != nil {
		return err
	}
	w.n++
	if w.n >= 500 {
		if err := w.tx.Commit(); err != nil {
			w.tx = nil
			return err
		}
		w.tx, w.n = nil, 0
	}
	return w.step(ctx)
}

func (w *hashWriter) setQuick(ctx context.Context, id int64, h string) error {
	return w.exec(ctx, `UPDATE files SET quick_hash=?, hash_status=CASE WHEN hash_status='none' THEN 'quick' ELSE hash_status END, updated_at=? WHERE id=?`,
		h, store.NowMS(), id)
}

func (w *hashWriter) setContent(ctx context.Context, id int64, h, status string) error {
	return w.exec(ctx, `UPDATE files SET content_hash=?, hash_status=?, updated_at=? WHERE id=?`,
		h, status, store.NowMS(), id)
}

func (w *hashWriter) setPHash(ctx context.Context, id int64, h string) error {
	return w.exec(ctx, `UPDATE files SET phash=?, updated_at=? WHERE id=?`, h, store.NowMS(), id)
}

func (w *hashWriter) setFrames(ctx context.Context, id int64, raw string) error {
	return w.exec(ctx, `UPDATE files SET phash_frames=?, updated_at=? WHERE id=?`, raw, store.NowMS(), id)
}

func (w *hashWriter) recordError(ctx context.Context, path, code string, err error) {
	_, _ = w.st.Exec(ctx, `INSERT INTO scan_errors(task_id, path, code, message, ts) VALUES(?,?,?,?,?)`,
		w.rs.taskID, path, code, truncate(err.Error(), 500), store.NowMS())
	w.rs.update(func(p *Progress) { p.ErrorCount++ })
}

func (w *hashWriter) flush(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tx != nil {
		err := w.tx.Commit()
		w.tx, w.n = nil, 0
		return err
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

# MediaCleaner Pro

NAS 媒体库去重与清理工具。扫描 → 分组 → 规则求值 → 人工确认 → 隔离区，全链路可回滚。

面向群晖（DSM 6/7）的 Docker 部署，同时可在 Windows / Linux 本地运行。后端 Go（单二进制、纯 Go SQLite），前端 React + TypeScript，镜像内置 ffmpeg。

---

## 1. 设计原则

**删除是最后一步，而且不是真删除。** 所有清理动作默认把文件移动进隔离区（保留 30 天可恢复），永久删除需要显式切换模式。

**检测能力互相独立。** 这是本项目的核心设计：三种能力可以任意组合，不需要为了查重复文件去解码所有视频。

| 能力 | 原理 | 依赖 | 耗时 |
|------|------|------|------|
| 完全重复 | 元数据索引 → 采样指纹 → 全文 BLAKE3 校验（三档校验强度可选） | 无 | 极低（10 万文件约 3-8 分钟） |
| 相似图片 | pHash 感知哈希聚类（纯 Go 解码） | 无 | 中（与图片数量线性） |
| 相似视频 | ffmpeg 均匀抽帧 → 逐帧 pHash → 多帧匹配 | ffmpeg | 高（与视频时长线性） |

对应的三个预设：

- **极速：仅完全重复** —— 零解码、零 ffmpeg，用来快速查同名同内容的重复视频/文件
- **标准：完全重复 + 相似图片** —— 纯 Go 实现，仍是零 ffmpeg
- **深度：含相似视频** —— 唯一会解码视频的档位，可单独开关

预设只决定默认开关，界面上每一项都能再手动微调；也可以完全不选预设、直接手动配置一次任务。

**校验强度分三档。** 同一个「完全重复」能力可以选：① 稳妥＝扫描时全文 BLAKE3 校验；② 快速＝只采样，清理前逐字节复验；③ 极速＝完全免校验，清理时也不比对（高风险，需二次确认）。三档由 `defer_verify` / `skip_verify` 两个开关组合而成，见 §4.6。注意全文哈希用的是 **BLAKE3**，隔离区文件归档用的 SHA-256 是另一回事。

**跨类型不重复计数。** 相似分组里遇到内容完全相同的文件时，只保留一份代表——它们已由「完全重复」组负责。因此 `可回收空间 = 各组非保留文件体积之和`，Dashboard、Dry Run、实际执行三处数字一致。

## 2. 安全模型

| 防线 | 实现 |
|------|------|
| 存储卷级清理开关（主闸门） | `writable=0` 的卷只索引、只展示，任何规则都无法从它产出删除项（执行阶段还有二次校验）。自动注册的卷默认关闭 |
| 媒体目录挂载模式（次闸门） | 默认读写（否则隔离区搬不动文件）；要物理级只读加 `--readonly`，此时清理动作全部失败，只剩扫描与预览 |
| 隔离区 | 移动而非删除，保留期内按原路径恢复；原路径已存在同名文件时追加时间戳而非覆盖 |
| 执行前复核 | 逐文件 `Lstat`，拒绝符号链接/非普通文件，mtime 或 size 变化一律跳过 |
| 保留文件全局保护 | 每组 keeper 与用户手动指定/钉住的路径进入保护集，任何规则都不能删 |
| 路径约束 | 注册的卷必须位于 `DataRoot` 之内；导出下载做路径穿越校验 |
| 二次确认 | 规则默认 `require_confirmation`，清理任务需显式点「执行清理」 |

## 3. 群晖部署

### 3.1 一键脚本（推荐）

把项目放到群晖任意目录（例如 `/volume1/docker/mediacleaner`），SSH 登录后：

```bash
cd /volume1/docker/mediacleaner
sudo bash deploy/synology-deploy.sh
```

**源码与数据分开**：源码目录只用于构建，运行期数据全部写到
`/volume1/docker/mediacleaner-data`（可用 `--data-dir` 改到别的卷）。

**默认端口 8083**，**默认把宿主机根目录挂载到容器 `/host`**——
这样界面里的目录选择器可以直接浏览 `/host/volume1/photo` 这类真实路径，
不用为每个目录单独写挂载。只想开放媒体目录时加 `--no-host-root`。

```text
/volume1/docker/mediacleaner            源码（本次 git 上传的内容）
/volume1/docker/mediacleaner-data       数据（备份只需要这个目录）
├─ data/     SQLite 库与索引
├─ config/   运行配置、initial-admin.txt
└─ cache/    隔离区、缩略图、转码代理、导出文件
```

脚本会依次完成：

1. 检查 docker / compose，识别 CPU 架构（x86_64 原生构建，不需要模拟层）
2. 创建 `/volume1/docker/mediacleaner-data/{data,config,cache}`
3. 生成 `.env`（`MC_DATA_DIR`、随机初始密码、PUID/PGID 取当前用户、时区）
4. 自动探测 `/volume1/{photo,video,music,...}` 并写入 `MC_ROOTS` 供首次启动自动注册存储卷
   （挂载了根目录时容器内路径是 `/host/volume1/photo`；清理开关默认关闭）
5. `docker compose build`（多阶段构建：前端 → Go 二进制 → Alpine + ffmpeg）
6. `docker compose up -d` 并轮询健康检查
7. 打印访问地址、初始密码、后续三步操作

常用参数：

```bash
sudo bash deploy/synology-deploy.sh --media /volume1/photo,/volume1/film  # 预置扫描卷
sudo bash deploy/synology-deploy.sh --no-host-root  # 不挂根目录，只按 --media 精确挂载到 /media
sudo bash deploy/synology-deploy.sh --run-as-root  # 容器以 root 运行（共享目录被 ACL 锁住时用）
sudo bash deploy/synology-deploy.sh --readonly   # 只读挂载（只能扫描，清理会全部失败）
sudo bash deploy/synology-deploy.sh --data-dir /volume2/mc-data  # 数据放到另一个卷
sudo bash deploy/synology-deploy.sh --port 8088  # 改映射端口（默认 8083）
sudo bash deploy/synology-deploy.sh --arch amd64 # 显式指定目标平台（默认原生）
sudo bash deploy/synology-deploy.sh --rebuild    # 重建镜像（走层缓存，只重编改动的层）
sudo bash deploy/synology-deploy.sh --no-cache   # 全量重建（重跑 npm ci / go mod download）
sudo bash deploy/synology-deploy.sh --no-build   # 跳过构建，用现有镜像启动
sudo bash deploy/synology-deploy.sh --pull       # 强制重新预拉取基础镜像
sudo bash deploy/synology-deploy.sh --mirrors "docker.1ms.run;hub.rat.dev"  # 指定镜像源顺序
sudo bash deploy/synology-deploy.sh --no-prepull # 跳过基础镜像预拉取
sudo bash deploy/synology-deploy.sh --status     # 容器与健康状态
sudo bash deploy/synology-deploy.sh --logs       # 跟踪日志
sudo bash deploy/synology-deploy.sh --down       # 停止（数据目录保留）
```

重复执行是安全的：`.env` 里已存在的键会保留，`docker-compose.override.yml`
每次按 `.env` 的 `MC_MEDIA` 重新生成（想固定媒体目录就把它写进 `.env`）。

> 脚本只依赖 DSM 自带的 bash 与 docker，不需要在 NAS 上安装 Go / Node：
> 前端与后端都在 Docker 构建阶段完成编译。

### 3.3 迭代速度

`--rebuild` **不再清缓存**，只重编改动过的层，日常改代码按这个预期：

| 改动范围 | 大约耗时 |
|------|------|
| 只改 Go 代码 / `deploy/*.sh` | 1-3 分钟 |
| 改前端 `src/`（package.json 没动） | 2-4 分钟 |
| 改了 `package.json` / `package-lock.json` / `go.sum` | 与首次相当（依赖层重建） |
| `--no-cache` 全量重建 | 8-12 分钟 |

依赖层（`npm ci`、`go mod download`、`apk add ffmpeg`）只在依赖清单变化时才重跑，
所以**不要**为了「确保干净」习惯性加 `--no-cache`。真遇到缓存相关的怪问题再用。

只改了 `.env`（端口、PUID、镜像源等），不动代码时，可以跳过构建直接重启容器：

```bash
sudo bash deploy/synology-deploy.sh --no-build
```

### 3.2 手动 compose

```bash
sudo docker compose build
sudo docker compose up -d
```

媒体目录可以在 `docker-compose.yml` 里手写，或用 `docker-compose.override.yml`（compose 会自动合并）：

```yaml
services:
  mediacleaner:
    volumes:
      # 方式一：挂载宿主机根目录，界面里可挑任意路径（部署脚本默认用这个）
      - "/:/host"
      # 方式二：精确挂载单个媒体目录
      - "/volume1/photo:/media/photo"
      - "/volume1/video:/media/video"
```

注意：容器内路径必须落在 `MC_DATA_ROOT`（默认 `/data`）或 `MC_ALLOW_PATHS`
（默认 `/media`，挂根目录时再加 `/host`）之内，否则注册会被拒绝。
媒体目录若要物理级只读，在路径后加 `:ro`，但此时清理会失败（只有扫描与预览可用）。

### 3.3 首次使用

1. 浏览器打开 `http://<NAS_IP>:8083`，用 `admin` + `.env` 里的 `MC_ADMIN_PASSWORD` 登录
2. 「媒体扫描」页直接点「浏览容器内目录并加入扫描」，在目录选择器里挑
   `/host/volume1/photo` 这类路径（挂载了根目录时），任务会自动注册该目录
3. 也可以用「系统设置 → 存储卷 → 添加」手动登记，**先保持「允许清理」关闭**
4. 选「极速：仅完全重复」跑一次，确认索引与分组正常
5. 到「完全重复」页核对结果，用 Dry Run 看命中数量与释放空间
6. 确认无误后，回到存储卷打开「允许清理」，再创建清理任务

## 4. 目录与数据

宿主机（群晖）上的布局：

```text
/volume1/docker/mediacleaner/          源码，只参与构建
/volume1/docker/mediacleaner-data/     运行期数据（由 MC_DATA_DIR 指定）
├─ data/            SQLite 库（mediacleaner.db）、索引
├─ config/          运行配置、initial-admin.txt（首次随机密码）
└─ cache/
   ├─ quarantine/   隔离区实体文件
   ├─ thumbs/       缩略图缓存
   ├─ proxy/        视频转码代理缓存
   └─ exports/      导出文件（csv/xlsx/json/html）
```

容器内路径固定为 `/data`、`/config`、`/cache`，与宿主机目录的映射由
`docker-compose.yml` 的 `${MC_DATA_DIR}` 决定，因此升级源码不会碰到数据。

备份只需要 `data/` 与 `config/`；`cache/` 可随时清空（会丢未过期的隔离文件）。

### 4.1 增量扫描：索引怎么复用

「两次扫描是同一个文件吗」由两组标识决定，都是本地文件系统的元数据，
不需要读文件内容：

| 用途 | 判定 | 说明 |
|------|------|------|
| 定位记录 | `(root_id, path)` | 唯一索引，决定 UPSERT 更新哪一行 |
| 内容是否变化 | `size` + `mtime_ns` | 两者都没变 → 沿用 `quick_hash` / `content_hash` / `phash` |
| 文件是否同一个 | `(dev, inode)` | 同卷内改名/移动不动 inode，指纹可以跟着搬 |

因此：

- **原地没动**的文件：二次扫描只做一次 UPSERT，不读任何字节；
- **改名或移动**的文件：路径变了但 `(dev, inode, size, mtime_ns)` 一致，
  遍历结束后由 `ReconcileByInode` 把旧指纹搬过来，同样不重算；
- **改过内容**的文件：`size` 或 `mtime_ns` 变了 → 指纹清空，重新计算；
- `dev` / `inode` 为 0 的环境（Windows、部分网络文件系统）自动跳过 inode 继承，
  退化为按路径判定。

想更严格一点（防止"改了内容又把 mtime 改回去"），打开 `MC_CTIME_CHECK=1`：
ctime 是 inode 变更时间，用户态改不回去。代价是 `chmod` / `chown` / `touch`
也会让指纹失效，所以默认关闭。

删除、隔离、恢复都不影响已有指纹——隔离只是移动文件，恢复时若回到原路径
仍是同一行记录。

### 4.2 精确校验：慢在哪，为什么不能靠「头尾不同」省

「完全重复」只能靠逐字节相同来证明，所以必须读内容。整条链路是三级漏斗，
**每一级只在上一级幸存者上花钱**：

| 级别 | 每个文件读取量 | 进入下一级的条件 |
|------|----------------|------------------|
| ① 大小 | 0（纯 SQL） | 库内存在另一个同大小文件 |
| ② 采样指纹 `quick_hash` | 大文件 4×64KB，≤8KB 的小文件全量 | 与某个文件同 `(size, quick_hash)` |
| ③ 全文 BLAKE3 | 整个文件（分块流式，1MB/块） | — 输出 `content_hash` |

关键在 ② 的窗口位置（`fingerprint.Quick`）：取的是 **首 64KB、1/3 处、2/3 处、
末 64KB**。也就是说，文件的开头和结尾本来就在采样范围内。

所以「不同场景的视频头尾字节不一样」这件事，在 ② 就已经被识别了——它们落进
不同的桶，**根本不会进入 ③**。反过来，凡是通过 ② 的文件，头尾必然已经相同；
再加一道首尾比对，能排除的候选数是 **0**（除非 xxhash64 碰撞，概率 2⁻⁶⁴），
只会给每个文件多读 128KB。这就是本项目不提供「首尾预筛」的原因。

由此可以推出一条重要结论：

> **③ 的读取量 = 库内真重复副本的字节总和。**

3 份 40GB 的同一部片就是 120GB 读取，机械盘 200MB/s 约 10 分钟；
1TB 的重复数据约 1–2 小时。这是「完全重复」这个结论的必要成本，
不是 bug，也没有靠采样绕过去的办法。

耗时估算：`③ 阶段总读取字节 ÷ 有效读速`。界面上「精确校验」会显示
「N 个候选文件需全文读取，共 X GB」，据此可直接算出还要等多久。
默认 4 线程（`MC_THREADS`），机械盘总吞吐通常 150–250 MB/s。

想更快，按收益排序：

1. **缩小扫描根**：候选集是「大小在库内出现过两次以上」的文件，只扫
   `/volume1/video` 比扫整卷的候选少一个数量级；
2. **确认没开 IO 限速**：任务里 `limit_mbps` 设成 5 就意味着 1 TB 要读 55 小时；
3. **别用网络挂载路径扫**：SMB/NFS 会把读速压到 10–30 MB/s，务必用容器内本地路径；
4. **第二次扫描不用重付**：`size` + `mtime_ns` 没变的文件会继承 `content_hash`
   （见 4.1），复制/移动过来的副本通常也保留 mtime，因此只有首次扫描要全读；
5. 机械盘上把 `MC_THREADS` 从 4 降到 2 有时反而更快（减少寻道抖动），可对比实测。

真正能砍掉这笔成本的办法是把校验从「扫描时」推迟到「删除前」，见 4.3。

### 4.3 延迟校验模式（极速预设默认开启）

`defer_verify=true` 时，扫描只跑到 ② 采样指纹就建组，不再读全文：

- 分组依据：`(size, quick_hash)` —— 采样窗口覆盖首 64KB、1/3、2/3、末 64KB；
- 分组证据记为 `evidence='sampled'`，界面显示黄色「未校验」徽标与说明条；
- 扫描读取量降到 **256KB/文件**，全库出结果是分钟级；
- 分组可以正常进入规则、生成待删清单、批量勾选，**不需要再跑一次全量扫描**。

安全兜底放在清理执行时（`cleaner.processOne`）：

1. 执行前重新核对 size / mtime / 普通文件 / 卷是否可写；
2. 找到该组的保留副本，确认它仍然存在（否则拒绝删除，避免删掉唯一一份）；
3. 比较「待删文件」与「保留副本」的全文哈希：
   - 两边都已有 `content_hash`（已校验的组）→ 只比字符串，**零读取**；
   - 缺哪边就只读哪边，保留副本的哈希在一次执行内缓存，多副本组只读一次；
4. 不一致 → 该条目标记为失败并说明原因，**绝不删除**；其余条目照常继续。

所以延迟校验的读取量 ∝ 你实际删掉的文件，而不是全库重复数据。3 份 40GB 的
副本，全删两份也只读 3 份；只删一份则读 2 份。

| 入口 | 位置 | 说明 |
|------|------|------|
| 扫描开关 | 扫描页 →「跳过全文校验（快很多）」 | 极速预设默认开，标准/深度默认关；也可手动改 |
| 分组标记 | 分组页 →「未校验」徽标 | 提示该组是「高度可能重复」，可先预览播放确认 |
| 执行开关 | 清理任务确认弹窗 →「跳过逐字节校验，直接执行」 | 默认**不勾**。勾上后完全不读内容直接执行，风险自负，会写入审计日志 |
| 批次级开关 | 扫描页 →「完全免校验：这批直接不比对」 | 默认关；勾选需二次确认，本批分组直接标为「免校验」，见 §4.6 |

取舍很清楚：延迟校验把分组语义从「已证明重复」降级为「高度可能重复」。
误判概率极低（同大小 + 4 段分散采样全撞上），但执行前的比对才是最终凭据；
主动跳过比对意味着你自己承担这个概率。

### 4.4 视频预览与快捷批量清理

**预览默认走原文件直链**，不再一律送进转码：

- `/api/files/{id}/raw` 由 `http.ServeContent` 输出，支持 Range（拖动进度条必需），
  带正确的 `Content-Type`，零转码、零等待、不占缓存磁盘；
- 可直接播放：mp4 / m4v / mov / webm 容器 + H.264 / VP9 / AV1 视频 + AAC / MP3 / Opus 音轨；
- 不能直接播放：mkv / avi / ts 容器、HEVC(H.265) 视频、DTS / AC3 / TrueHD 音轨。
  这时前端给出明确说明，并提供「生成转码预览」与「下载原文件」两个出口；
- 转码只在手动点击后触发，由后台 goroutine 执行，`GET /api/files/{id}/proxy?probe=1`
  非阻塞地返回状态与百分比，完成后自动开始播放，结果缓存到 `cache/proxy/` 复用；
- 10 秒看门狗：容器/编码不被支持而浏览器既不报错也不加载时，自动切到说明卡片，
  不会再出现「一直转圈」。

> 旧实现把每个视频都指向 `/proxy`，并在 HTTP 请求里同步跑完整片 x264；
> 转码中还会返回 `202 + JSON`，浏览器把 2xx 当视频字节流、又不触发 `error`，
> 表现就是永久卡在加载圈。现在这两条路径都已拆开。

**分组结果可以直接批量清理，不需要重新全文扫描**：

| 入口 | 作用 |
|------|------|
| 汇总条下方「全选本页」 | 把当前页所有非保留副本一次性勾选 |
| 汇总条下方「一键清理全部…」 | 后端按 `group_kind` 把该类型**全部**分组（不限当前页）的非保留副本纳入计划，创建任务后**立即执行** |
| 底部「移入隔离区」 | 手动勾选后同样是创建即执行，不再需要自己跑去清理页点「执行」 |

执行完成后弹窗可直接跳转清理任务页看进度。安全约束不变：每组保留 1 份、
在任意分组中被标记为保留的文件会被排除、默认隔离区模式可在保留期内恢复，
`sampled` 分组在执行前仍会与保留副本逐字节比对，不一致的自动跳过。

### 4.5 同组保留条件（每组保留哪一份）

在「完全重复 / 相似图片 / 相似视频」页面的汇总条下方新增「同组保留条件」卡片：
选好条件点「一键智能设定保留项」即可按条件重算该类型**全部**分组的保留文件；
每张分组卡片也可单独点「按条件重选」。条件按分组类型持久化（保存在 `settings.keeper_rules`），
重新扫描导致分组重建后会自动恢复。

#### 判定口径

所有条件共用同一套候选与并列规则：

- **候选集**：只取该组内 `files.state='active'` 的成员。已隔离 / 已删除 / 丢失的文件
  不能作为保留副本，也不会被选为 keeper；
- **并列（主键相同）**：先按 `path` 字典序升序，再按 `file_id` 升序。结果完全确定性，
  不依赖数据库返回顺序，可复现；
- **保护最后一份**：组内有效成员只剩 1 个时强制保留它，无论选了什么条件，防止整组被删空；
- **未配置（空串）**：不干预。沿用原有行为 —— 用户手动指定的 `is_keeper`，
  或扫描时的默认策略「路径层级最浅 → 路径字典序最小」。
  点「清除条件」只移除配置，**不会重算已有的保留标记**（避免把手动指定的保留文件也冲掉）；
  想完全回到扫描默认状态，重新扫描一次即可。

| 条件 ID | 名称 | 数据源 | 保留哪一个 |
|---------|------|--------|------------|
| `oldest_mtime` | 最早修改 | `files.mtime_ns` | 值最小者 |
| `newest_mtime` | 最晚修改 | `files.mtime_ns` | 值最大者 |
| `oldest_created` | 最早创建 | `files.btime_ns` | 值最小者 |
| `newest_created` | 最晚创建 | `files.btime_ns` | 值最大者 |
| `deepest_path` | 路径最深 | `files.path` | 层数最多者 |
| `shallowest_path` | 路径最浅 | `files.path` | 层数最少者（等价于原默认策略） |

- **修改时间** = `mtime_ns`，文件内容最后一次写入的时间。复制文件通常会生成新的 mtime
  （除非用 `cp -p` 之类保留时间戳），所以「最早修改」≠「最早产生」。
- **创建时间** = `btime_ns`，真实出生时间。Linux 用 `statx(STATX_BTIME)` 采集
  （`backend/internal/scan/fileinfo_linux.go`），Windows 用 `CreationTime`；
  文件系统/内核不支持时记 **0 = 未知**。`ctime_ns` 在 Linux 上是 **inode 变更时间**，
  chmod / chown / 改名都会刷新它，**不作为创建时间使用**，只用于指纹失效判定。
  - 组内只要有 ≥1 个成员已知 btime，就只在「已知」成员里比较；未知成员不参与、也不会被选为 keeper；
  - **全组都未知** → 该条件不适用，回退组内原有的 active keeper（没有则回退路径最浅），
    `keep_reason` 明确写「创建时间不可用，回退默认保留」，接口返回 `unavailable` 组数；
  - 页面会显示该类型的「创建时间可用率」，为 0 时建议改用「最早/最晚修改」。
- **路径层数** = 把路径里的 `\` 统一折算成 `/` 后统计 `/` 的个数，口径与
  `scan.pickKeeper` 一致。注意比较的是**绝对路径**，不同存储卷的根前缀长度会影响层数。

#### 适用范围

| 维度 | 范围 |
|------|------|
| 分组类型 | `exact` / `image` / `video` / `name` 各自独立配置。相似图片 / 相似视频组内内容本就不同，条件只决定「哪一份留下」，不改变相似度判定，也不会触发逐字节校验（`evidence=similar`）。 |
| 生效路径 | 分组页一键清理、手动勾选、清理任务执行、规则引擎的 `protectedIDs`（读到更新后的 `is_keeper`）都会遵守。 |
| 不适用范围 | 规则引擎里按 `retention` 权重自行选 keeper 的路径、以及「同名不同后缀」的关联文件（`assoc_of`）不受此条件控制；它们由规则自身决定。 |
| 手动优先 | 在某组手动点「保留」会**清除该组的条件**，避免执行前的条件复核把用户选中的文件再换掉；该类型的默认条件不受影响。 |
| 自动删除 | 不自动删除任何文件。条件只改 `dup_group_members.is_keeper/keep_reason`，删除仍需显式创建并执行清理任务。 |

#### 执行时序（关键）

`cleaner.ExecuteWith` 在删除循环**之前**插入 `enforceKeeperRules`：

1. `keeper.Enforce` 一次性加载所有 `dup_groups.keeper_rule<>''` 的分组及其成员；
2. 按当前成员状态（含已隔离的）用同一套 `Resolve` 重算 keeper，事务内写回
   `is_keeper` / `keep_reason`，并清掉指向已失效成员的旧 keeper；
3. 用重算后的「保护集合」过滤待删明细：命中的条目标记 `action='skipped'`、`selected=0`，
   错误栏写明「按同组保留条件判定为保留文件，已跳过删除」；
4. 其余条目同步刷新 `keeper_path`，让 `sampled` 分组的删除前逐字节比对照着**新的**保留副本做；
5. 才进入原有的 `processOne` 删除循环。

因此即使计划是在配置条件**之前**生成的、或分组已被新一轮扫描重建，也不会把刚被判为保留的那份删掉。
**未配置任何条件时保护集合为空，第 3 步整体跳过，原有去重行为逐字不变。**
复核失败会直接阻止整个任务执行，不删任何文件。

#### 接口与验证

```bash
# 查看某类型的条件、口径定义与创建时间可用率
curl -s 'http://<host>:8083/api/groups/keeper-rule?kind=exact'

# 一键按「最晚修改」重算全部完全重复分组（并持久化为默认）
curl -s -X POST 'http://<host>:8083/api/groups/keeper-rule' \
  -H 'Content-Type: application/json' -d '{"kind":"exact","rule":"newest_mtime"}'

# 只对某一组按当前条件重选；清除条件传 "rule":""
curl -s -X POST 'http://<host>:8083/api/groups/keeper-rule' \
  -H 'Content-Type: application/json' -d '{"kind":"exact","rule":"deepest_path","group_id":12}'
```

```sql
-- 核对条件是否真的落库、每组是否恰好 1 个 active keeper
SELECT g.id, g.kind, g.keeper_rule,
       SUM(CASE WHEN f.state='active' AND m.is_keeper=1 THEN 1 ELSE 0 END) AS active_keepers,
       COUNT(m.id) AS members
FROM dup_groups g JOIN dup_group_members m ON m.group_id=g.id
JOIN files f ON f.id=m.file_id
WHERE g.batch_active=1 AND g.keeper_rule<>''
GROUP BY g.id ORDER BY g.id;

-- 查看被保留文件的判定依据
SELECT group_id, file_id, keep_reason FROM dup_group_members WHERE is_keeper=1;

-- 确认执行期被保护而跳过的明细
SELECT task_id, path, action, error FROM clean_items
WHERE error LIKE '%同组保留条件%';
```

单元测试：`python tools/gorun.py test ./internal/keeper`（6 种条件、并列、btime 未知回退、保护最后一份）；
SQL 回归：`python tools/defercheck.py`。

### 4.6 完全免校验（skip_verify）与一键信任

`dup_groups.evidence` 有四个取值，它决定清理执行时要不要读内容：

| evidence | 来源 | 界面标记 | 清理时行为 |
|---|---|---|---|
| `exact` | 扫描跑过全文 BLAKE3，同组 `content_hash` 相同 | 已校验 | 两侧已有哈希，只比字符串，**零读取** |
| `sampled` | `defer_verify=true`，只比 size + 4 段采样 | 未校验（黄） | 与保留副本逐字节比对，不一致就拒绝删除 |
| `trusted` | `skip_verify=true`，或分组页「一键信任」 | 免校验（红） | **不比对内容**；仍确认保留副本是存在的普通文件 |
| `similar` | 图片/视频感知哈希聚类 | 相似度 | 本就允许内容不同，不比对 |

`skip_verify=true` 隐含 `defer_verify=true`：扫描不读全文，本批分组在清理时也不比对。
创建任务时后端要求同时传 `ack_skip_verify=true`（界面二次确认标记），把
`risk_accepted` 写进 `audit_log`，并把 `skip_verify` 持久化到 `scan_tasks.options_json`。
取値统一定义在 `store/util.go`，`scan`（生产）与 `cleaner`（消费）共用，避免循环依赖。

**风险口径（必须如实告知用户）：** 分组只依据「文件大小 + 4 段采样指纹」判定。
若存在大小相同、采样段也碰巧相同、但内容不同的文件，它们会被当成副本一起删除。
建议只对确定由复制/备份产生的目录使用，并保持默认「移入隔离区」以便恢复。
`trusted` **不是**「已全文校验」，界面不得这样显示；它只表示用户承担了误判风险。

「一键信任全部未校验分组」把**当前激活批次**里所有 `sampled` 分组改成 `trusted`，
让这些分组也能直接批量删除。它**不重新全文扫描**，只是跳过删除前的比对 —— 用户口中的
「标为已校验」指的就是这个效果，务必按真实语义解释。可随时「撤销免校验」恢复比对。

| 接口 | 说明 |
|---|---|
| `POST /api/scans` 带 `skip_verify` + `ack_skip_verify` | 建组即为 `trusted` |
| `POST /api/groups/trust-all` `{"ack":true}` | 当前批次 `sampled` → `trusted` |
| `POST /api/groups/trust-all` `{"ack":true,"restore":true}` | 反向撤销，`trusted` → `sampled` |

### 4.7 结果批次隔离（一个任务一份结果）

旧实现 `buildGroups` 先 `DELETE FROM dup_groups` 全表清空，分组又是从**全部**
`state='active'` 文件重建的，`dup_groups` 上也没有任务维度 —— 多个扫描任务的结果
因此互相覆盖、混在一起。改造后：

- `dup_groups.batch_active`：同一时刻只有一个批次为 1，旧批次置 0 归档但仍保留；
- 唯一索引从 `(kind, signature)` 改为 `(scan_task_id, kind, signature)`，不同任务
  可以产出完全相同的分组签名；升级时 `DROP INDEX ux_groups_sig` 后重建；
- `settings.active_batch_task_id` 记住当前查看哪一批（即使该批 0 个分组）；
- 新分组先以 `batch_active=0` 插入，全部建组成功后 `activate` 在**一个事务**里
  原子切换 —— 扫描中途失败时界面仍停在上一批完整结果，不会看到半截清单；
  事务内不能另起 `st.Exec`，写连接池 `MaxOpenConns=1` 会自锁；
- 归档批次只保留最近 5 个，避免长期运行时库无限增长。

所有读取 `dup_groups` 的路径（分组列表/汇总/详情、保留条件、一键清理、规则求值、
导出、统计、文件详情、系统信息）统一带 `batch_active=1`；`cleaner` 按明确的
`group_id` 查证据时不受影响（清理项持有的是创建时的具体分组）。

| 接口 | 说明 |
|---|---|
| `GET /api/groups/batches` | 列出各批次的分组数、可删数、证据分布、`skip_verify`、是否激活 |
| `POST /api/groups/batch` `{"scan_task_id":12}` | 切换当前查看批次，不重算、不重新扫描 |

```sql
-- 批次分布与激活状态（应恰好只有一个 batch_active=1）
SELECT scan_task_id, batch_active, evidence, COUNT(*)
FROM dup_groups GROUP BY scan_task_id, batch_active, evidence ORDER BY scan_task_id DESC;

-- 当前激活批次，以及 0 分组时 settings 里记的是哪一批
SELECT DISTINCT scan_task_id FROM dup_groups WHERE batch_active=1;
SELECT value_json FROM settings WHERE key='active_batch_task_id';

-- 审计：谁开了免校验 / 一键信任 / 切过批次
SELECT ts, actor, action, target_id, detail_json FROM audit_log
WHERE action IN ('scan.create','group.trust_all','group.untrust_all','group.switch_batch')
ORDER BY ts DESC LIMIT 20;
```

### 4.8 一键清理向导（新手默认入口）

首页 `/wizard` 把流程串成一条线，不必在扫描页、分组页、清理页之间来回跳：

1. **设定规则与目录** —— 勾选存储卷/目录，选检测范围、每组保留哪一份、校验严格度；
2. **查找重复文件** —— 一个按钮创建扫描任务，显示实时进度，完成后自动进下一步；
3. **核对清单** —— 汇总数字 + 逐组清单（保留文件绿标、待删副本可展开看完整路径），
   可切换历史批次、一键信任未校验分组，或跳到分组页逐个预览播放；
4. **确认清理** —— 二次确认后创建清理任务并立即执行，文件进隔离区、保留期内可恢复。

已有结果时打开首页直接落在第 3 步，不用每次重新设置。向导只自动清理「完全重复」组；
顺带扫出的相似图片/视频结果留在各自页面人工判断，避免自动误删画质不同的版本。
原有的扫描页、分组页、规则页、清理页全部保留给进阶使用。

### 4.9 旧库升级：索引必须晚于补列

`store.init()` 的顺序固定为 **`schemaSQL` → `ensureColumns()` → `ensureIndexes()`**。
旧库的表不会被 `CREATE TABLE IF NOT EXISTS` 重建，后加的列要到第 2 步才存在，
所以**静态 schema 里不能有任何引用迁移列的 `CREATE INDEX`**——否则旧库第一步就报
`apply schema: no such column: ...`，而全新安装的库（`CREATE TABLE` 自带新列）反而不报错，
极易漏测。批次相关的 `ux_groups_batch` / `ix_groups_batch` 因此统一放在
`ensureIndexes()`；`ensureColumns()` 用 `ALTER TABLE ... ADD COLUMN`（容忍「列已存在」）补齐，
`ensureIndexes()` 先 `DROP INDEX ux_groups_sig` 再重建两个批次索引。

升级不用删库：旧分组自动取 `scan_task_id=0`、`batch_active=1`，界面照常显示，数据完整保留。
回归用 `python tools/defercheck.py`（[8] 静态断言）与 `python tools/schemacheck.py`
（对真实库只读快照跑完整 init 顺序，不碰生产库）。

### 4.10 隔离区在哪，为什么「同盘移动」有时却像复制

**隔离区实体文件不在容器里**，一直在宿主机：

```text
/volume1/docker/mediacleaner-data/cache/quarantine/<哈希前2位>/<哈希>.mp4
```

`quarantine_items.stored_path` 记的是容器视角的 `/cache/quarantine/...`，
两条路径指向同一个 inode（`/cache` 就是 `${MC_DATA_DIR}/cache` 的 bind mount）。
所以容器重建、镜像升级都不会丢隔离文件；真正会丢的是把 `cache/` 目录删掉。

慢的原因是 **mount namespace**，不是磁盘：

| 挂载 | 容器内 | 宿主机 |
|------|--------|--------|
| 数据 | `/data` | `${MC_DATA_DIR}/data` |
| 缓存/隔离区 | `/cache` | `${MC_DATA_DIR}/cache` |
| 宿主机根（`--no-host-root` 时没有） | `/host` | `/` |

`rename(2)` 要求两个路径在**同一个 mount** 内，否则返回 `EXDEV`。媒体卷是
`/host/volume1/photo`、隔离区是 `/cache` —— 宿主机上明明同属 volume1，容器里却是
两个 mount，于是"移动"退化成"读一遍再写一遍"。Windows 资源管理器同盘移动瞬间完成，
是因为它走的就是同卷 rename。

`cleaner.moveOrCopy` 因此按三级降级：

1. `rename` —— 同一 mount，纯元数据操作，O(1)；
2. `link` + 删源 —— 跨 mount 但**同一文件系统**时依然只是元数据操作（`link(2)` 只要求
   同一个文件系统，不要求同一个 mount），`--no-host-root` 布局下 `/media/photo` →
   `/cache` 也能秒移；
3. 复制 —— 真的跨盘（比如媒体在 volume2、数据在 volume1）才读一遍，边写边算哈希，
   落地即完成校验，不再回读第二遍。

为了让第 1 级在默认布局下生效，`quarantineDir()` 会用 `/host` + `MC_HOST_CACHE_DIR`
拼出隔离区在宿主机视角的另一条路径（`/host/volume1/docker/.../cache/quarantine`），
再用 `os.SameFile` 确认两者是同一个目录才启用；拼不出来就退回 `/cache`，行为不变。
`stored_path` 始终写 `/cache` 视角，不会因为换了搬运方式而改库。

### 4.11 隔离文件反查主本

隔离区里的文件按内容哈希重命名（如 `ea097aca…aca9d.mov`），看不出原名。
隔离页每条记录的 **「主本」** 按钮可以在**不重新扫描**的前提下，
反查出这份内容现在还留在库里哪个位置（文件名 + 目录 + 缩略图，支持预览）。

两个等价的接口：

```
GET /api/quarantine/{id}/origin          # 按隔离记录 ID
GET /api/quarantine/origin?hash=<hash>   # 按隔离文件名（含扩展名也行，自动剥掉）
```

定位策略分两级，返回体里的 `method` 字段说明用了哪级：

| method | 含义 | 精度 |
|--------|------|------|
| `group` | 通过该隔离文件的 `orig_path` 找到它所属的重复组，再取激活批次中的保留项（keeper）| 最高——这是当初清理时真正对照过的「主本」 |
| `hash` | 组关系不可用时，退回用 `files.content_hash` 匹配仍在库（`state='active'`）的同内容文件 | 同内容但可能不是当时那个组 |
| `none` | 两级都找不到（如分组已重建 + 哈希名是 SHA-256 兜底路径产生的）| 需重新扫描才能定位 |

**为什么会有 `none`**：隔离文件名有两种来源——BLAKE3（来自 `files.content_hash`，
能和库里的指纹对上）和 SHA-256/派生键（隔离时不得不全文读一遍的兜底路径，
和库里的 BLAKE3 指纹对不上）。后者只能靠组关系反查；组再被重建过，链路就断了。
这属于已知边界，接口会在 `note` 里如实说明，不会假装找到了。

隔离页顶部的**哈希搜索框**就是这条 `?hash=` 链路的界面入口——把
`fc5d2032…ba7501`（或带扩展名的完整文件名）粘进去回车即可，不用先在列表里找到那条记录。

### 4.12 预览隔离区里的文件本身

隔离的实体文件就在 `cache/quarantine/<哈希前2位>/<哈希>.<扩展名>`，虽然已经改名，
内容完好，当然可以预览：

```
GET /api/quarantine/{id}/file    # 原文件直链（http.ServeContent，支持 Range，视频可拖进度条）
GET /api/quarantine/{id}/thumb   # 缩略图：图片直接缩放，视频 ffmpeg 抽帧
```

界面上有三处入口：列表行的 ▶ 按钮、主本反查弹窗里「隔离文件」一行的小眼睛、
以及反查出的主本卡片上的「预览」（主本仍在库里，走的是文件表那条常规链路）。

两个口径差异要注意：

- **主本预览**走 `/api/files/{id}/…`，视频解不了时有转码代理兜底；
- **隔离文件预览**只有直链，没有转码链路（转码缓存按 files 表的 id 工作，隔离文件不在表里）。
  浏览器解不了的格式（HEVC、MKV+DTS 等）会给出明确提示和「下载原文件」按钮。
- 缩略图缓存键用**负数**隔离 id，与文件表的正数 id 空间天然隔离，互不覆盖。

## 5. 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `MC_ADDR` | `:8080` | 监听地址 |
| `MC_PORT` | `8083` | compose 映射到宿主机的端口 |
| `MC_DATA_DIR` | `/volume1/docker/mediacleaner-data` | **宿主机**数据根目录（源码之外） |
| `MC_DATA_ROOT` | `/data` | 容器内的数据根（安全边界） |
| `MC_ALLOW_PATHS` | `/media` | 边界之外的额外白名单（分号分隔），媒体挂载点需要它 |
| `MC_ROOTS` | 空 | 首次启动自动注册的卷，格式 `名称=容器内路径;名称=路径` |
| `MC_ROOTS_WRITABLE` | `0` | 自动注册的卷是否直接允许清理（默认否，需在界面手动打开） |
| `MC_DB` | `/data/mediacleaner.db` | SQLite 路径 |
| `MC_CACHE_DIR` | `/cache` | 缓存与隔离区 |
| `MC_CONFIG_DIR` | `/config` | 配置目录 |
| `MC_HOST_ROOT` | `/host` | 宿主机根在容器内的挂载点；用于拼出隔离区的同 mount 路径（见 4.10） |
| `MC_HOST_DATA_DIR` | 空 | 数据根在宿主机上的路径，如 `/volume1/docker/mediacleaner-data` |
| `MC_HOST_CACHE_DIR` | 由 `MC_HOST_DATA_DIR` + `/cache` 推导 | 缓存/隔离区在宿主机上的路径 |
| `MC_ADMIN_PASSWORD` | 空 | 首次创建 admin 的口令；留空则随机生成并写文件 |
| `MC_THREADS` | `4` | 扫描并发（上限 32） |
| `MC_MAX_THREADS` | `32` | `MC_THREADS` 的上限 |
| `MC_QUICK_BLOCK` | `65536` | 采样指纹单段读取字节数（大文件共 4 段） |
| `MC_QUICK_SMALL` | `8192` | 小于该值的文件采样指纹改为全量读 |
| `MC_EXACT_HEADTAIL` | `65536` | 首尾预筛窗口大小，每文件共读 2×该值（见 4.2） |
| `MC_EXACT_CHUNK` | `1048576` | 全文哈希的分块大小 |
| `MC_QUARANTINE_DAYS` | `30` | 隔离区保留天数 |
| `MC_SESSION_HOURS` | `168` | 登录会话有效期 |
| `MC_FFMPEG` / `MC_FFPROBE` | `ffmpeg` / `ffprobe` | 二进制路径，镜像内已固定在 `/usr/bin` |
| `PUID` / `PGID` | `1026` / `100` | 运行身份，群晖上按实际情况调整 |
| `MC_SIMILAR_THRESHOLD` | `12` | 图片 pHash 汉明距离阈值（越小越严格） |
| `MC_SIMILAR_MIN_HITS` | `3` | 相似视频判定的最少帧命中数 |
| `MC_MAX_SCAN_DEPTH` | `32` | 最大扫描深度（防止误扫过深目录） |
| `MC_CTIME_CHECK` | `0` | `1` 时把 ctime 也纳入指纹失效判定（见 4.1） |

## 6. 本地开发

```bash
# 后端（需要一个 Go 1.23+）
cd backend && go build ./... && go run ./cmd/server

# 前端（另开终端，vite 代理到 :8080）
cd frontend && npm install && npm run dev

# 生产构建
cd frontend && npm run build     # 产出 frontend/dist，后端会直接托管
```

仓库内的 `tools/` 是开发期自检脚本（不参与镜像构建）：

| 脚本 | 用途 |
|------|------|
| `tools/make_testdata.py` | 生成测试媒体库（重复/相似/附属文件/唯一文件） |
| `tools/e2e.py` | 端到端验收：扫描→分组→Dry Run→隔离→恢复→只读闸门 A/B 对照 |
| `tools/dbcheck.py` | 直接读取 SQLite，核对指纹、分组、错误明细 |
| `tools/webbuild.py` | 前端类型检查 + 打包，日志落盘 |
| `tools/gorun.py` | 在受限环境下驱动 Go 构建（缓存与 telemetry 收进项目内） |

## 7. 常见问题

**Q：没有 ffmpeg 会怎样？**
完全重复与相似图片是纯 Go 实现，完全不受影响；只有「相似图片（视频）」不可用，界面会明确提示并且禁止开启该开关。官方镜像已内置 ffmpeg。

**Q：`--rebuild` 之后容器起不来，日志报端口占用。**
改 `.env` 里的 `MC_PORT`（例如 8088）后重新 `--no-build`。

**Q：扫描到了文件，但清理任务里没有内容。**
检查存储卷是否打开了「允许清理」，以及 Dry Run 的提示里是否有“因所在存储卷未开启「允许清理」被跳过”。

**Q：执行完显示「成功 0 个，失败 0 个，跳过 2444 个（只读卷），释放 0 B」。**
计划阶段就踩了只读闸门。现在一键清理（按分组类型）与手动勾选都会在**生成计划时**
就按 `roots.writable` 过滤：全被过滤会直接报「这 N 个文件所在的存储卷还没有开启
「允许清理」」，部分被过滤会在计划提示里写明数量。处理方式只有一个：到
「系统设置 → 存储卷」把该卷的「允许清理」打开，再重新生成计划。
（执行阶段的 `ErrReadOnly` 仍然保留，作为"计划生成后又改回只读"的兜底。）

**Q：隔离区文件是在容器里面吗？容器重建会不会丢？**
不在容器里。实体文件在宿主机 `${MC_DATA_DIR}/cache/quarantine/<哈希前2位>/<哈希>.mp4`，
`/cache` 是它的 bind mount，容器只是能看见它。`docker compose down` / 升级镜像都不会
动这些文件；只有把宿主机上的 `cache/` 目录删掉才会丢。详见 4.10。

**Q：volume1 的文件隔离到 volume1，为什么跟复制一样慢？Windows 上是瞬间完成的。**
因为容器里 `/host/volume1/...`（媒体）与 `/cache`（隔离区）是两个 mount，
`rename(2)` 跨 mount 一定返回 `EXDEV`，只能退化成整文件复制。现在按三级降级：
同 mount 用 rename、跨 mount 同文件系统用 link + 删源、真跨盘才复制。
带上 `MC_HOST_CACHE_DIR`（compose 已默认注入）后，默认布局走的就是 rename。详见 4.10。

**Q：清理任务能暂停 / 恢复吗？**
能。执行中的任务在详情页有「暂停」，暂停后已完成项、隔离区记录、进度都保留，
点「继续」从下一个文件接着做；想彻底放弃才用「取消执行」（已处理的不回滚）。
暂停是数据库状态挂起，不是中断进程，所以容器重启后任务会停在"已暂停"，点继续即可。

**Q：点「取消执行」没反应？**
以前有两种情况会「点了没反应」：请求失败时界面不显示错误，以及执行协程已经不在了
（容器重启过）却还去查进程内句柄，直接报错。现在：后端发现协程不在但状态还是
running/paused 时会把任务直接收尾为 canceled；界面在点击后立刻显示"正在收尾当前文件"
并保留错误提示。取消是协作式的 —— 循环在**当前这个文件**处理完（大文件隔离可能要几秒）
才停下，已处理的不会回滚。

**Q：待删清单几千项，打开任务详情浏览器就卡死。**
明细接口以前一次返回全部行。现在按执行顺序分页：默认只取 `filter=pending` 的前 60 项
（第一条就是正在处理的文件），滚到底自动再拉一页；已处理项折叠成一行
「已处理 N 项」，展开才按需加载最近处理的 50 条。计数（待执行/已处理/失败/跳过）
由服务端聚合查询给出，与分页无关，所以数字始终是全量的。
接口：`GET /api/clean/tasks/{id}/items?filter=pending|done|failed|skipped|all&limit=&offset=`
（`limit=0` 表示不分页，仅供脚本排查用）。

**Q：隔离区里的文件都改成了哈希名，还能知道它原来对应库里哪个文件吗？**
能，不用重新扫描。隔离页每条记录有「主本」按钮，按组关系（当初清理时真正对照的
保留项）反查，组关系不可用时退回指纹匹配；结果卡片带缩略图和目录，仍在库的文件
可直接预览。个别极端情况（分组重建过 + 哈希名来自兜底路径）两级都查不到，
界面会明确说明需要重新扫描。详见 4.11。

**Q：群晖上构建很慢 / OOM。**
前端打包需要 1.5G 以上内存。可在 Container Manager 里临时加大 swap，或在开发机构建好后导出镜像：

```bash
docker build -t mediacleaner:local .
docker save mediacleaner:local | gzip > mediacleaner.tar.gz
# 传到 NAS 后
gunzip -c mediacleaner.tar.gz | sudo docker load
sudo bash deploy/synology-deploy.sh --no-build
```

**Q：日志报 `打开数据库失败: apply schema: unable to open database file: out of memory (14)`。**
不是内存问题，是容器内 `/data` **不可写**——SQLite 建库返回 `SQLITE_CANTOPEN`。
入口脚本打印的 `数据目录：/data=dr-xr-xr-x` 就是证据（属主虽然是 `mcapp`，但没有 `w` 位，
常见于群晖 ACL 把目录压成 555）。宿主机上修一次即可：

```bash
sudo chown -R 1026:100 /volume1/docker/mediacleaner-data
sudo chmod -R u+rwX /volume1/docker/mediacleaner-data
sudo docker compose up -d
```

新版入口脚本会自己探测（以 PUID 身份实际试写一个文件），发现不可写就自动
`chmod -R u+rwX` 修复；部署脚本也会在起容器前校正一次属主与权限。

**Q：日志报 `打开数据库失败: apply schema: SQL logic error: no such column: batch_active (1)`。**
这**不是**权限问题，`chown` / `chmod` / 换端口都无效（与上面那条 `out of memory (14)`
是两类错误）。原因是旧库升级时静态 schema 里的索引引用了「补列之后才存在」的列，详见 4.9。
处理：确认已同步最新 `backend/internal/store/schema.go` 与 `store.go`，然后重新构建即可，
不需要删库、也不需要改权限：

```bash
sudo bash deploy/synology-deploy.sh --rebuild
```

想先在开发机上演练（只读快照，不动生产库）：

```bash
python tools/schemacheck.py            # 按真实 init 顺序跑一遍，检查列/索引/数据行数
python tools/schemacheck.py --legacy  # 额外复现修复前的 no such column 报错
```

新版部署脚本对这类错误会单独提示「数据库结构迁移失败」，不会再误判成 `/data` 不可写。

**Q：目录选择器报 `无法读取目录: open /host/volume1/xxx: permission denied`。**
挂载没问题，是**容器内运行身份没有读权限**（`PUID=1026` 对群晖共享目录通常不够，
共享目录上挂的是 Windows ACL，不是普通的 777）。三种处理方式：

```bash
# ① 最省事：容器以 root 运行（root 绕过 ACL）。写入的新文件属主也是 root。
sudo bash deploy/synology-deploy.sh --no-build --run-as-root

# ② 保持非 root：把运行身份改成目录属主
ls -ld /volume1/video              # 看属主的 uid/gid
sudo sed -i 's/^PUID=.*/PUID=<uid>/; s/^PGID=.*/PGID=<gid>/' .env
sudo docker compose up -d

# ③ 或者给目录补上读权限（会影响该目录的其他使用者，谨慎）
sudo chmod -R a+rX /volume1/video
```

新版入口脚本启动时会逐个探测 `MC_ROOTS` 里的卷能不能读，日志里直接写
「扫描卷不可读：/host/volume1/video（700 root:root），运行身份 1026:100 无权访问」。

**Q：DNS 解析失败导致 `npm ci` / `go mod download` 失败。**
在群晖「控制面板 → 网络 → 常规」里把 DNS 改成 `223.5.5.5`、`119.29.29.29`；或在 `.env` 里换 `GOPROXY`。

**Q：构建报 `403 Forbidden - toomanyrequests: 免费节点当前繁忙`。**
这是公共镜像加速器限流，跟项目代码无关。部署脚本会在构建前**依次尝试多个镜像源**预拉取
`node:20-alpine` / `golang:1.23-alpine` / `alpine:3.20`，成功后本地打回标准标签，
构建阶段就不再联网：

```bash
sudo bash deploy/synology-deploy.sh --rebuild
# 内置顺序不理想时自己指定（; 分隔，第一个失败自动换下一个）
sudo bash deploy/synology-deploy.sh --rebuild --mirrors "docker.1ms.run;hub.rat.dev;docker.1panel.live"
```

也可以完全绕开加速器，指定完整的基础镜像地址（`.env` 里改，Compose 会传给 Dockerfile）：

```ini
MC_NODE_IMAGE=docker.1ms.run/library/node:20-alpine
MC_GO_IMAGE=docker.1ms.run/library/golang:1.23-alpine
MC_RUNTIME_IMAGE=docker.1ms.run/library/alpine:3.20
```

实在拉不动时，在开发机构建好导出镜像，NAS 上 `docker load` 后 `--no-build` 启动（见上一问）。

## 8. 目录结构

```
backend/
  cmd/server/          入口
  internal/
    api/               REST + SSE + 静态托管
    scan/              遍历、索引、指纹、分组
    sim/               pHash / 聚类
    rules/             规则模型与求值
    cleaner/           清理执行与隔离区
    media/             ffmpeg/ffprobe 封装、缩略图、转码
    store/             SQLite schema 与访问
    auth/              会话与密码
frontend/src/
  pages/               9 个页面（总览/扫描/重复/相似/规则/清理/历史/隔离/统计/设置）
  components/          UI 基础组件
deploy/
  synology-deploy.sh   一键构建部署
  entrypoint.sh        PUID/PGID 处理与降权启动
```

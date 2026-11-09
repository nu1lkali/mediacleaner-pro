# MediaCleaner Pro

NAS 媒体库去重与清理工具。扫描 → 分组 → 规则求值 → 人工确认 → 隔离区，全链路可回滚。

面向群晖（DSM 6/7）的 Docker 部署，同时可在 Windows / Linux 本地运行。后端 Go（单二进制、纯 Go SQLite），前端 React + TypeScript，镜像内置 ffmpeg。

---

## 1. 设计原则

**删除是最后一步，而且不是真删除。** 所有清理动作默认把文件移动进隔离区（保留 30 天可恢复），永久删除需要显式切换模式。

**检测能力互相独立。** 这是本项目的核心设计：三种能力可以任意组合，不需要为了查重复文件去解码所有视频。

| 能力 | 原理 | 依赖 | 耗时 |
|------|------|------|------|
| 完全重复 | 元数据索引 → 采样指纹 → 全文 SHA-256 校验 | 无 | 极低（10 万文件约 3-8 分钟） |
| 相似图片 | pHash 感知哈希聚类（纯 Go 解码） | 无 | 中（与图片数量线性） |
| 相似视频 | ffmpeg 均匀抽帧 → 逐帧 pHash → 多帧匹配 | ffmpeg | 高（与视频时长线性） |

对应的三个预设：

- **极速：仅完全重复** —— 零解码、零 ffmpeg，用来快速查同名同内容的重复视频/文件
- **标准：完全重复 + 相似图片** —— 纯 Go 实现，仍是零 ffmpeg
- **深度：含相似视频** —— 唯一会解码视频的档位，可单独开关

预设只决定默认开关，界面上每一项都能再手动微调；也可以完全不选预设、直接手动配置一次任务。

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
| `MC_ADMIN_PASSWORD` | 空 | 首次创建 admin 的口令；留空则随机生成并写文件 |
| `MC_THREADS` | `4` | 扫描并发（上限 32） |
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

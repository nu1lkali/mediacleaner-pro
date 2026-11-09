#!/bin/sh
# 本地/开发机构建（需要 Go 1.23+ 与 Node 20+）
#
# 群晖上不用这个脚本：直接 `sudo bash deploy/synology-deploy.sh`
# 在容器里构建，宿主机不需要装 Go/Node。
#
# 用法：
#   sh build.sh                 # 本机平台
#   GOOS=linux GOARCH=amd64 sh build.sh   # 交叉编译给群晖 x86 用
set -e
cd "$(dirname "$0")"

GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"

echo "==> 前端（产物 frontend/dist，后端直接托管）"
cd frontend
npm ci
npm run build

# 自检：Tailwind 走 PostCSS 插件，缺 postcss.config.js 会「构建成功但没有样式」
if ! ls dist/assets/*.css >/dev/null 2>&1; then
    echo "✗ 构建产物缺少 CSS：dist/assets/*.css" >&2
    exit 1
fi
if grep -q "@tailwind" dist/assets/*.css; then
    echo "✗ CSS 未经 Tailwind 编译（@tailwind 被原样输出），请检查 frontend/postcss.config.js" >&2
    exit 1
fi
cd ..

echo "==> 后端（${GOOS}/${GOARCH}，CGO_ENABLED=0 静态二进制）"
mkdir -p bin
cd backend
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags "-s -w" -o ../bin/mediacleaner ./cmd/server
cd ..

echo "==> 完成：bin/mediacleaner"
echo "    本地运行：MC_ADMIN_PASSWORD=改成你的密码 ./bin/mediacleaner"
echo "    群晖部署：sudo bash deploy/synology-deploy.sh（容器内构建，无需本机 Go/Node）"

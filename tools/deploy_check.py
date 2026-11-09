#!/usr/bin/env python
"""容器环境变量专项验证（不需要 Docker，直接跑本地二进制）。

验证三件在 NAS 上才会暴露的问题：
  1. MC_ROOTS 自动注册的存储卷，默认「不允许清理」——Dry Run 必须为 0 项
  2. MC_ALLOW_PATHS 白名单生效：不在 DataRoot 之内的媒体挂载点能被注册
  3. 白名单缺失时，同一个挂载点必须被拒绝（防止把 / 注册成卷）

用法：
    python tools/deploy_check.py
"""
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request
from http.cookiejar import CookieJar
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
EXE = ROOT / "backend" / "bin" / "mediacleaner.exe"
LOG = ROOT / ".tools" / "deploy_check.log"
ADMIN_PWD = "Passw0rd!234"
PORT = 18081
BASE = f"http://127.0.0.1:{PORT}/api"

lines: list[str] = []


def log(msg: str = "") -> None:
    print(msg, flush=True)
    lines.append(msg)
    LOG.write_text("\n".join(lines) + "\n", encoding="utf-8")


class Client:
    def __init__(self) -> None:
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))

    def call(self, method: str, path: str, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(BASE + path, data=data, method=method)
        if data:
            req.add_header("Content-Type", "application/json")
        try:
            with self.opener.open(req, timeout=30) as resp:
                raw = resp.read().decode("utf-8", "replace")
                return resp.status, (json.loads(raw) if raw.strip() else None)
        except urllib.error.HTTPError as e:
            raw = e.read().decode("utf-8", "replace")
            try:
                return e.code, json.loads(raw)
            except json.JSONDecodeError:
                return e.code, raw


def wait_health(deadline: float) -> bool:
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(f"{BASE}/health", timeout=2) as r:
                if r.status == 200:
                    return True
        except Exception:  # noqa: BLE001
            time.sleep(0.3)
    return False


class Server:
    """按给定环境变量起一个独立实例（独立 db/cache/config，独立端口）。"""

    def __init__(self, name: str, extra_env: dict, media: Path, data: Path) -> None:
        base = ROOT / ".tools" / "deploy_check" / name
        if base.exists():
            shutil.rmtree(base)
        base.mkdir(parents=True)
        env = dict(os.environ)
        env.update(
            MC_HOME=str(ROOT),
            MC_ADDR=f"127.0.0.1:{PORT}",
            MC_ADMIN_PASSWORD=ADMIN_PWD,
            MC_DATA_ROOT=str(data),
            MC_DB=str(base / "test.db"),
            MC_CACHE_DIR=str(base / "cache"),
            MC_CONFIG_DIR=str(base / "config"),
        )
        env.pop("MC_ALLOW_PATHS", None)
        env.pop("MC_ROOTS", None)
        env.update(extra_env)
        self.proc = subprocess.Popen([str(EXE)], env=env, cwd=str(ROOT),
                                     stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                     text=True, encoding="utf-8", errors="replace")

    def __enter__(self):
        if not wait_health(time.time() + 30):
            out = self.proc.stdout.read() if self.proc.stdout else ""
            raise SystemExit("服务未就绪：\n" + out)
        return self

    def __exit__(self, *exc):
        self.proc.terminate()
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()


def login(c: Client) -> None:
    st, r = c.call("POST", "/auth/login",
                   {"username": "admin", "password": ADMIN_PWD, "remember": True})
    if st != 200:
        raise SystemExit(f"登录失败：{st} {r}")


def main() -> int:
    LOG.write_text("", encoding="utf-8")
    if not EXE.exists():
        log("缺少 backend/bin/mediacleaner.exe，请先运行 tools/e2e.py --build")
        return 2

    src = ROOT / "testdata"
    if not src.exists():
        log("缺少 testdata，请先运行 tools/make_testdata.py")
        return 2

    work = ROOT / ".tools" / "deploy_check"
    media = work / "media"
    data = work / "database"          # 故意与 media 不同目录：模拟容器里 /data 与 /media
    if media.exists():
        shutil.rmtree(media)
    shutil.copytree(src, media)
    data.mkdir(parents=True, exist_ok=True)

    # ---------- 场景 A：带 MC_ALLOW_PATHS + MC_ROOTS ----------
    log("[A] 模拟容器环境：MC_ROOTS 自动注册 + MC_ALLOW_PATHS 白名单 …")
    with Server("a", {"MC_ALLOW_PATHS": str(media),
                      "MC_ROOTS": f"photo={media / 'v1'}"}, media, data):
        c = Client()
        login(c)
        st, roots = c.call("GET", "/roots")
        items = roots.get("items") or []
        log(f"      GET /roots -> {st} 卷数={len(items)}")
        for r in items:
            log(f"        - {r['name']} path={r['real_path']} writable={r['writable']}")
        assert len(items) == 1, "MC_ROOTS 未自动注册"
        assert items[0]["writable"] is False, "自动注册的卷不应默认允许清理"

        # Dry Run 依赖分组数据，先跑一次扫描（标准预设：完全重复 + 相似图片）
        st, task = c.call("POST", "/scans", {
            "name": "deploy-check", "mode": "incremental", "profile": "standard",
            "root_ids": [items[0]["id"]], "min_file_size": 1, "threads": 2,
        })
        log(f"      POST /scans -> {st} id={task.get('id') if st == 200 else task}")
        assert st == 200, "扫描任务创建失败"
        tid = task["id"]
        deadline = time.time() + 90
        state = ""
        while time.time() < deadline:
            st, t = c.call("GET", f"/scans/{tid}")
            state = t.get("status", "")
            if state in ("succeeded", "failed", "cancelled"):
                break
            time.sleep(0.4)
        log(f"      扫描结束: {state} - {t.get('message')}")
        assert state == "succeeded", "扫描未成功完成"

        st, dry = c.call("POST", "/rules/dry-run", {"rule_ids": [], "max_items": 100})
        log(f"      Dry Run（只读卷）-> 命中 {dry.get('total_items')} 项；"
            f"提示={dry.get('notes')}")
        assert dry.get("total_items") == 0, "只读卷竟然产出了删除项"

        st, upd = c.call("PATCH", f"/roots/{items[0]['id']}", {"writable": True})
        log(f"      PATCH 允许清理 -> {st}")
        st, dry2 = c.call("POST", "/rules/dry-run", {"rule_ids": [], "max_items": 100})
        log(f"      Dry Run（可写卷）-> 命中 {dry2.get('total_items')} 项")
        assert (dry2.get("total_items") or 0) > 0, "开启允许清理后仍无命中，闸门逻辑异常"
        log("      ✓ 自动注册默认关闭；打开开关后才有删除项")

    # ---------- 场景 B：缺 MC_ALLOW_PATHS，越界路径必须被拒绝 ----------
    log("[B] 移除白名单后，同一挂载点必须注册失败 …")
    data2 = work / "database2"
    data2.mkdir(parents=True, exist_ok=True)
    with Server("b", {"MC_ROOTS": f"photo={media / 'v1'}"}, media, data2):
        c = Client()
        login(c)
        st, roots = c.call("GET", "/roots")
        items = roots.get("items") or []
        log(f"      GET /roots -> {st} 卷数={len(items)}（预期 0）")
        assert len(items) == 0, "越界路径被注册成功，DataRoot 边界失效"
        st, err = c.call("POST", "/roots", {"name": "x", "path": str(media / "v1")})
        log(f"      POST /roots 越界路径 -> {st} {err.get('error') if isinstance(err, dict) else err}")
        assert st >= 400, "越界路径未被拒绝"
        log("      ✓ 边界与白名单都按预期工作")

    log("")
    log("容器环境变量验证通过，结果已写入 .tools/deploy_check.log")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except AssertionError as e:
        log(f"断言失败：{e}")
        sys.exit(1)

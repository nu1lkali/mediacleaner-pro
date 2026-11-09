#!/usr/bin/env python
"""端到端验收：构建 → 启动 → 登录 → 扫描 → 分组 → Dry Run → 隔离 → 恢复。

全程只用标准库，结果写入 .tools/e2e.log，便于在杀软干扰下仍能取回结论。
用法：
    python tools/e2e.py            # 使用已构建的 exe
    python tools/e2e.py --build    # 先重新构建
"""
import http.cookiejar
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
GO = ROOT / ".tools" / "go" / "bin" / "go.exe"
EXE = ROOT / "backend" / "bin" / "mediacleaner.exe"
LOG = ROOT / ".tools" / "e2e.log"
ADDR = "127.0.0.1:18080"
BASE = f"http://{ADDR}/api"
ADMIN_PWD = "Passw0rd!234"

lines: list[str] = []


def log(msg: str = "") -> None:
    print(msg, flush=True)
    lines.append(msg)
    LOG.write_text("\n".join(lines) + "\n", encoding="utf-8")


def go_env() -> dict:
    env = dict(os.environ)
    tools = ROOT / ".tools"
    env.update(
        GOROOT=str(tools / "go"),
        GOPROXY="https://goproxy.cn",
        GOSUMDB="off",
        GOFLAGS="-mod=mod",
        GOTOOLCHAIN="local",
        GOMODCACHE=str(tools / "gomodcache"),
        GOCACHE=str(tools / "gocache"),
        GOTMPDIR=str(tools / "gotmp"),
        # 关掉遥测与用户级目录写入：沙箱/杀软环境下更干净
        GOTELEMETRY="off",
        GOTELEMETRYDIR=str(tools / "gotelemetry"),
        CGO_ENABLED="0",
    )
    (tools / "gotmp").mkdir(parents=True, exist_ok=True)
    (tools / "gotelemetry").mkdir(parents=True, exist_ok=True)
    return env


def build() -> None:
    log("[1/9] 构建后端二进制 …")
    EXE.parent.mkdir(parents=True, exist_ok=True)
    cmd = [str(GO), "build", "-trimpath", "-ldflags", "-s -w",
           "-o", str(EXE), "./cmd/server"]
    p = subprocess.run(cmd, cwd=str(ROOT / "backend"), env=go_env(),
                       capture_output=True, text=True, encoding="utf-8", errors="replace")
    if p.returncode != 0:
        log((p.stdout or "") + (p.stderr or ""))
        raise SystemExit("构建失败")
    log(f"      构建完成 {EXE.stat().st_size // 1024} KB")


class Client:
    def __init__(self) -> None:
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))

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
            time.sleep(0.4)
    return False


def main() -> int:
    LOG.write_text("", encoding="utf-8")
    if "--build" in sys.argv or not EXE.exists():
        build()

    data_root = ROOT / "testdata"
    if not data_root.exists():
        log("缺少 testdata，请先运行 tools/make_testdata.py")
        return 2

    # 验收会把文件移入隔离区，必须用副本，保证样本库可重复使用
    sandbox = ROOT / ".tools" / "e2e" / "media"
    if sandbox.exists():
        shutil.rmtree(sandbox)
    shutil.copytree(data_root, sandbox)
    data_root = sandbox

    env = dict(os.environ)
    env.update(
        MC_HOME=str(ROOT),
        MC_DATA_ROOT=str(data_root),
        MC_ADDR=ADDR,
        MC_ADMIN_PASSWORD=ADMIN_PWD,
        MC_DB=str(ROOT / ".tools" / "e2e" / "test.db"),
        MC_CACHE_DIR=str(ROOT / ".tools" / "e2e" / "cache"),
        MC_CONFIG_DIR=str(ROOT / ".tools" / "e2e" / "config"),
    )
    db = Path(env["MC_DB"])
    if db.exists():
        db.unlink()

    log("[2/9] 启动服务 …")
    proc = subprocess.Popen([str(EXE)], env=env, cwd=str(ROOT),
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                            text=True, encoding="utf-8", errors="replace")
    try:
        if not wait_health(time.time() + 30):
            log("服务未在 30s 内就绪，输出如下：")
            log(proc.stdout.read() if proc.stdout else "")
            return 3

        c = Client()
        log("[3/9] 健康检查与登录 …")
        st, health = c.call("GET", "/health")
        log(f"      GET /health -> {st} {health}")
        st, boot = c.call("GET", "/system/bootstrap")
        log(f"      初始化状态: {boot.get('initialized')} 版本 {boot.get('version')}")

        st, login = c.call("POST", "/auth/login",
                           {"username": "admin", "password": ADMIN_PWD, "remember": True})
        log(f"      POST /auth/login -> {st} 用户={login.get('user', {}).get('username')}")
        if st != 200:
            return 4

        st, me = c.call("GET", "/auth/me")
        log(f"      GET /auth/me -> {st} 角色={me.get('user', {}).get('role')}")

        log("[4/9] 注册两个存储卷（模拟跨卷，开启允许清理）…")
        ids = []
        for sub in ("v1", "v2"):
            st, root = c.call("POST", "/roots",
                              {"name": sub, "path": str(data_root / sub), "writable": True})
            log(f"      POST /roots {sub} -> {st} id={root.get('id') if st == 200 else root}")
            if st == 200:
                ids.append(root["id"])

        log("[5/9] 创建扫描任务（标准预设：完全重复 + 相似图片）…")
        st, task = c.call("POST", "/scans", {
            "name": "e2e 验收扫描",
            "mode": "incremental",
            "profile": "standard",
            "root_ids": ids,
            "min_file_size": 1,
            "threads": 4,
        })
        log(f"      POST /scans -> {st} id={task.get('id') if st == 200 else task}")
        if st != 200:
            return 5
        task_id = task["id"]

        deadline = time.time() + 90
        last = None
        while time.time() < deadline:
            st, t = c.call("GET", f"/scans/{task_id}")
            snap = (t.get("status"), t.get("phase"), t.get("files_seen"))
            if snap != last:
                log(f"      进度: {snap[0]:<10} {snap[1]:<16} 文件={snap[2]}")
                last = snap
            if t.get("status") in ("succeeded", "failed", "canceled"):
                log(f"      结束: {t.get('status')} - {t.get('message')}")
                break
            time.sleep(0.5)

        log("[6/9] 分组结果 …")
        st, summary = c.call("GET", "/groups/summary")
        log(f"      汇总: {json.dumps(summary, ensure_ascii=False)}")
        for kind in ("exact", "image", "video"):
            st, page = c.call("GET", f"/groups?kind={kind}&size=10")
            items = page.get("items", []) if st == 200 else []
            log(f"      kind={kind} 组数={page.get('total') if st == 200 else st}")
            for g in items:
                members = [m["name"] for m in g.get("members", [])]
                log(f"        #{g['id']} 成员={g['member_count']} 可释放={g['reclaimable_bytes']}B "
                    f"相似度={g['avg_similarity']} {members}")

        log("[7/9] Dry Run（规则求值，不删任何文件）…")
        st, plan = c.call("POST", "/rules/dry-run", {"rule_ids": [], "max_items": 100})
        log(f"      命中 {plan.get('total_items')} 项，涉及 {plan.get('group_count')} 组，"
            f"预计释放 {plan.get('total_bytes')} B")
        for it in plan.get("items", [])[:10]:
            log(f"        - {it['name']} ({it['media_kind']}) {it['reason']}")

        log("[8/9] 创建清理任务（隔离模式）并执行 …")
        st, ct = c.call("POST", "/clean/tasks", {
            "name": "e2e 清理", "rule_ids": [], "execute_mode": "quarantine"})
        log(f"      POST /clean/tasks -> {st} id={ct.get('id') if st == 200 else ct}")
        if st == 200:
            ctid = ct["id"]
            st, items = c.call("GET", f"/clean/tasks/{ctid}/items")
            log(f"      待清理 {items.get('total')} 项 / 已勾选 {items.get('selected')} 项 "
                f"/ {items.get('selected_bytes')} B")
            st, ex = c.call("POST", f"/clean/tasks/{ctid}/execute")
            log(f"      执行 -> {st} {ex}")
            for _ in range(60):
                st, t = c.call("GET", f"/clean/tasks/{ctid}")
                if t.get("status") in ("succeeded", "failed", "canceled"):
                    log(f"      清理结束: {t.get('status')} {t.get('message')}")
                    break
                time.sleep(0.4)

        log("[9/9] 隔离区与恢复 …")
        st, q = c.call("GET", "/quarantine?limit=20")
        log(f"      隔离区共 {q.get('total')} 条，占用 {q.get('quarantined_bytes')} B")
        if q.get("items"):
            target = q["items"][0]
            log(f"      恢复到 {target['orig_path']}")
            st, r = c.call("POST", f"/quarantine/{target['id']}/restore")
            log(f"      restore -> {st} {r}")
            log(f"      文件已回到磁盘: {Path(target['orig_path']).exists()}")

        # 留档：完整分组数据
        st, all_groups = c.call("GET", "/groups?kind=exact&size=50")
        (ROOT / ".tools" / "e2e-groups.json").write_text(
            json.dumps(all_groups, ensure_ascii=False, indent=2), encoding="utf-8")

        # ---------- 附加校验：只读卷必须挡住删除（A/B 对照） ----------
        log("[附加] 只读卷保护校验 …")
        log("      重新扫描以重建分组（上一轮已把文件移入隔离区）…")
        st, t2 = c.call("POST", "/scans", {
            "name": "e2e 只读校验扫描", "mode": "full", "profile": "standard",
            "root_ids": ids, "min_file_size": 1, "threads": 4,
        })
        if st != 200:
            log(f"      !! 重建分组失败: {t2}")
            return 6
        t2id = t2["id"]
        deadline = time.time() + 90
        while time.time() < deadline:
            st, tt = c.call("GET", f"/scans/{t2id}")
            if tt.get("status") in ("succeeded", "failed", "canceled"):
                log(f"      重建完成: {tt.get('status')} - {tt.get('message')}")
                break
            time.sleep(0.5)

        # A：可写状态下应命中
        st, planA = c.call("POST", "/rules/dry-run", {"rule_ids": [], "max_items": 100})
        log(f"      A. 卷可写 -> 命中 {planA.get('total_items')} 项（预期 > 0）")

        # B：全部改为只读后应为 0，并给出提示
        for rid in ids:
            c.call("PATCH", f"/roots/{rid}", {"writable": False})
        st, planB = c.call("POST", "/rules/dry-run", {"rule_ids": [], "max_items": 100})
        log(f"      B. 卷只读 -> 命中 {planB.get('total_items')} 项（预期 0）；"
            f"提示={planB.get('notes')}")

        for rid in ids:
            c.call("PATCH", f"/roots/{rid}", {"writable": True})

        assert planA.get("total_items", 0) > 0, "可写卷没有产出删除项，A/B 对照无效"
        assert planB.get("total_items", 0) == 0, "只读卷仍产出了删除项，安全闸门失效"
        log("      ✓ 同一批文件：可写时命中、只读时归零 —— 闸门生效")

        log("")
        log("验收完成，结果已写入 .tools/e2e.log")
        return 0
    finally:
        try:
            proc.send_signal(signal.CTRL_BREAK_EVENT if os.name == "nt" else signal.SIGTERM)
        except Exception:  # noqa: BLE001
            pass
        try:
            proc.terminate()
            proc.wait(timeout=5)
        except Exception:  # noqa: BLE001
            proc.kill()


if __name__ == "__main__":
    try:
        sys.exit(main())
    except SystemExit:
        raise
    except BaseException:  # noqa: BLE001
        import traceback

        tb = traceback.format_exc()
        print(tb, flush=True)
        lines.append("!! 异常中断 !!")
        lines.append(tb)
        LOG.write_text("\n".join(lines) + "\n", encoding="utf-8")
        sys.exit(1)

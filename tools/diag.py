#!/usr/bin/env python
"""分步诊断：逐步调用接口并把每步结果立即写盘，用于定位"中途无声退出"。

用法: python tools/diag.py
输出: .tools/diag.log （每步 flush）
"""
import http.cookiejar
import json
import os
import subprocess
import sys
import time
import traceback
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
EXE = ROOT / "backend" / "bin" / "mediacleaner.exe"
LOG = ROOT / ".tools" / "diag.log"
ADDR = "127.0.0.1:18081"
BASE = f"http://{ADDR}/api"
PWD = "Passw0rd!234"

fh = open(LOG, "w", encoding="utf-8")


def log(msg: str = "") -> None:
    fh.write(msg + "\n")
    fh.flush()
    os.fsync(fh.fileno())


class C:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))

    def call(self, method, path, body=None, timeout=30):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(BASE + path, data=data, method=method)
        if data:
            req.add_header("Content-Type", "application/json")
        t0 = time.time()
        try:
            with self.op.open(req, timeout=timeout) as r:
                raw = r.read().decode("utf-8", "replace")
                ms = int((time.time() - t0) * 1000)
                try:
                    return r.status, json.loads(raw), ms
                except json.JSONDecodeError:
                    return r.status, raw, ms
        except urllib.error.HTTPError as e:
            raw = e.read().decode("utf-8", "replace")
            ms = int((time.time() - t0) * 1000)
            try:
                return e.code, json.loads(raw), ms
            except json.JSONDecodeError:
                return e.code, raw, ms


def main() -> int:
    log(f"exe={EXE} exists={EXE.exists()}")
    if not EXE.exists():
        return 2

    work = ROOT / ".tools" / "diag"
    for sub in ("cache", "config"):
        (work / sub).mkdir(parents=True, exist_ok=True)
    db = work / "diag.db"
    if db.exists():
        db.unlink()

    env = dict(os.environ)
    env.update(
        MC_HOME=str(ROOT),
        MC_DATA_ROOT=str(ROOT / "testdata"),
        MC_ADDR=ADDR,
        MC_ADMIN_PASSWORD=PWD,
        MC_DB=str(db),
        MC_CACHE_DIR=str(work / "cache"),
        MC_CONFIG_DIR=str(work / "config"),
    )
    srv = subprocess.Popen([str(EXE)], env=env, cwd=str(ROOT),
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                           text=True, encoding="utf-8", errors="replace")

    def dump_server(tag: str) -> None:
        try:
            srv.terminate()
            out, _ = srv.communicate(timeout=5)
            log(f"--- server output ({tag}) rc={srv.returncode} ---")
            log(out[-4000:] if out else "(empty)")
        except Exception:
            srv.kill()

    try:
        # 等健康检查
        c = C()
        for _ in range(60):
            try:
                st, body, _ = c.call("GET", "/health", timeout=2)
                if st == 200:
                    log("[health] ok")
                    break
            except Exception:
                time.sleep(0.3)
        else:
            log("[health] 服务未就绪")
            dump_server("health-fail")
            return 3

        st, body, ms = c.call("POST", "/auth/login", {"username": "admin", "password": PWD, "remember": True})
        log(f"[login] {st} {ms}ms")

        ids = []
        for sub in ("v1", "v2"):
            st, body, ms = c.call("POST", "/roots", {"name": sub, "path": str(ROOT / "testdata" / sub)})
            log(f"[root {sub}] {st} {ms}ms -> {body if st != 200 else body.get('id')}")
            if st == 200:
                ids.append(body["id"])

        st, body, ms = c.call("POST", "/scans", {
            "name": "diag", "mode": "incremental", "profile": "standard",
            "root_ids": ids, "min_file_size": 1, "threads": 4,
        })
        log(f"[scan create] {st} {ms}ms -> {body if st != 200 else body.get('id')}")
        if st != 200:
            dump_server("scan-create-fail")
            return 4
        tid = body["id"]

        deadline = time.time() + 120
        last = None
        while time.time() < deadline:
            st, t, ms = c.call("GET", f"/scans/{tid}")
            snap = (t.get("status"), t.get("phase"), t.get("files_seen"))
            if snap != last:
                log(f"[scan] {snap} msg={t.get('message')!r} errs={t.get('error_count')}")
                last = snap
            if t.get("status") in ("succeeded", "failed", "canceled"):
                break
            time.sleep(0.5)

        log(f"[scan opts] probe={t.get('options')}")

        # 分组：逐类型单独请求，定位崩溃点
        for kind in ("exact", "image", "video"):
            try:
                st, page, ms = c.call("GET", f"/groups?kind={kind}&size=10")
                log(f"[groups {kind}] {st} {ms}ms total={page.get('total') if isinstance(page, dict) else page}")
                if isinstance(page, dict):
                    for g in page.get("items", []):
                        ms_ = [m["name"] for m in g.get("members", [])]
                        log(f"    #{g['id']} n={g['member_count']} sim={g['avg_similarity']} {ms_}")
            except Exception:
                log(f"[groups {kind}] 异常：\n{traceback.format_exc()}")
                dump_server(f"groups-{kind}")
                return 5

        st, summary, ms = c.call("GET", "/groups/summary")
        log(f"[summary] {st} {ms}ms {summary}")

        # 检查图片指纹是否算出来了
        st, page, ms = c.call("GET", "/groups?kind=exact&size=50")
        fids = []
        if isinstance(page, dict):
            for g in page.get("items", []):
                for m in g.get("members", []):
                    fids.append((m["file_id"], m["name"]))
        log(f"[files] 候选 {len(fids)} 个")
        for fid, nm in fids:
            st, d, ms = c.call("GET", f"/files/{fid}")
            if st == 200:
                log(f"    {nm} kind={d.get('media_kind')} phash={d.get('phash')!r} "
                    f"content={(d.get('content_hash') or '')[:12]!r} quick={(d.get('quick_hash') or '')[:12]!r} "
                    f"media={bool(d.get('media'))}")

        st, errs, ms = c.call("GET", f"/scans/{tid}/errors?limit=50")
        log(f"[scan errors] {st} {ms}ms {errs}")
        log("[done]")
        return 0
    except BaseException:
        log("!! 异常 !!")
        log(traceback.format_exc())
        dump_server("exception")
        return 1
    finally:
        try:
            srv.terminate()
            srv.wait(timeout=5)
        except Exception:
            srv.kill()
        fh.close()


if __name__ == "__main__":
    sys.exit(main())

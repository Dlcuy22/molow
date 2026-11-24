#!/usr/bin/env python3
"""Monitor a process tree (including WebView children) for CPU, memory, and app events.

Targets "molow" by default so common runs need no flags:

    python tools/process_monitor.py
    python tools/process_monitor.py --duration 30 --out stats.json
    python tools/process_monitor.py --profiler --duration 60
    python tools/process_monitor.py -p --config-dir /path/to/molow -nn

cpu_percent is normalized to the whole machine (100 = all cores busy);
cpu_percent_per_core keeps the raw psutil per-core value. Newly seen processes
are primed once so their first reported CPU reading reflects a real interval.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import math
import os
import shutil
import signal
import socket
import struct
import sys
import time
import urllib.error
import urllib.request
from collections import Counter
from dataclasses import dataclass
from datetime import datetime, timedelta
from pathlib import Path
from urllib.parse import urlparse

try:
    import psutil
except ImportError:
    sys.exit("psutil is required: python -m pip install psutil")

DEFAULT_NAME = "molow"
DEFAULT_VITE_PORT = 9245
IS_WINDOWS = sys.platform.startswith("win")
IS_LINUX = sys.platform.startswith("linux")
IS_MACOS = sys.platform == "darwin"

_WIN_PRIORITIES = {
    getattr(psutil, name): name.removesuffix("_PRIORITY_CLASS").replace("_", " ").title()
    for name in dir(psutil)
    if name.endswith("_PRIORITY_CLASS")
}

_WEBVIEW_HOSTS = frozenset({
    "msedgewebview2.exe",
    "msedge.exe",
    "chrome.exe",
    "cefviewwing.exe",
    "libcef.exe",
    "cefclient.exe",
    "electron.exe",
    "nw.exe",
})

_CHROMIUM_TYPES = frozenset({
    "renderer",
    "gpu-process",
    "crashpad-handler",
    "utility",
    "broker",
    "ppapi",
    "plugin",
    "renderer-service",
})

_WEBKIT_ROLES = {
    "webkitnetworkprocess": "network",
    "webkitwebprocessgpu": "gpu-process",
    "webkitwebprocessnetworking": "networking",
    "webkitwebprocesswebgl": "webgl",
    "webkitwebprocessdatabases": "databases",
    "webkitwebprocessstorage": "storage",
    "webkitwebprocessserviceworker": "service-worker",
    "webkitwebprocess": "renderer",
}

_INT_ATTRS = frozenset({
    "bytes",
    "files",
    "totalBytes",
    "maxBytes",
    "maxMb",
    "remaining",
    "count",
    "bars",
    "fft",
    "rate",
    "delayMs",
    "lines",
    "quality",
    "libraries",
    "playlists",
    "removed",
})

_BOOL_ATTRS = frozenset({"enabled", "open", "minimized", "synced", "middleware", "handler"})


@dataclass
class TrackedProcess:
    proc: psutil.Process
    create_time: float | None
    primed: bool = False


def now_dt() -> datetime:
    return datetime.now().astimezone()


def now_iso() -> str:
    return now_dt().isoformat(timespec="milliseconds")


def to_mb(byte_count: int | None) -> float | None:
    if byte_count is None:
        return None
    return round(byte_count / (1024 * 1024), 2)


def safe_call(func, default=None):
    try:
        return func()
    except (psutil.NoSuchProcess, psutil.ZombieProcess, psutil.AccessDenied, OSError, ValueError, AttributeError):
        return default


def format_priority(value: int | None) -> str | None:
    if value is None:
        return None
    if IS_WINDOWS:
        return _WIN_PRIORITIES.get(value, str(value))
    return f"nice {value}"


def default_config_dir() -> Path:
    if IS_WINDOWS:
        base = os.environ.get("APPDATA")
        return (Path(base) if base else Path.home() / "AppData" / "Roaming") / "molow"
    if IS_MACOS:
        return Path.home() / "Library" / "Application Support" / "molow"
    base = os.environ.get("XDG_CONFIG_HOME")
    return (Path(base) if base else Path.home() / ".config") / "molow"


def default_cache_dir() -> Path:
    if IS_WINDOWS:
        base = os.environ.get("LOCALAPPDATA")
        return (Path(base) if base else Path.home() / "AppData" / "Local") / "molow"
    if IS_MACOS:
        return Path.home() / "Library" / "Caches" / "molow"
    base = os.environ.get("XDG_CACHE_HOME")
    return (Path(base) if base else Path.home() / ".cache") / "molow"


def read_json_file(path: Path) -> dict | None:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
        return data if isinstance(data, dict) else None
    except (OSError, json.JSONDecodeError, UnicodeDecodeError):
        return None


def resolve_log_path(config_dir: Path, settings: dict | None) -> Path:
    raw = ""
    if isinstance(settings, dict):
        general = settings.get("general")
        if isinstance(general, dict) and isinstance(general.get("logFile"), str):
            raw = general["logFile"].strip()
    if raw:
        return Path(raw).expanduser().resolve()
    return (config_dir / "molow.log").resolve()


def name_matches(pname: str, want: str) -> bool:
    pname = pname.lower()
    want = want.lower()
    if pname == want or pname.startswith(want):
        return True
    # Linux truncates /proc/<pid>/comm to 15 bytes, so match a full query against a truncated name.
    return len(pname) >= 15 and want.startswith(pname)


def find_pid_by_name(name: str) -> int | None:
    want = name.lower()
    exact_stems = {want, f"{want}.exe"}
    exact: list[int] = []
    prefix: list[int] = []

    for proc in psutil.process_iter(["pid", "name"]):
        pname = (proc.info["name"] or "").lower()
        if not pname or not name_matches(pname, want):
            continue
        if pname in exact_stems:
            exact.append(proc.info["pid"])
        else:
            prefix.append(proc.info["pid"])

    # Pick the lowest PID so a parent launcher wins over its spawned children.
    candidates = exact or prefix
    return min(candidates) if candidates else None


def webkit_role(name: str, exe_base: str) -> str | None:
    # Prefer the untruncated argv[0] basename over Linux's 15-byte comm name.
    for candidate in (exe_base, name):
        if not candidate:
            continue
        exact = [p for p in _WEBKIT_ROLES if candidate.startswith(p)]
        if exact:
            return _WEBKIT_ROLES[max(exact, key=len)]
        if len(candidate) >= 15:
            trunc = [p for p in _WEBKIT_ROLES if p.startswith(candidate)]
            if trunc:
                return _WEBKIT_ROLES[min(trunc, key=len)]
    return None


def classify_webview(name: str, cmdline: list[str] | None) -> tuple[str | None, str | None]:
    """Return (webview_role, webview_owner) for a process."""
    lower = name.lower()
    cmd = cmdline or []
    exe_base = os.path.basename(cmd[0]).lower() if cmd else ""

    owner = None
    arg_type = None
    embedded = False
    for token in cmd:
        low = token.lower()
        if low.startswith("--webview-exe-name="):
            owner = token.split("=", 1)[1]
        elif low.startswith("--type="):
            arg_type = low.split("=", 1)[1]
        elif "--embedded-browser-webview" in low:
            embedded = True

    wk_role = webkit_role(lower, exe_base)
    if arg_type in _CHROMIUM_TYPES:
        return arg_type, owner
    if wk_role is not None:
        return wk_role, owner
    if lower in _WEBVIEW_HOSTS or embedded:
        return arg_type or "browser-host", owner
    return None, owner


def get_tracked(pid: int, cache: dict[int, TrackedProcess]) -> TrackedProcess:
    """Reuse cached psutil.Process objects so cpu_percent keeps its previous baseline."""
    entry = cache.get(pid)
    if entry is not None and entry.proc.is_running():
        ct = safe_call(entry.proc.create_time)
        if ct == entry.create_time:
            return entry
    proc = psutil.Process(pid)
    entry = TrackedProcess(proc=proc, create_time=safe_call(proc.create_time))
    cache[pid] = entry
    return entry


def discover_tree(
    root_pid: int,
    include_detached: bool,
    owner_hint: str,
    cache: dict[int, TrackedProcess],
) -> tuple[dict[int, TrackedProcess], dict[int, int]]:
    found: dict[int, TrackedProcess] = {}
    depth: dict[int, int] = {}

    found[root_pid] = get_tracked(root_pid, cache)
    depth[root_pid] = 0

    frontier = [root_pid]
    while frontier:
        current = frontier.pop()
        children = safe_call(lambda p=found[current].proc: p.children(recursive=False), [])
        for child in children:
            if child.pid in found:
                continue
            try:
                found[child.pid] = get_tracked(child.pid, cache)
            except (psutil.NoSuchProcess, psutil.AccessDenied):
                continue
            depth[child.pid] = depth[current] + 1
            frontier.append(child.pid)

    if include_detached and owner_hint:
        want_owner = owner_hint.lower()
        for proc in psutil.process_iter(["pid", "name", "cmdline"], ad_value=None):
            if proc.pid in found:
                continue
            role, owner = classify_webview(proc.info["name"] or "", proc.info["cmdline"])
            if role is None or not owner or want_owner not in owner.lower():
                continue
            try:
                found[proc.pid] = get_tracked(proc.pid, cache)
            except (psutil.NoSuchProcess, psutil.AccessDenied):
                continue
            depth[proc.pid] = -1

    return found, depth


def read_memory(proc: psutil.Process) -> dict[str, float | None]:
    # memory_full_info can fail with AccessDenied on Linux even when memory_info succeeds.
    mem = safe_call(proc.memory_full_info) or safe_call(proc.memory_info)
    if mem is None:
        return {"rss_mb": None, "private_mb": None, "pss_mb": None, "uss_mb": None}
    return {
        "rss_mb": to_mb(getattr(mem, "rss", None)),
        "private_mb": to_mb(getattr(mem, "private", None)),
        "pss_mb": to_mb(getattr(mem, "pss", None)),
        "uss_mb": to_mb(getattr(mem, "uss", None)),
    }


def sample_process(proc: psutil.Process, is_target: bool, depth: int, cores: int) -> dict:
    name = safe_call(proc.name, "<unknown>") or "<unknown>"
    cmdline = safe_call(proc.cmdline)
    webview_role, _ = classify_webview(name, cmdline)

    raw_cpu = safe_call(lambda: proc.cpu_percent(interval=None))
    cpu_percent = None if raw_cpu is None else round(raw_cpu / max(cores, 1), 2)
    mem = read_memory(proc)
    io = safe_call(proc.io_counters)

    return {
        "pid": proc.pid,
        "ppid": safe_call(proc.ppid),
        "name": name,
        "depth": depth,
        "role": "target" if is_target else webview_role,
        "cpu_percent": cpu_percent,
        "cpu_percent_per_core": None if raw_cpu is None else round(raw_cpu, 2),
        "rss_mb": mem["rss_mb"],
        "private_mb": mem["private_mb"],
        "pss_mb": mem["pss_mb"],
        "uss_mb": mem["uss_mb"],
        "threads": safe_call(proc.num_threads),
        "handles": safe_call(lambda: proc.num_handles()) if IS_WINDOWS else None,
        "priority": format_priority(safe_call(proc.nice)),
        "io_read_bytes": getattr(io, "read_bytes", None) if io else None,
        "io_write_bytes": getattr(io, "write_bytes", None) if io else None,
    }


def _unescape_go_quoted(text: str) -> str:
    out: list[str] = []
    i = 0
    n = len(text)
    escapes = {"n": "\n", "r": "\r", "t": "\t", "\\": "\\", '"': '"'}
    while i < n:
        ch = text[i]
        if ch == "\\" and i + 1 < n and text[i + 1] in escapes:
            out.append(escapes[text[i + 1]])
            i += 2
        else:
            out.append(ch)
            i += 1
    return "".join(out)


def _coerce_attr(key: str, value: str):
    if key in _BOOL_ATTRS:
        if value == "true":
            return True
        if value == "false":
            return False
    if key in _INT_ATTRS:
        try:
            return int(value)
        except ValueError:
            pass
    return value


def parse_logfmt_line(line: str) -> dict | None:
    """Parse a Go slog.NewTextHandler line into {timestamp, dt, level, msg, attrs}."""
    s = line.strip()
    if not s or "time=" not in s or "msg=" not in s:
        return None

    pairs: dict[str, str] = {}
    i = 0
    n = len(s)
    while i < n:
        while i < n and s[i] == " ":
            i += 1
        if i >= n:
            break
        eq = s.find("=", i)
        if eq == -1:
            break
        key = s[i:eq]
        if not key or " " in key:
            break
        i = eq + 1
        if i < n and s[i] == '"':
            i += 1
            start = i
            while i < n:
                if s[i] == "\\" and i + 1 < n:
                    i += 2
                    continue
                if s[i] == '"':
                    break
                i += 1
            val = _unescape_go_quoted(s[start:i])
            if i < n and s[i] == '"':
                i += 1
        else:
            start = i
            while i < n and s[i] != " ":
                i += 1
            val = s[start:i]
        pairs[key] = val

    raw_time = pairs.pop("time", None)
    level = pairs.pop("level", None)
    msg = pairs.pop("msg", None)
    if not raw_time or not level or msg is None:
        return None

    try:
        dt = datetime.fromisoformat(raw_time)
    except ValueError:
        return None

    attrs = {k: _coerce_attr(k, v) for k, v in pairs.items()}
    return {
        "timestamp": raw_time,
        "dt": dt,
        "level": level.upper(),
        "msg": msg,
        "attrs": attrs,
    }


def categorize_event(msg: str, attrs: dict) -> str:
    if msg.startswith(("album player:", "window:", "visualizer:")):
        return "ui"
    if attrs.get("kind") in {"state", "track", "probe", "seek"}:
        return "playback"
    if msg.startswith(("config:", "state:", "log file:", "settings:")):
        return "config"
    if msg.startswith(("trackcache:", "artwork:", "artist art:", "romanize:")):
        return "cache"
    if msg.startswith("lyrics:"):
        return "lyrics"
    if msg.startswith(("discord:", "discordrpc:")):
        return "discord"
    if msg.startswith(("mpris:", "smtc:", "system media")):
        return "media"
    if msg.startswith("library:"):
        return "library"
    if msg.startswith("ytm:"):
        return "ytm"
    if msg.startswith(("effect", "bundled effects:", "user effects:", "preset:")):
        return "effects"
    if msg.startswith(("AssetServer Info:", "Build Info:")):
        return "wails"
    return "other"


def is_highlight_event(ev: dict) -> bool:
    if ev["level"] in {"WARN", "ERROR"}:
        return True
    cat = ev["category"]
    msg = ev["msg"]
    if cat in {"ui", "config"}:
        return True
    if cat == "playback" and ev["attrs"].get("kind") in {"state", "track", "seek"}:
        return True
    if cat == "cache" and any(k in msg for k in ("stored", "evicted", "cleared", "limit updated", "pruned", "dictionary")):
        return True
    if cat == "lyrics" and any(k in msg for k in ("saving the winner", "sidecar live", "chain missed")):
        return True
    if cat == "discord" and not (msg == "discordrpc: activity set" and ev["attrs"].get("reason") == "refresh"):
        return True
    return False


def _proc_connections(proc: psutil.Process) -> list:
    getter = getattr(proc, "net_connections", None) or getattr(proc, "connections", None)
    if getter is None:
        return []
    return safe_call(lambda: getter(kind="inet"), []) or []


def _extract_cli_port(cmdline: list[str]) -> int | None:
    for idx, token in enumerate(cmdline):
        if token in {"--port", "-p"} and idx + 1 < len(cmdline):
            try:
                return int(cmdline[idx + 1])
            except ValueError:
                pass
        elif token.startswith("--port="):
            try:
                return int(token.split("=", 1)[1])
            except ValueError:
                pass
    return None


def _is_vite_cmdline(name: str, cmdline: list[str]) -> bool:
    low_name = name.lower()
    if low_name in {"vite", "vite.exe", "vite.cmd"}:
        return True
    for token in cmdline:
        low = token.replace("\\", "/").lower()
        base = low.rsplit("/", 1)[-1]
        if base in {"vite", "vite.js", "vite.mjs", "vite.cjs", "vite.cmd", "vite.exe"}:
            return True
        if "/vite/bin/vite" in low or "/.bin/vite" in low:
            return True
    return False


def _read_recent_dev_server_url(log_path: Path, min_ts: float | None) -> str | None:
    if not log_path.is_file():
        return None
    try:
        size = log_path.stat().st_size
        with log_path.open("rb") as fh:
            if size > 65536:
                fh.seek(size - 65536)
                fh.readline()
            tail = fh.read().decode("utf-8", errors="replace")
    except OSError:
        return None

    found_url: str | None = None
    for line in tail.splitlines():
        if "AssetServer Info:" not in line:
            continue
        rec = parse_logfmt_line(line)
        if rec is None:
            continue
        # Ignore startup lines from an earlier app session.
        if min_ts is not None and rec["dt"].timestamp() < min_ts - 5.0:
            continue
        url = rec["attrs"].get("devServerURL")
        found_url = str(url) if url else None
    return found_url


def _read_recent_profiler_endpoints(log_path: Path, min_ts: float | None) -> tuple[str | None, str | None]:
    if not log_path.is_file():
        return None, None
    try:
        size = log_path.stat().st_size
        with log_path.open("rb") as fh:
            if size > 262144:
                fh.seek(size - 262144)
                fh.readline()
            tail = fh.read().decode("utf-8", errors="replace")
    except OSError:
        return None, None

    pprof_url: str | None = None
    cdp_url: str | None = None
    for line in tail.splitlines():
        if "profiler:" not in line:
            continue
        rec = parse_logfmt_line(line)
        if rec is None:
            continue
        if min_ts is not None and rec["dt"].timestamp() < min_ts - 5.0:
            continue
        msg = rec["msg"]
        attrs = rec["attrs"]
        if msg == "profiler: pprof listening" and isinstance(attrs.get("addr"), str):
            addr = attrs["addr"].strip()
            pprof_url = addr if addr.startswith("http") else f"http://{addr}"
        elif msg == "profiler: cdp enabled":
            if isinstance(attrs.get("url"), str) and attrs["url"].strip():
                cdp_url = attrs["url"].strip().rstrip("/")
            elif isinstance(attrs.get("port"), int):
                cdp_url = f"http://127.0.0.1:{attrs['port']}"
    return pprof_url, cdp_url


def _repo_root_from_exe(root_exe: str | None) -> Path | None:
    if not root_exe:
        return None
    p = Path(root_exe).resolve()
    if p.parent.name.lower() == "bin":
        return p.parent.parent
    return p.parent


def detect_build_mode(
    root_pid: int,
    root_exe: str | None,
    tree_pids: set[int],
    log_path: Path | None = None,
) -> tuple[dict, dict[str, str]]:
    """Detect whether the monitored app is a dev or production build via Vite / Wails signals."""
    reasons: list[str] = []
    target_env: dict[str, str] = {}
    env_readable = False
    proc_create_time: float | None = None
    dev_url: str | None = None

    try:
        root_proc = psutil.Process(root_pid)
        proc_create_time = safe_call(root_proc.create_time)
        raw_env = safe_call(root_proc.environ)
        if raw_env is not None:
            target_env = raw_env
            env_readable = True
        if target_env.get("FRONTEND_DEVSERVER_URL"):
            dev_url = target_env["FRONTEND_DEVSERVER_URL"].strip()
            reasons.append(f"env:FRONTEND_DEVSERVER_URL={dev_url}")

        for parent in safe_call(root_proc.parents, []) or []:
            pname = (safe_call(parent.name, "") or "").lower()
            pcmd = safe_call(parent.cmdline, []) or []
            if "wails3" in pname and any(tok.lower() == "dev" for tok in pcmd):
                reasons.append(f"parent:wails3 dev (pid={parent.pid})")
                break
    except (psutil.NoSuchProcess, psutil.AccessDenied):
        pass

    if log_path is not None and dev_url is None:
        log_url = _read_recent_dev_server_url(log_path, proc_create_time)
        if log_url:
            dev_url = log_url
            reasons.append(f"log:devServerURL={log_url}")

    expected_ports: set[int] = set()
    if dev_url:
        parsed_port = urlparse(dev_url).port
        if parsed_port:
            expected_ports.add(parsed_port)
    for env_key in ("WAILS_VITE_PORT",):
        raw_port = target_env.get(env_key) or os.environ.get(env_key)
        if raw_port and raw_port.isdigit():
            expected_ports.add(int(raw_port))
    expected_ports.add(DEFAULT_VITE_PORT)

    # Collect remote ports currently connected from the monitored tree (e.g. WebView2 -> Vite).
    tree_remote_ports: set[int] = set()
    for pid in tree_pids:
        p = safe_call(lambda pid=pid: psutil.Process(pid))
        if p is None:
            continue
        for conn in _proc_connections(p):
            raddr = getattr(conn, "raddr", None)
            if raddr and getattr(raddr, "port", None):
                tree_remote_ports.add(raddr.port)

    repo_root = _repo_root_from_exe(root_exe)
    vite_match: dict | None = None
    vite_score = -1

    for proc in psutil.process_iter(["pid", "name", "cmdline", "cwd"], ad_value=None):
        pname = proc.info["name"] or ""
        pcmd = proc.info["cmdline"] or []
        if not _is_vite_cmdline(pname, pcmd):
            continue

        listen_ports: list[int] = []
        for conn in _proc_connections(proc):
            if getattr(conn, "status", None) == psutil.CONN_LISTEN:
                laddr = getattr(conn, "laddr", None)
                if laddr and getattr(laddr, "port", None):
                    listen_ports.append(laddr.port)
        cli_port = _extract_cli_port(pcmd)
        if cli_port and cli_port not in listen_ports:
            listen_ports.append(cli_port)

        pcwd = proc.info["cwd"] or ""
        score = 0
        match_reasons: list[str] = []
        if any(port in tree_remote_ports for port in listen_ports):
            score += 4
            match_reasons.append("active-connection")
        if any(port in expected_ports for port in listen_ports):
            score += 3
            match_reasons.append("wails-vite-port")
        if repo_root is not None and pcwd:
            try:
                Path(pcwd).resolve().relative_to(repo_root)
                score += 2
                match_reasons.append("same-repo")
            except ValueError:
                pass
        if "molow" in pcwd.lower() or any("molow" in tok.lower() for tok in pcmd):
            score += 1
            match_reasons.append("name-hint")

        # Tie Vite to the monitored process so a stray Vite server cannot mark a production build as dev.
        linked = (
            "active-connection" in match_reasons
            or bool(reasons)
            or (not env_readable and "same-repo" in match_reasons and "wails-vite-port" in match_reasons)
        )
        if score <= 0 or not linked:
            continue

        if score > vite_score:
            vite_score = score
            port = next((pt for pt in listen_ports if pt in expected_ports or pt in tree_remote_ports), listen_ports[0] if listen_ports else None)
            vite_match = {
                "pid": proc.info["pid"],
                "name": pname,
                "port": port,
                "cwd": pcwd or None,
                "cmdline": " ".join(pcmd[:8]),
                "matched_by": match_reasons,
            }

    if vite_match is not None:
        port_txt = f":{vite_match['port']}" if vite_match["port"] else ""
        reasons.append(f"vite:pid={vite_match['pid']}{port_txt}")
        if dev_url is None and vite_match["port"]:
            dev_url = f"http://localhost:{vite_match['port']}"

    is_dev = bool(reasons)
    return (
        {
            "mode": "dev" if is_dev else "production",
            "vite_detected": vite_match is not None or dev_url is not None,
            "dev_server_url": dev_url,
            "vite": vite_match,
            "reasons": reasons,
        },
        target_env,
    )


def flatten_dict(data: dict | None, prefix: str = "") -> dict[str, object]:
    if not isinstance(data, dict):
        return {}
    out: dict[str, object] = {}
    for key, val in data.items():
        full = f"{prefix}.{key}" if prefix else str(key)
        if isinstance(val, dict):
            out.update(flatten_dict(val, full))
        else:
            out[full] = val
    return out


def diff_dicts(before: dict | None, after: dict | None) -> list[dict]:
    flat_a = flatten_dict(before)
    flat_b = flatten_dict(after)
    changes: list[dict] = []
    for key in sorted(set(flat_a) | set(flat_b)):
        va = flat_a.get(key)
        vb = flat_b.get(key)
        if va != vb:
            changes.append({"key": key, "before": va, "after": vb})
    return changes


def snapshot_dir(root: Path, exclude: set[Path] | None = None) -> dict[str, tuple[int, int]]:
    """Capture {relative_posix_path: (size_bytes, mtime_ns)} without reading file contents."""
    snap: dict[str, tuple[int, int]] = {}
    if not root.is_dir():
        return snap
    excluded = {p.resolve() for p in (exclude or set())}

    def walk(cur: Path) -> None:
        try:
            with os.scandir(cur) as it:
                entries = list(it)
        except OSError:
            return
        for entry in entries:
            try:
                p = Path(entry.path)
                if entry.is_dir(follow_symlinks=False):
                    walk(p)
                elif entry.is_file(follow_symlinks=False):
                    resolved = p.resolve()
                    if resolved in excluded:
                        continue
                    st = entry.stat(follow_symlinks=False)
                    rel = p.relative_to(root).as_posix()
                    snap[rel] = (st.st_size, st.st_mtime_ns)
            except OSError:
                continue

    walk(root)
    return snap


def diff_file_snapshots(before: dict[str, tuple[int, int]], after: dict[str, tuple[int, int]]) -> dict:
    created = []
    modified = []
    deleted = []
    for rel in sorted(set(before) | set(after)):
        b = before.get(rel)
        a = after.get(rel)
        if b is None and a is not None:
            created.append({"path": rel, "size_bytes": a[0]})
        elif b is not None and a is None:
            deleted.append({"path": rel, "before_bytes": b[0]})
        elif b is not None and a is not None and b != a:
            modified.append({
                "path": rel,
                "before_bytes": b[0],
                "after_bytes": a[0],
                "delta_bytes": a[0] - b[0],
            })
    return {"created": created, "modified": modified, "deleted": deleted}


def summarize_cache_buckets(cache_dir: Path, snap: dict[str, tuple[int, int]]) -> dict[str, dict]:
    buckets: dict[str, dict] = {}
    for rel, (size, _mtime) in snap.items():
        bucket = rel.split("/", 1)[0] if "/" in rel else "_root"
        entry = buckets.setdefault(bucket, {"files": 0, "bytes": 0})
        entry["files"] += 1
        entry["bytes"] += size
    for bucket, entry in buckets.items():
        entry["mb"] = to_mb(entry["bytes"])
        entry["dir"] = str((cache_dir / bucket).resolve() if bucket != "_root" else cache_dir.resolve())
    return buckets


class LogTailer:
    """Tail MoLow's slog text file for records written during the active monitoring window."""

    def __init__(self, log_path: Path, window_start: datetime) -> None:
        self.log_path = log_path
        self.window_start = window_start
        self.offset = self._current_size()
        self.bytes_appended = 0
        self.truncations = 0
        self._partial = ""

    def _current_size(self) -> int:
        try:
            return self.log_path.stat().st_size
        except OSError:
            return 0

    def poll(self) -> list[dict]:
        try:
            size = self.log_path.stat().st_size
        except OSError:
            return []

        if size < self.offset:
            # ClearLogFile truncates molow.log in place; resume from offset zero.
            self.offset = 0
            self._partial = ""
            self.truncations += 1

        if size == self.offset:
            return []

        try:
            with self.log_path.open("rb") as fh:
                fh.seek(self.offset)
                chunk = fh.read(size - self.offset)
        except OSError:
            return []

        self.offset += len(chunk)
        self.bytes_appended += len(chunk)
        text = self._partial + chunk.decode("utf-8", errors="replace")
        lines = text.splitlines(keepends=True)
        if lines and not lines[-1].endswith(("\n", "\r")):
            self._partial = lines.pop()
        else:
            self._partial = ""

        events: list[dict] = []
        min_dt = self.window_start - timedelta(seconds=1)
        for line in lines:
            rec = parse_logfmt_line(line)
            if rec is None or rec["dt"] < min_dt:
                continue
            elapsed = round(max(0.0, (rec["dt"] - self.window_start).total_seconds()), 3)
            ev = {
                "timestamp": rec["timestamp"],
                "elapsed_s": elapsed,
                "level": rec["level"],
                "category": categorize_event(rec["msg"], rec["attrs"]),
                "msg": rec["msg"],
                "attrs": rec["attrs"],
            }
            events.append(ev)
        return events


def check_profiler_warnings(
    config_dir: Path,
    settings: dict | None,
    target_env: dict[str, str],
) -> tuple[str, str, bool, list[str]]:
    """Return (configured_level, effective_level, log_file_enabled, warnings)."""
    warnings: list[str] = []
    molow_debug = bool(target_env.get("MOLOW_DEBUG"))

    if settings is None:
        configured_level = "info"
        log_file_enabled = False
        warnings.append(
            f"settings.json not found in {config_dir}; app may be using defaults (logLevel='info', logFileEnabled=false)."
        )
    else:
        general = settings.get("general") if isinstance(settings.get("general"), dict) else {}
        configured_level = str(general.get("logLevel") or "info").lower()
        log_file_enabled = bool(general.get("logFileEnabled", False))

    effective_level = "debug (MOLOW_DEBUG)" if molow_debug else configured_level
    if configured_level != "debug" and not molow_debug:
        warnings.append(
            f"app log level is '{configured_level}' (not 'debug'); debug events (album player, minimize, visualizer, cache) will be missing. Set Log level = debug in MoLow Settings."
        )
    if settings is not None and not log_file_enabled:
        warnings.append(
            "log file output is disabled in settings (general.logFileEnabled=false); enable 'Write log to file' in MoLow Settings to capture app events."
        )

    return configured_level, effective_level, log_file_enabled, warnings


class ProfileTracker:
    """Correlate app logs, config/state changes, written files, caches, and build mode."""

    def __init__(
        self,
        config_dir: Path,
        cache_dir: Path,
        build_info: dict,
        target_env: dict[str, str],
        window_start: datetime,
        exclude_paths: set[Path] | None = None,
    ) -> None:
        self.config_dir = config_dir.resolve()
        self.cache_dir = cache_dir.resolve()
        self.settings_path = self.config_dir / "settings.json"
        self.state_path = self.config_dir / "state.json"
        self.build_info = build_info
        self.exclude_paths = exclude_paths or set()

        self.initial_settings = read_json_file(self.settings_path)
        self._last_settings = self.initial_settings
        self.initial_state = read_json_file(self.state_path)
        self.log_path = resolve_log_path(self.config_dir, self.initial_settings)

        (
            self.configured_log_level,
            self.effective_log_level,
            self.log_file_enabled,
            self.warnings,
        ) = check_profiler_warnings(self.config_dir, self.initial_settings, target_env)

        self._initial_config_files = snapshot_dir(self.config_dir, self.exclude_paths)
        self._initial_cache_files = snapshot_dir(self.cache_dir, self.exclude_paths)
        self._initial_cache_buckets = summarize_cache_buckets(self.cache_dir, self._initial_cache_files)

        self.tailer = LogTailer(self.log_path, window_start)
        self.events: list[dict] = []
        self.highlights: list[dict] = []
        self.config_timeline: list[dict] = []
        self._by_category: Counter[str] = Counter()
        self._by_level: Counter[str] = Counter()

        self._config_writes: list[dict] = []
        self._lyrics_writes: list[dict] = []
        self._trackcache_stores: list[dict] = []
        self._artwork_updates: list[dict] = []

        self._cache_events = {
            "trackcache": {
                "hits": 0,
                "misses": 0,
                "stored": 0,
                "stored_bytes": 0,
                "evicted": 0,
                "evicted_bytes": 0,
                "cleared": 0,
                "limit_updates": 0,
            },
            "artwork": {
                "misses": 0,
                "thumb_hits": 0,
                "thumb_cached": 0,
                "thumb_cached_bytes": 0,
                "covers_updated": 0,
                "pruned": 0,
            },
            "lyrics": {
                "sidecar_hits": 0,
                "chain_started": 0,
                "saved": 0,
                "saved_bytes": 0,
                "misses": 0,
            },
        }

    def poll(self) -> list[dict]:
        new_events = self.tailer.poll()
        for ev in new_events:
            self.events.append(ev)
            self._by_category[ev["category"]] += 1
            self._by_level[ev["level"]] += 1
            if is_highlight_event(ev):
                self.highlights.append(ev)
            self._record_event_metrics(ev)
        return new_events

    def _record_event_metrics(self, ev: dict) -> None:
        msg = ev["msg"]
        attrs = ev["attrs"]

        if msg == "config: wrote":
            self._config_writes.append({"timestamp": ev["timestamp"], "elapsed_s": ev["elapsed_s"], "path": attrs.get("path")})
            latest = read_json_file(self.settings_path)
            if latest is not None:
                changes = diff_dicts(self._last_settings, latest)
                if changes:
                    self.config_timeline.append({
                        "timestamp": ev["timestamp"],
                        "elapsed_s": ev["elapsed_s"],
                        "changes": changes,
                    })
                self._last_settings = latest
                new_log_path = resolve_log_path(self.config_dir, latest)
                if new_log_path != self.log_path:
                    self.log_path = new_log_path
                    self.tailer = LogTailer(new_log_path, self.tailer.window_start)
        elif msg == "trackcache: hit":
            self._cache_events["trackcache"]["hits"] += 1
        elif msg == "trackcache: miss":
            self._cache_events["trackcache"]["misses"] += 1
        elif msg == "trackcache: stored":
            size = attrs.get("bytes") if isinstance(attrs.get("bytes"), int) else 0
            self._cache_events["trackcache"]["stored"] += 1
            self._cache_events["trackcache"]["stored_bytes"] += size
            self._trackcache_stores.append({
                "timestamp": ev["timestamp"],
                "elapsed_s": ev["elapsed_s"],
                "id": attrs.get("id"),
                "bytes": size,
            })
        elif msg == "trackcache: evicted":
            size = attrs.get("bytes") if isinstance(attrs.get("bytes"), int) else 0
            self._cache_events["trackcache"]["evicted"] += 1
            self._cache_events["trackcache"]["evicted_bytes"] += size
        elif msg == "trackcache: cleared":
            self._cache_events["trackcache"]["cleared"] += 1
        elif msg == "trackcache: limit updated":
            self._cache_events["trackcache"]["limit_updates"] += 1
        elif msg == "artwork: miss":
            self._cache_events["artwork"]["misses"] += 1
        elif msg == "artwork: search thumb hit":
            self._cache_events["artwork"]["thumb_hits"] += 1
        elif msg == "artwork: search thumb cached in memory":
            size = attrs.get("bytes") if isinstance(attrs.get("bytes"), int) else 0
            self._cache_events["artwork"]["thumb_cached"] += 1
            self._cache_events["artwork"]["thumb_cached_bytes"] += size
        elif msg.startswith("artwork: updated "):
            self._cache_events["artwork"]["covers_updated"] += 1
            self._artwork_updates.append({
                "timestamp": ev["timestamp"],
                "elapsed_s": ev["elapsed_s"],
                "msg": msg,
                "attrs": attrs,
            })
        elif msg == "artwork: pruned unreferenced files":
            removed = attrs.get("removed") if isinstance(attrs.get("removed"), int) else 0
            self._cache_events["artwork"]["pruned"] += removed
        elif msg == "lyrics: sidecar already on disk, no fetch":
            self._cache_events["lyrics"]["sidecar_hits"] += 1
        elif msg == "lyrics: chain started":
            self._cache_events["lyrics"]["chain_started"] += 1
        elif msg == "lyrics: saving the winner":
            size = attrs.get("bytes") if isinstance(attrs.get("bytes"), int) else 0
            self._cache_events["lyrics"]["saved"] += 1
            self._cache_events["lyrics"]["saved_bytes"] += size
            self._lyrics_writes.append({
                "timestamp": ev["timestamp"],
                "elapsed_s": ev["elapsed_s"],
                "provider": attrs.get("provider"),
                "target": attrs.get("target"),
                "bytes": size,
            })
        elif msg in {"lyrics: chain missed", "lyrics: no sheet, session remembers the miss"}:
            self._cache_events["lyrics"]["misses"] += 1

    def discovered_endpoints(self) -> tuple[str | None, str | None]:
        """Return (pprof_url, cdp_url) if logged by the running app."""
        pprof_url = None
        cdp_url = None
        for ev in self.events:
            msg = ev.get("msg")
            attrs = ev.get("attrs") or {}
            if msg == "profiler: pprof listening" and isinstance(attrs.get("addr"), str):
                addr = attrs["addr"].strip()
                pprof_url = addr if addr.startswith("http") else f"http://{addr}"
            elif msg == "profiler: cdp enabled":
                if isinstance(attrs.get("url"), str) and attrs["url"].strip():
                    cdp_url = attrs["url"].strip().rstrip("/")
                elif isinstance(attrs.get("port"), int):
                    cdp_url = f"http://127.0.0.1:{attrs['port']}"
        return pprof_url, cdp_url

    def status_line(self) -> str:
        mode = self.build_info["mode"]
        if not self.events:
            return f"Profiler [{mode}]: 0 events"
        last = self.highlights[-1] if self.highlights else self.events[-1]
        return f"Profiler [{mode}]: {len(self.events)} events  last: [{last['elapsed_s']:.1f}s] {last['msg']}"

    def finalize(self) -> dict:
        self.poll()
        final_settings = read_json_file(self.settings_path)
        final_state = read_json_file(self.state_path)
        final_config_files = snapshot_dir(self.config_dir, self.exclude_paths)
        final_cache_files = snapshot_dir(self.cache_dir, self.exclude_paths)
        final_cache_buckets = summarize_cache_buckets(self.cache_dir, final_cache_files)

        disk_cache: dict[str, dict] = {}
        for bucket in sorted(set(self._initial_cache_buckets) | set(final_cache_buckets)):
            b_init = self._initial_cache_buckets.get(bucket, {"files": 0, "bytes": 0, "mb": 0.0})
            b_fin = final_cache_buckets.get(bucket, {"files": 0, "bytes": 0, "mb": 0.0})
            delta_bytes = b_fin["bytes"] - b_init["bytes"]
            disk_cache[bucket] = {
                "dir": b_fin.get("dir") or b_init.get("dir"),
                "initial": {"files": b_init["files"], "bytes": b_init["bytes"], "mb": b_init["mb"]},
                "final": {"files": b_fin["files"], "bytes": b_fin["bytes"], "mb": b_fin["mb"]},
                "delta_files": b_fin["files"] - b_init["files"],
                "delta_bytes": delta_bytes,
                "delta_mb": to_mb(delta_bytes),
            }

        return {
            "build": self.build_info,
            "warnings": self.warnings,
            "paths": {
                "config_dir": str(self.config_dir),
                "cache_dir": str(self.cache_dir),
                "settings_path": str(self.settings_path),
                "state_path": str(self.state_path),
                "log_path": str(self.log_path),
            },
            "app_config": {
                "log_level": self.configured_log_level,
                "log_level_effective": self.effective_log_level,
                "log_file_enabled": self.log_file_enabled,
                "initial": self.initial_settings,
                "final": final_settings,
                "changes": diff_dicts(self.initial_settings, final_settings),
                "timeline": self.config_timeline,
            },
            "app_state": {
                "initial": self.initial_state,
                "final": final_state,
                "changes": diff_dicts(self.initial_state, final_state),
            },
            "written": {
                "config_dir": diff_file_snapshots(self._initial_config_files, final_config_files),
                "cache_dir": diff_file_snapshots(self._initial_cache_files, final_cache_files),
                "log_bytes_appended": self.tailer.bytes_appended,
                "log_truncations": self.tailer.truncations,
                "log_events": {
                    "config_writes": self._config_writes,
                    "lyrics_writes": self._lyrics_writes,
                    "trackcache_stores": self._trackcache_stores,
                    "artwork_updates": self._artwork_updates,
                },
            },
            "cache": {
                "disk": disk_cache,
                "events": self._cache_events,
            },
            "event_counts": {
                "total": len(self.events),
                "by_category": dict(sorted(self._by_category.items())),
                "by_level": dict(sorted(self._by_level.items())),
            },
            "highlights": self.highlights,
            "events": self.events,
        }


class SummaryAccumulator:
    """Streaming summary aggregator so unlimited monitor runs stay bounded in memory."""

    def __init__(self) -> None:
        self.sample_count = 0
        self.cpu_sum = 0.0
        self.cpu_max = 0.0
        self.rss_sum = 0.0
        self.rss_max = 0.0
        self.uss_sum = 0.0
        self.uss_max = 0.0
        self.uss_count = 0
        self.peak_procs = 0
        self._per_proc: dict[tuple[int, str], dict] = {}
        self._io_bounds: dict[tuple[int, str], list[int | None]] = {}

    def add(self, sample: dict) -> None:
        self.sample_count += 1
        totals = sample["totals"]
        self.cpu_sum += totals["cpu_percent"]
        self.cpu_max = max(self.cpu_max, totals["cpu_percent"])
        self.rss_sum += totals["rss_mb"]
        self.rss_max = max(self.rss_max, totals["rss_mb"])
        uss_total = totals.get("uss_mb")
        if uss_total is not None:
            self.uss_sum += uss_total
            self.uss_max = max(self.uss_max, uss_total)
            self.uss_count += 1
        self.peak_procs = max(self.peak_procs, sample["process_count"])

        for rec in sample["processes"]:
            key = (rec["pid"], rec["name"])
            entry = self._per_proc.setdefault(
                key,
                {
                    "pid": rec["pid"],
                    "name": rec["name"],
                    "role": rec["role"],
                    "samples": 0,
                    "cpu_sum": 0.0,
                    "cpu_count": 0,
                    "cpu_max": 0.0,
                    "rss_sum": 0.0,
                    "rss_count": 0,
                    "rss_max_mb": 0.0,
                    "uss_sum": 0.0,
                    "uss_count": 0,
                    "uss_max_mb": None,
                    "private_max_mb": None,
                    "pss_max_mb": None,
                    "threads_max": 0,
                    "handles_max": None,
                },
            )
            entry["samples"] += 1

            cpu = rec["cpu_percent"]
            if cpu is not None:
                entry["cpu_sum"] += cpu
                entry["cpu_count"] += 1
                entry["cpu_max"] = max(entry["cpu_max"], cpu)

            rss = rec["rss_mb"]
            if rss is not None:
                entry["rss_sum"] += rss
                entry["rss_count"] += 1
                entry["rss_max_mb"] = max(entry["rss_max_mb"], rss)

            uss = rec.get("uss_mb")
            if uss is not None:
                entry["uss_sum"] += uss
                entry["uss_count"] += 1
                entry["uss_max_mb"] = max(entry["uss_max_mb"] or 0.0, uss)

            if rec["private_mb"] is not None:
                entry["private_max_mb"] = max(entry["private_max_mb"] or 0.0, rec["private_mb"])

            if rec["pss_mb"] is not None:
                entry["pss_max_mb"] = max(entry["pss_max_mb"] or 0.0, rec["pss_mb"])

            if rec["threads"]:
                entry["threads_max"] = max(entry["threads_max"], rec["threads"])
            if rec["handles"] is not None:
                entry["handles_max"] = max(entry["handles_max"] or 0, rec["handles"])

            # OS I/O counters are cumulative, so track first and last reading per process.
            bounds = self._io_bounds.setdefault(key, [None, None, None, None])
            if rec["io_read_bytes"] is not None:
                if bounds[0] is None:
                    bounds[0] = rec["io_read_bytes"]
                bounds[1] = rec["io_read_bytes"]
            if rec["io_write_bytes"] is not None:
                if bounds[2] is None:
                    bounds[2] = rec["io_write_bytes"]
                bounds[3] = rec["io_write_bytes"]

    def build(self) -> dict:
        processes = []
        for key, entry in self._per_proc.items():
            r0, r1, w0, w1 = self._io_bounds[key]
            read_delta = max(0, r1 - r0) if r0 is not None and r1 is not None else None
            write_delta = max(0, w1 - w0) if w0 is not None and w1 is not None else None
            processes.append({
                "pid": entry["pid"],
                "name": entry["name"],
                "role": entry["role"],
                "samples": entry["samples"],
                "cpu_avg": round(entry["cpu_sum"] / entry["cpu_count"], 2) if entry["cpu_count"] else 0.0,
                "cpu_max": round(entry["cpu_max"], 2),
                "rss_avg_mb": round(entry["rss_sum"] / entry["rss_count"], 2) if entry["rss_count"] else 0.0,
                "rss_max_mb": round(entry["rss_max_mb"], 2),
                "uss_avg_mb": round(entry["uss_sum"] / entry["uss_count"], 2) if entry["uss_count"] else None,
                "uss_max_mb": round(entry["uss_max_mb"], 2) if entry["uss_max_mb"] is not None else None,
                "private_max_mb": entry["private_max_mb"],
                "pss_max_mb": entry["pss_max_mb"],
                "threads_max": entry["threads_max"],
                "handles_max": entry["handles_max"],
                "io_read_mb": to_mb(read_delta),
                "io_write_mb": to_mb(write_delta),
            })

        processes.sort(key=lambda item: (-item["cpu_max"], -item["rss_max_mb"], item["pid"]))

        if self.sample_count == 0:
            totals = {"cpu_avg": 0.0, "cpu_max": 0.0, "rss_avg_mb": 0.0, "rss_max_mb": 0.0, "uss_avg_mb": None, "uss_max_mb": None, "peak_procs": 0}
        else:
            totals = {
                "cpu_avg": round(self.cpu_sum / self.sample_count, 2),
                "cpu_max": round(self.cpu_max, 2),
                "rss_avg_mb": round(self.rss_sum / self.sample_count, 2),
                "rss_max_mb": round(self.rss_max, 2),
                "uss_avg_mb": round(self.uss_sum / self.uss_count, 2) if self.uss_count else None,
                "uss_max_mb": round(self.uss_max, 2) if self.uss_count else None,
                "peak_procs": self.peak_procs,
            }

        return {"totals": totals, "processes": processes}


class CDPClient:
    """Minimal stdlib RFC 6455 WebSocket client for loopback Chrome DevTools Protocol."""

    def __init__(self, ws_url: str, timeout: float = 5.0) -> None:
        parsed = urlparse(ws_url)
        if parsed.scheme != "ws" or not parsed.hostname:
            raise ValueError(f"unsupported CDP WebSocket URL: {ws_url!r}")
        host = parsed.hostname
        port = parsed.port or 80
        path = parsed.path or "/"
        if parsed.query:
            path = f"{path}?{parsed.query}"

        self._sock = socket.create_connection((host, port), timeout=timeout)
        self._sock.settimeout(timeout)
        self._next_id = 1
        self._closed = False

        key = base64.b64encode(os.urandom(16)).decode("ascii")
        expected_accept = base64.b64encode(
            hashlib.sha1(f"{key}258EAFA5-E914-47DA-95CA-C5AB0DC85B11".encode("ascii")).digest()
        ).decode("ascii")

        req = (
            f"GET {path} HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            "Sec-WebSocket-Version: 13\r\n\r\n"
        )
        self._sock.sendall(req.encode("ascii"))

        buf = b""
        while b"\r\n\r\n" not in buf:
            chunk = self._sock.recv(4096)
            if not chunk:
                raise ConnectionError("CDP WebSocket handshake closed early")
            buf += chunk

        header_raw, extra = buf.split(b"\r\n\r\n", 1)
        lines = header_raw.decode("latin1").split("\r\n")
        if not lines or "101" not in lines[0]:
            raise ConnectionError(f"CDP WebSocket handshake rejected: {lines[0] if lines else ''}")

        headers = {}
        for line in lines[1:]:
            if ":" in line:
                k, v = line.split(":", 1)
                headers[k.strip().lower()] = v.strip()
        if headers.get("sec-websocket-accept") != expected_accept:
            raise ConnectionError("CDP WebSocket Sec-WebSocket-Accept mismatch")

        self._recv_buf = bytearray(extra)

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        try:
            self._send_frame(0x8, b"")
        except OSError:
            pass
        try:
            self._sock.close()
        except OSError:
            pass

    def _recv_exact(self, n: int) -> bytes:
        while len(self._recv_buf) < n:
            chunk = self._sock.recv(max(4096, n - len(self._recv_buf)))
            if not chunk:
                raise ConnectionError("CDP WebSocket connection closed")
            self._recv_buf.extend(chunk)
        out = bytes(self._recv_buf[:n])
        del self._recv_buf[:n]
        return out

    def _send_frame(self, opcode: int, payload: bytes) -> None:
        mask = os.urandom(4)
        length = len(payload)
        header = bytearray([0x80 | (opcode & 0x0F)])
        if length < 126:
            header.append(0x80 | length)
        elif length < 65536:
            header.append(0x80 | 126)
            header.extend(struct.pack("!H", length))
        else:
            header.append(0x80 | 127)
            header.extend(struct.pack("!Q", length))
        header.extend(mask)
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        self._sock.sendall(bytes(header) + masked)

    def _recv_message(self) -> str | None:
        fragments: list[bytes] = []
        while True:
            b1, b2 = self._recv_exact(2)
            fin = bool(b1 & 0x80)
            opcode = b1 & 0x0F
            masked = bool(b2 & 0x80)
            length = b2 & 0x7F
            if length == 126:
                (length,) = struct.unpack("!H", self._recv_exact(2))
            elif length == 127:
                (length,) = struct.unpack("!Q", self._recv_exact(8))
            mask = self._recv_exact(4) if masked else None
            payload = self._recv_exact(length)
            if mask is not None:
                payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))

            if opcode == 0x8:
                self._closed = True
                return None
            if opcode == 0x9:
                self._send_frame(0xA, payload)
                continue
            if opcode == 0xA:
                continue
            if opcode in (0x1, 0x2, 0x0):
                fragments.append(payload)
                if fin:
                    return b"".join(fragments).decode("utf-8", errors="replace")

    def call(
        self,
        method: str,
        params: dict | None = None,
        *,
        timeout: float = 10.0,
        on_event=None,
    ) -> dict:
        if self._closed:
            raise ConnectionError("CDP WebSocket is closed")
        self._sock.settimeout(timeout)
        req_id = self._next_id
        self._next_id += 1
        req = {"id": req_id, "method": method, "params": params or {}}
        self._send_frame(0x1, json.dumps(req).encode("utf-8"))

        while True:
            raw = self._recv_message()
            if raw is None:
                raise ConnectionError("CDP WebSocket closed while waiting for response")
            msg = json.loads(raw)
            if "id" not in msg:
                if on_event is not None:
                    on_event(msg)
                continue
            if msg["id"] != req_id:
                continue
            if "error" in msg:
                err = msg["error"]
                raise RuntimeError(f"CDP {method} failed: {err.get('message', err)}")
            return msg.get("result") or {}


def _http_get_bytes(url: str, timeout: float = 5.0) -> bytes:
    with urllib.request.urlopen(url, timeout=timeout) as resp:
        return resp.read()


def _http_get_json(url: str, timeout: float = 3.0):
    return json.loads(_http_get_bytes(url, timeout=timeout).decode("utf-8"))


def discover_cdp_targets(cdp_url: str, timeout: float = 2.0) -> list[dict]:
    base = cdp_url.rstrip("/")
    data = _http_get_json(f"{base}/json/list", timeout=timeout)
    if not isinstance(data, list):
        return []
    return [
        t for t in data
        if isinstance(t, dict)
        and t.get("type") == "page"
        and isinstance(t.get("webSocketDebuggerUrl"), str)
    ]


def extract_endpoints_from_env(env: dict[str, str]) -> tuple[str | None, str | None]:
    pprof_url = None
    cdp_url = None
    pprof_addr = env.get("MOLOW_PPROF_ADDR", "").strip()
    if pprof_addr and not pprof_addr.endswith(":0"):
        pprof_url = pprof_addr if pprof_addr.startswith("http") else f"http://{pprof_addr}"
    wv_args = env.get("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "")
    for token in wv_args.split():
        if token.startswith("--remote-debugging-port="):
            port_txt = token.split("=", 1)[1].strip()
            if port_txt.isdigit():
                cdp_url = f"http://127.0.0.1:{port_txt}"
    return pprof_url, cdp_url


class RuntimeProbe:
    """Collect Go runtime/pprof data and WebView2 DevTools metrics & snapshots."""

    def __init__(
        self,
        *,
        pprof_url: str | None = None,
        cdp_url: str | None = None,
        artifacts_dir: Path | None = None,
        cpu_spike_pct: float | None = None,
    ) -> None:
        self.pprof_url = pprof_url.rstrip("/") if pprof_url else None
        self.cdp_url = cdp_url.rstrip("/") if cdp_url else None
        self.artifacts_dir = artifacts_dir
        self.cpu_spike_pct = cpu_spike_pct
        self._cdp_clients: dict[str, CDPClient] = {}
        self._cdp_labels: dict[str, str] = {}
        self.captures: list[dict] = []
        self.errors: list[str] = []
        self._go_spike_streak = 0
        self._wv_spike_streak = 0
        self._spike_captured = False

    def update_endpoints(self, pprof_url: str | None, cdp_url: str | None) -> None:
        if not self.pprof_url and pprof_url:
            self.pprof_url = pprof_url.rstrip("/")
        if not self.cdp_url and cdp_url:
            self.cdp_url = cdp_url.rstrip("/")

    def close(self) -> None:
        for client in self._cdp_clients.values():
            client.close()
        self._cdp_clients.clear()

    def _ensure_cdp_clients(self) -> None:
        if not self.cdp_url:
            return
        try:
            targets = discover_cdp_targets(self.cdp_url, timeout=1.5)
        except Exception:
            return

        active_ids: set[str] = set()
        for idx, target in enumerate(targets):
            tid = str(target.get("id") or idx)
            active_ids.add(tid)
            if tid in self._cdp_clients:
                continue
            ws_url = target["webSocketDebuggerUrl"]
            try:
                client = CDPClient(ws_url, timeout=3.0)
                client.call("Performance.enable", timeout=3.0)
                self._cdp_clients[tid] = client
                self._cdp_labels[tid] = self._target_label(target, idx)
            except Exception as exc:
                self.errors.append(f"cdp connect ({tid}): {exc}")

        for tid in list(self._cdp_clients):
            if tid not in active_ids:
                self._cdp_clients.pop(tid).close()
                self._cdp_labels.pop(tid, None)

    @staticmethod
    def _target_label(target: dict, idx: int) -> str:
        url = str(target.get("url") or "")
        if "window=effects" in url:
            return "effects"
        title = str(target.get("title") or "").strip().lower()
        if "effect" in title:
            return "effects"
        return "main" if idx == 0 else f"page-{idx}"

    def sample(self) -> tuple[dict | None, dict | None]:
        go_runtime = self._sample_go_runtime()
        webview_metrics = self._sample_webview_metrics()
        return go_runtime, webview_metrics

    def _sample_go_runtime(self) -> dict | None:
        if not self.pprof_url:
            return None
        try:
            raw = _http_get_json(f"{self.pprof_url}/debug/runtime", timeout=1.5)
            return {
                "goroutines": raw.get("goroutines"),
                "heap_alloc_mb": to_mb(raw.get("heap_alloc_bytes")),
                "heap_inuse_mb": to_mb(raw.get("heap_inuse_bytes")),
                "stack_inuse_mb": to_mb(raw.get("stack_inuse_bytes")),
                "num_gc": raw.get("num_gc"),
                "pause_total_ms": round(raw["pause_total_ns"] / 1_000_000, 2)
                if isinstance(raw.get("pause_total_ns"), int) else None,
            }
        except Exception as exc:
            self.errors.append(f"go runtime poll: {exc}")
            return None

    def _sample_webview_metrics(self) -> dict | None:
        if not self.cdp_url:
            return None
        self._ensure_cdp_clients()
        if not self._cdp_clients:
            return None

        out: dict[str, dict] = {}
        for tid, client in list(self._cdp_clients.items()):
            label = self._cdp_labels.get(tid, tid)
            try:
                res = client.call("Performance.getMetrics", timeout=2.0)
                metrics = {
                    m["name"]: m["value"]
                    for m in res.get("metrics", [])
                    if isinstance(m, dict) and "name" in m and "value" in m
                }
                heap = client.call("Runtime.getHeapUsage", timeout=2.0)
                out[label] = {
                    "js_heap_used_mb": to_mb(heap.get("usedSize")),
                    "js_heap_total_mb": to_mb(heap.get("totalSize")),
                    "dom_nodes": int(metrics["Nodes"]) if "Nodes" in metrics else None,
                    "js_event_listeners": int(metrics["JSEventListeners"]) if "JSEventListeners" in metrics else None,
                    "script_duration_ms": round(metrics["ScriptDuration"] * 1000, 2) if "ScriptDuration" in metrics else None,
                    "layout_duration_ms": round(metrics["LayoutDuration"] * 1000, 2) if "LayoutDuration" in metrics else None,
                    "task_duration_ms": round(metrics["TaskDuration"] * 1000, 2) if "TaskDuration" in metrics else None,
                }
            except Exception as exc:
                self.errors.append(f"cdp metrics ({label}): {exc}")
                self._cdp_clients.pop(tid, None)
                client.close()
        return out or None

    def _ensure_artifacts_dir(self) -> Path:
        if self.artifacts_dir is None:
            self.artifacts_dir = Path(f"molow-profile-artifacts-{now_dt().strftime('%Y%m%d-%H%M%S')}")
        self.artifacts_dir.mkdir(parents=True, exist_ok=True)
        return self.artifacts_dir

    def capture_go_pprof(self, kind: str, *, tag: str, seconds: int = 10) -> dict | None:
        if not self.pprof_url:
            return None
        out_dir = self._ensure_artifacts_dir()
        if kind == "profile":
            url = f"{self.pprof_url}/debug/pprof/profile?seconds={seconds}"
            timeout = seconds + 10.0
            filename = f"go-cpu-{tag}.pb.gz"
        elif kind == "heap":
            url = f"{self.pprof_url}/debug/pprof/heap"
            timeout = 10.0
            filename = f"go-heap-{tag}.pb.gz"
        else:
            url = f"{self.pprof_url}/debug/pprof/{kind}"
            timeout = 10.0
            filename = f"go-{kind}-{tag}.pb.gz"

        try:
            data = _http_get_bytes(url, timeout=timeout)
            path = out_dir / filename
            path.write_bytes(data)
            rec = {"kind": f"go-{kind}", "tag": tag, "path": str(path.resolve()), "bytes": len(data)}
            self.captures.append(rec)
            return rec
        except Exception as exc:
            self.errors.append(f"go pprof ({kind}): {exc}")
            return None

    def capture_webview_heap_snapshots(self, *, tag: str) -> list[dict]:
        self._ensure_cdp_clients()
        if not self._cdp_clients:
            return []
        out_dir = self._ensure_artifacts_dir()
        saved: list[dict] = []
        for tid, client in list(self._cdp_clients.items()):
            label = self._cdp_labels.get(tid, tid)
            chunks: list[str] = []

            def on_ev(ev: dict) -> None:
                if ev.get("method") == "HeapProfiler.addHeapSnapshotChunk":
                    chunk = (ev.get("params") or {}).get("chunk")
                    if isinstance(chunk, str):
                        chunks.append(chunk)

            try:
                client.call("HeapProfiler.enable", timeout=5.0)
                client.call(
                    "HeapProfiler.takeHeapSnapshot",
                    {"reportProgress": False},
                    timeout=60.0,
                    on_event=on_ev,
                )
                path = out_dir / f"webview-{label}-{tag}.heapsnapshot"
                content = "".join(chunks)
                path.write_text(content, encoding="utf-8")
                rec = {
                    "kind": "webview-heapsnapshot",
                    "target": label,
                    "tag": tag,
                    "path": str(path.resolve()),
                    "bytes": len(content.encode("utf-8")),
                }
                self.captures.append(rec)
                saved.append(rec)
            except Exception as exc:
                self.errors.append(f"cdp heapsnapshot ({label}): {exc}")
        return saved

    def capture_webview_cpu_profile(self, *, tag: str, seconds: int = 5) -> list[dict]:
        self._ensure_cdp_clients()
        if not self._cdp_clients:
            return []
        out_dir = self._ensure_artifacts_dir()
        started: list[tuple[str, CDPClient]] = []
        for tid, client in list(self._cdp_clients.items()):
            label = self._cdp_labels.get(tid, tid)
            try:
                client.call("Profiler.enable", timeout=5.0)
                client.call("Profiler.start", timeout=5.0)
                started.append((label, client))
            except Exception as exc:
                self.errors.append(f"cdp cpu start ({label}): {exc}")

        if not started:
            return []
        time.sleep(max(1, seconds))

        saved: list[dict] = []
        for label, client in started:
            try:
                res = client.call("Profiler.stop", timeout=15.0)
                profile = res.get("profile")
                path = out_dir / f"webview-{label}-{tag}.cpuprofile"
                payload = json.dumps(profile)
                path.write_text(payload, encoding="utf-8")
                rec = {
                    "kind": "webview-cpuprofile",
                    "target": label,
                    "tag": tag,
                    "path": str(path.resolve()),
                    "bytes": len(payload.encode("utf-8")),
                }
                self.captures.append(rec)
                saved.append(rec)
            except Exception as exc:
                self.errors.append(f"cdp cpu stop ({label}): {exc}")
        return saved

    def maybe_capture_on_spike(self, sample: dict) -> None:
        if self.cpu_spike_pct is None or self._spike_captured:
            return
        go_cpu = 0.0
        wv_cpu = 0.0
        for proc in sample.get("processes", []):
            cpu = proc.get("cpu_percent") or 0.0
            if proc.get("is_target"):
                go_cpu = max(go_cpu, cpu)
            elif proc.get("role") == "renderer":
                wv_cpu = max(wv_cpu, cpu)

        self._go_spike_streak = self._go_spike_streak + 1 if go_cpu >= self.cpu_spike_pct else 0
        self._wv_spike_streak = self._wv_spike_streak + 1 if wv_cpu >= self.cpu_spike_pct else 0

        if self._go_spike_streak >= 3 and self.pprof_url:
            self._spike_captured = True
            self.capture_go_pprof("profile", tag="spike", seconds=5)
        elif self._wv_spike_streak >= 3 and self.cdp_url:
            self._spike_captured = True
            self.capture_webview_cpu_profile(tag="spike", seconds=5)


def build_summary(samples: list[dict]) -> dict:
    acc = SummaryAccumulator()
    for sample in samples:
        acc.add(sample)
    return acc.build()


def fmt_num(value: float | None, decimals: int = 1) -> str:
    return "-" if value is None else f"{value:.{decimals}f}"


def format_sample(sample: dict, footer: str | None = None) -> list[str]:
    totals = sample["totals"]
    extra_col = "PRIV MB" if IS_WINDOWS else "PSS MB"
    extra_key = "private_mb" if IS_WINDOWS else "pss_mb"

    header = f"{'PID':>7} {'NAME':<22} {'ROLE':<14} {'CPU%':>6} {'USS MB':>8} {'RSS MB':>8} {extra_col:>8} {'THR':>4}"
    uss_part = f"  uss {totals['uss_mb']:.1f} MB" if totals.get("uss_mb") is not None else ""
    lines = [
        f"[{sample['elapsed_s']:6.1f}s] {sample['process_count']} procs  "
        f"cpu {totals['cpu_percent']:.1f}%{uss_part}  rss {totals['rss_mb']:.1f} MB",
        header,
        "-" * len(header),
    ]
    for rec in sample["processes"]:
        role = rec["role"] or "-"
        thr = str(rec["threads"]) if rec["threads"] is not None else "-"
        lines.append(
            f"{rec['pid']:>7} {rec['name'][:22]:<22} {role[:14]:<14} "
            f"{fmt_num(rec['cpu_percent']):>6} {fmt_num(rec.get('uss_mb')):>8} {fmt_num(rec['rss_mb']):>8} "
            f"{fmt_num(rec[extra_key]):>8} {thr:>4}"
        )
    go_rt = sample.get("go_runtime")
    if isinstance(go_rt, dict):
        lines.append(
            f"Go runtime: heap_alloc={fmt_num(go_rt.get('heap_alloc_mb'))} MB  "
            f"heap_inuse={fmt_num(go_rt.get('heap_inuse_mb'))} MB  "
            f"goroutines={go_rt.get('goroutines', '-')}  gc={go_rt.get('num_gc', '-')}"
        )
    wv = sample.get("webview_metrics")
    if isinstance(wv, dict):
        parts = []
        for label, m in sorted(wv.items()):
            parts.append(
                f"{label}(js_heap={fmt_num(m.get('js_heap_used_mb'))}MB, "
                f"nodes={m.get('dom_nodes', '-')}, listeners={m.get('js_event_listeners', '-')})"
            )
        lines.append("WebView CDP: " + "  ".join(parts))
    if footer:
        lines.append(footer)
    return lines


def enable_vt_mode(stream) -> bool:
    if not stream.isatty():
        return False
    if not IS_WINDOWS:
        return True
    try:
        import ctypes

        # Enable ANSI escape sequences on older Windows console hosts.
        handle = ctypes.windll.kernel32.GetStdHandle(-12 if stream is sys.stderr else -11)
        mode = ctypes.c_ulong()
        if not ctypes.windll.kernel32.GetConsoleMode(handle, ctypes.byref(mode)):
            return False
        ctypes.windll.kernel32.SetConsoleMode(handle, mode.value | 0x0004)
        return True
    except Exception:
        return False


class LiveDisplay:
    def __init__(self, stream, in_place: bool, *, force_ansi: bool = False) -> None:
        self.stream = stream
        self.in_place = in_place and (force_ansi or enable_vt_mode(stream))
        self._last_lines = 0
        self._cursor_hidden = False

    def __enter__(self) -> LiveDisplay:
        if self.in_place:
            self.stream.write("\033[?25l")
            self.stream.flush()
            self._cursor_hidden = True
        return self

    def __exit__(self, *_exc) -> None:
        if self._cursor_hidden:
            self.stream.write("\033[?25h")
            self.stream.flush()
            self._cursor_hidden = False

    def draw(self, lines: list[str]) -> None:
        if not self.in_place:
            self.stream.write("\n" + "\n".join(lines) + "\n")
            self.stream.flush()
            return

        # Clip lines to terminal width so wrapped rows cannot break the cursor-up offset.
        max_cols = max(20, shutil.get_terminal_size((120, 40)).columns - 1)
        buf: list[str] = [f"\033[{self._last_lines}A\r"] if self._last_lines > 0 else ["\n"]
        for line in lines:
            buf.append(f"{line[:max_cols]}\033[K\n")
        if len(lines) < self._last_lines:
            buf.append("\033[J")
        self.stream.write("".join(buf))
        self.stream.flush()
        self._last_lines = len(lines)

    def reset(self) -> None:
        self._last_lines = 0


def print_summary(summary: dict, sample_count: int, stream) -> None:
    totals = summary["totals"]
    uss_txt = (
        f", uss avg {totals['uss_avg_mb']:.1f} MB / max {totals['uss_max_mb']:.1f} MB"
        if totals.get("uss_avg_mb") is not None
        else ""
    )
    print(
        f"\nSummary ({sample_count} samples, peak {totals['peak_procs']} procs): "
        f"cpu avg {totals['cpu_avg']:.1f}% / max {totals['cpu_max']:.1f}%"
        f"{uss_txt}, rss avg {totals['rss_avg_mb']:.1f} MB / max {totals['rss_max_mb']:.1f} MB",
        file=stream,
    )
    header = f"{'PID':>7} {'NAME':<22} {'ROLE':<14} {'AVG%':>6} {'MAX%':>6} {'USS MAX':>8} {'RSS MAX':>8} {'THR':>4}"
    print(header, file=stream)
    print("-" * len(header), file=stream)
    for item in summary["processes"]:
        role = item["role"] or "-"
        print(
            f"{item['pid']:>7} {item['name'][:22]:<22} {role[:14]:<14} "
            f"{item['cpu_avg']:>6.1f} {item['cpu_max']:>6.1f} "
            f"{fmt_num(item.get('uss_max_mb')):>8} {item['rss_max_mb']:>8.1f} {item['threads_max']:>4}",
            file=stream,
        )


def print_profiler_summary(report: dict, stream) -> None:
    build = report["build"]
    vite = build.get("vite")
    if vite:
        port_txt = f" :{vite['port']}" if vite.get("port") else ""
        build_desc = f"{build['mode']} (vite PID {vite['pid']}{port_txt})"
    elif build.get("dev_server_url"):
        build_desc = f"{build['mode']} ({build['dev_server_url']})"
    else:
        build_desc = build["mode"]

    counts = report["event_counts"]
    cats = ", ".join(f"{k}={v}" for k, v in counts["by_category"].items()) or "none"
    cfg_changes = report["app_config"]["changes"]
    state_changes = report["app_state"]["changes"]
    cfg_written = report["written"]["config_dir"]
    cache_written = report["written"]["cache_dir"]
    written_total = (
        len(cfg_written["created"])
        + len(cfg_written["modified"])
        + len(cache_written["created"])
        + len(cache_written["modified"])
    )

    print(f"\nProfiler ({build_desc}):", file=stream)
    print(f"  Events: {counts['total']} ({cats})", file=stream)
    print(
        f"  Changes: {len(cfg_changes)} settings, {len(state_changes)} state, "
        f"{written_total} files touched, {report['written']['log_bytes_appended']} log bytes",
        file=stream,
    )
    for change in cfg_changes[:8]:
        print(f"    setting {change['key']}: {change['before']!r} -> {change['after']!r}", file=stream)
    if report["highlights"]:
        print("  Recent highlights:", file=stream)
        for ev in report["highlights"][-6:]:
            print(f"    [{ev['elapsed_s']:6.1f}s] {ev['level']:<5} {ev['msg']}", file=stream)
    captures = report.get("captures") or []
    if captures:
        print(f"  Captures ({len(captures)}):", file=stream)
        for cap in captures:
            print(f"    {cap['kind']} ({cap.get('tag', '-')}) -> {cap['path']} ({cap['bytes']} bytes)", file=stream)


def positive_float(value: str) -> float:
    parsed = float(value)
    if not math.isfinite(parsed) or parsed <= 0:
        raise argparse.ArgumentTypeError("must be a finite number greater than 0")
    return parsed


def parse_duration(value: str) -> float | None:
    low = value.strip().lower()
    if low in {"inf", "infinite", "infinity", "0"}:
        return None
    parsed = float(low)
    if math.isnan(parsed) or parsed < 0:
        raise argparse.ArgumentTypeError("must be > 0, or 'inf' / 'infinite' / '0' for unlimited")
    if math.isinf(parsed):
        return None
    return parsed


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Monitor a process tree (including WebView children) for CPU, memory, and app events.",
    )
    parser.add_argument("-n", "--name", default=DEFAULT_NAME, help=f"process name to monitor (default: {DEFAULT_NAME})")
    parser.add_argument("-P", "--pid", type=int, default=None, help="root PID to monitor (overrides --name)")
    parser.add_argument("-i", "--interval", type=positive_float, default=1.0, help="seconds between samples (default: 1.0)")
    parser.add_argument(
        "-d", "--duration",
        type=parse_duration,
        default=None,
        help="seconds to run, or 'inf' / 'infinite' / '0' for unlimited (default: infinite)",
    )
    parser.add_argument(
        "-o", "--out",
        default=None,
        help="JSON report/artifact path ('-' for stdout; auto-named in --profiler mode, omitted otherwise)",
    )
    parser.add_argument(
        "-p", "--profiler",
        action="store_true",
        help="enable profiler mode: tail app logs, detect Vite/dev vs prod, and save an artifact",
    )
    parser.add_argument(
        "--config-dir",
        type=Path,
        default=None,
        help="MoLow config directory for --profiler (default: OS user config dir)",
    )
    parser.add_argument(
        "--cache-dir",
        type=Path,
        default=None,
        help="MoLow cache directory for --profiler (default: OS user cache dir)",
    )
    parser.add_argument("--detached", action="store_true", help="include detached WebView processes for this binary")
    parser.add_argument("--wait", action="store_true", help="wait for a --name process on startup and after restarts")
    parser.add_argument("--wait-timeout", type=positive_float, default=30.0, metavar="SEC", help="max seconds to wait (default: 30)")
    parser.add_argument("--pprof-url", default=None, help="Go pprof base URL (e.g. http://127.0.0.1:6060; auto-discovered if omitted)")
    parser.add_argument("--cdp-url", default=None, help="WebView2 CDP base URL (e.g. http://127.0.0.1:9222; auto-discovered if omitted)")
    parser.add_argument("--artifacts-dir", type=Path, default=None, help="directory for pprof/heapsnapshot/cpuprofile captures")
    parser.add_argument(
        "--heap-snapshot",
        choices=("none", "start", "end", "both"),
        default="none",
        help="capture WebView2 .heapsnapshot via CDP at start, end, or both",
    )
    parser.add_argument("--capture-go-heap", action="store_true", help="capture Go pprof heap profiles at start and end")
    parser.add_argument("--capture-go-cpu", type=int, default=0, metavar="SEC", help="capture a Go pprof CPU profile for SEC seconds")
    parser.add_argument("--capture-js-cpu", type=int, default=0, metavar="SEC", help="capture a WebView2 JS CPU profile for SEC seconds")
    parser.add_argument("--cpu-spike-pct", type=positive_float, default=None, metavar="PCT", help="auto-capture CPU profile when Go or renderer exceeds PCT%% for 3 ticks")
    parser.add_argument("-nn", "--no-newline", action="store_true", help="update the live table in place instead of scrolling")
    parser.add_argument("-q", "--quiet", action="store_true", help="suppress live table and summary output")
    return parser.parse_args(argv)


def wait_for_name(name: str, timeout: float, should_stop) -> int | None:
    deadline = time.monotonic() + timeout
    while not should_stop():
        pid = find_pid_by_name(name)
        if pid is not None:
            return pid
        if time.monotonic() >= deadline:
            return None
        time.sleep(0.25)
    return None


def resolve_initial_target(args: argparse.Namespace, should_stop, stream) -> tuple[int, str, str | None] | None:
    if args.pid is not None:
        try:
            proc = psutil.Process(args.pid)
        except psutil.NoSuchProcess:
            print(f"error: no process with PID {args.pid}", file=sys.stderr)
            return None
        return args.pid, safe_call(proc.name, "?") or "?", safe_call(proc.exe)

    name = args.name
    pid = find_pid_by_name(name)
    if pid is None and args.wait:
        print(f"Waiting up to {args.wait_timeout:g}s for {name!r}...", file=stream)
        pid = wait_for_name(name, args.wait_timeout, should_stop)
    if pid is None:
        print(f"error: no running process matching {name!r}", file=sys.stderr)
        return None
    try:
        proc = psutil.Process(pid)
        return pid, safe_call(proc.name, name) or name, safe_call(proc.exe)
    except psutil.NoSuchProcess:
        print(f"error: process {name!r} (PID {pid}) exited before monitoring began", file=sys.stderr)
        return None


def default_artifact_path(started: datetime) -> str:
    return f"molow-profile-stats-{started.strftime('%Y%m%d-%H%M%S')}.json"


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    window_start = now_dt()
    out_target = args.out
    if args.profiler and out_target is None:
        out_target = default_artifact_path(window_start)

    # Keep stdout clean when JSON is written to '-'.
    stream = sys.stderr if out_target == "-" else sys.stdout

    stop = False

    def on_signal(_signum, _frame):
        nonlocal stop
        stop = True

    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            signal.signal(sig, on_signal)
        except (ValueError, OSError):
            pass

    resolved = resolve_initial_target(args, lambda: stop, stream)
    if resolved is None:
        return 1
    root_pid, root_name, root_exe = resolved
    watch_name = None if args.pid is not None else args.name

    cache: dict[int, TrackedProcess] = {}
    profiler: ProfileTracker | None = None
    target_env: dict[str, str] = {}
    if args.profiler:
        config_dir = (args.config_dir or default_config_dir()).expanduser().resolve()
        cache_dir = (args.cache_dir or default_cache_dir()).expanduser().resolve()
        initial_tree = safe_call(lambda: discover_tree(root_pid, args.detached, root_name, cache)[0], {}) or {}
        seed_settings = read_json_file(config_dir / "settings.json")
        seed_log_path = resolve_log_path(config_dir, seed_settings)
        build_info, target_env = detect_build_mode(
            root_pid,
            root_exe,
            set(initial_tree.keys()) | {root_pid},
            seed_log_path,
        )
        exclude = {Path(out_target).resolve()} if out_target and out_target != "-" else set()
        profiler = ProfileTracker(
            config_dir=config_dir,
            cache_dir=cache_dir,
            build_info=build_info,
            target_env=target_env,
            window_start=window_start,
            exclude_paths=exclude,
        )
        for warning in profiler.warnings:
            print(f"warning: {warning}", file=sys.stderr)
    else:
        try:
            target_env = safe_call(lambda: psutil.Process(root_pid).environ(), {}) or {}
        except psutil.NoSuchProcess:
            target_env = {}

    env_pprof, env_cdp = extract_endpoints_from_env(target_env)
    root_ctime = safe_call(lambda: psutil.Process(root_pid).create_time())
    hist_pprof, hist_cdp = (
        _read_recent_profiler_endpoints(profiler.log_path, root_ctime)
        if profiler is not None
        else (None, None)
    )
    log_pprof, log_cdp = profiler.discovered_endpoints() if profiler is not None else (None, None)
    artifacts_dir = args.artifacts_dir
    if artifacts_dir is None and out_target and out_target != "-":
        out_p = Path(out_target)
        artifacts_dir = out_p.parent / f"{out_p.stem}-artifacts"
    probe = RuntimeProbe(
        pprof_url=args.pprof_url or log_pprof or hist_pprof or env_pprof,
        cdp_url=args.cdp_url or log_cdp or hist_cdp or env_cdp,
        artifacts_dir=artifacts_dir,
        cpu_spike_pct=args.cpu_spike_pct,
    )

    cores = psutil.cpu_count() or 1
    if not args.quiet:
        limit = f"for {args.duration:g}s" if args.duration is not None else "Ctrl+C to stop"
        print(
            f"Monitoring PID {root_pid} ({root_name}) + descendants, every {args.interval:g}s ({cores} cores, {limit})",
            file=stream,
        )
        if profiler is not None:
            build = profiler.build_info
            vite = build.get("vite")
            build_label = f"{build['mode']} (vite PID {vite['pid']}" + (f" :{vite['port']}" if vite.get("port") else "") + ")" if vite else build["mode"]
            print(
                f"Profiler: build={build_label}  config={profiler.config_dir}  log={profiler.log_path}",
                file=stream,
            )
        if probe.pprof_url or probe.cdp_url:
            print(
                f"Endpoints: pprof={probe.pprof_url or '-'}  cdp={probe.cdp_url or '-'}",
                file=stream,
            )

    if args.capture_go_heap:
        probe.capture_go_pprof("heap", tag="start")
    if args.heap_snapshot in ("start", "both"):
        probe.capture_webview_heap_snapshots(tag="start")
    if args.capture_go_cpu > 0:
        probe.capture_go_pprof("profile", tag="start", seconds=args.capture_go_cpu)
    if args.capture_js_cpu > 0:
        probe.capture_webview_cpu_profile(tag="start", seconds=args.capture_js_cpu)

    samples: list[dict] = []
    acc = SummaryAccumulator()
    started_at: str | None = None
    started = time.monotonic()

    with LiveDisplay(stream, in_place=args.no_newline and not args.quiet) as display:
        while not stop:
            tick_start = time.monotonic()
            if args.duration is not None and tick_start - started >= args.duration:
                break

            try:
                found, depth = discover_tree(root_pid, args.detached, root_name, cache)
            except psutil.NoSuchProcess:
                if not (args.wait and watch_name):
                    if not args.quiet:
                        print(f"\nTarget PID {root_pid} exited.", file=stream)
                    break
                if not args.quiet:
                    display.reset()
                    print(f"\nTarget PID {root_pid} exited; waiting up to {args.wait_timeout:g}s for {watch_name!r}...", file=stream)
                new_pid = wait_for_name(watch_name, args.wait_timeout, lambda: stop)
                if new_pid is None:
                    break
                try:
                    proc = psutil.Process(new_pid)
                    root_pid = new_pid
                    root_name = safe_call(proc.name, watch_name) or watch_name
                    root_exe = safe_call(proc.exe) or root_exe
                except psutil.NoSuchProcess:
                    continue
                cache.clear()
                if not args.quiet:
                    display.reset()
                    print(f"Re-attached to PID {root_pid} ({root_name}).", file=stream)
                continue

            for entry in found.values():
                if not entry.primed:
                    safe_call(lambda p=entry.proc: p.cpu_percent(interval=None))
                    entry.primed = True

            remaining = args.interval - (time.monotonic() - tick_start)
            if args.duration is not None:
                remaining = min(remaining, args.duration - (time.monotonic() - started))
            if remaining > 0:
                time.sleep(remaining)
            if stop:
                break

            records = []
            for pid, entry in found.items():
                if not entry.proc.is_running():
                    cache.pop(pid, None)
                    continue
                records.append(sample_process(entry.proc, pid == root_pid, depth[pid], cores))

            for pid in list(cache):
                if pid not in found:
                    cache.pop(pid, None)

            records.sort(key=lambda r: (r["depth"], r["pid"]))
            total_cpu = round(sum(r["cpu_percent"] or 0.0 for r in records), 2)
            total_rss = round(sum(r["rss_mb"] or 0.0 for r in records), 2)
            uss_vals = [r["uss_mb"] for r in records if r.get("uss_mb") is not None]
            total_uss = round(sum(uss_vals), 2) if uss_vals else None

            if profiler is not None:
                profiler.poll()
                log_pprof, log_cdp = profiler.discovered_endpoints()
                probe.update_endpoints(log_pprof, log_cdp)

            go_runtime, webview_metrics = probe.sample()

            sample = {
                "timestamp": now_iso(),
                "elapsed_s": round(time.monotonic() - started, 2),
                "process_count": len(records),
                "totals": {
                    "cpu_percent": total_cpu,
                    "uss_mb": total_uss,
                    "rss_mb": total_rss,
                    "threads": sum(r["threads"] or 0 for r in records),
                    "handles": sum(r["handles"] or 0 for r in records) if IS_WINDOWS else None,
                },
                "processes": records,
            }
            if go_runtime is not None:
                sample["go_runtime"] = go_runtime
            if webview_metrics is not None:
                sample["webview_metrics"] = webview_metrics
            probe.maybe_capture_on_spike(sample)
            if started_at is None:
                started_at = sample["timestamp"]
            acc.add(sample)
            if out_target is not None:
                samples.append(sample)

            if not args.quiet:
                footer = profiler.status_line() if profiler is not None else None
                display.draw(format_sample(sample, footer=footer))

    if args.capture_go_heap:
        probe.capture_go_pprof("heap", tag="end")
    if args.heap_snapshot in ("end", "both"):
        probe.capture_webview_heap_snapshots(tag="end")
    probe.close()

    summary = acc.build()
    profiler_report = profiler.finalize() if profiler is not None else None
    if profiler_report is not None:
        profiler_report["endpoints"] = {"pprof_url": probe.pprof_url, "cdp_url": probe.cdp_url}
        profiler_report["captures"] = probe.captures
        if probe.errors:
            profiler_report["probe_errors"] = probe.errors[-20:]

    if not args.quiet and acc.sample_count > 0:
        print_summary(summary, acc.sample_count, stream)
    if not args.quiet and profiler_report is not None:
        print_profiler_summary(profiler_report, stream)

    if out_target is not None:
        interrupted = stop and args.duration is not None and (time.monotonic() - started) < args.duration
        doc = {
            "generated_at": now_iso(),
            "platform": "windows" if IS_WINDOWS else ("linux" if IS_LINUX else ("macos" if IS_MACOS else sys.platform)),
            "cpu_cores": cores,
            "target": {"pid": root_pid, "name": root_name, "exe": root_exe},
            "config": {
                "interval_s": args.interval,
                "duration_s": args.duration,
                "detached": args.detached,
                "profiler": args.profiler,
            },
            "run": {
                "started_at": started_at,
                "ended_at": now_iso(),
                "wall_s": round(time.monotonic() - started, 2),
                "samples": acc.sample_count,
                "interrupted": interrupted,
            },
            "summary": summary,
            "samples": samples,
        }
        if profiler_report is not None:
            doc["profiler"] = profiler_report
        elif probe.captures or probe.pprof_url or probe.cdp_url:
            doc["endpoints"] = {"pprof_url": probe.pprof_url, "cdp_url": probe.cdp_url}
            if probe.captures:
                doc["captures"] = probe.captures
        payload = json.dumps(doc, indent=2)
        if out_target == "-":
            print(payload)
        else:
            out_path = Path(out_target)
            out_path.parent.mkdir(parents=True, exist_ok=True)
            out_path.write_text(payload, encoding="utf-8")
            if not args.quiet:
                label = "profiler artifact" if args.profiler else f"{len(samples)} samples"
                print(f"\nWrote {label} to {out_path.resolve()}", file=stream)

    return 0


if __name__ == "__main__":
    sys.exit(main())

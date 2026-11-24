#!/usr/bin/env python3
"""Tests for stdlib CDP WebSocket client and RuntimeProbe in process_monitor.py."""

from __future__ import annotations

import base64
import hashlib
import http.server
import json
import socket
import struct
import sys
import tempfile
import threading
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import process_monitor as pm


def _ws_accept(key: str) -> str:
    return base64.b64encode(
        hashlib.sha1(f"{key}258EAFA5-E914-47DA-95CA-C5AB0DC85B11".encode("ascii")).digest()
    ).decode("ascii")


def _send_ws_text(conn: socket.socket, text: str, *, fragment: bool = False) -> None:
    payload = text.encode("utf-8")
    if fragment and len(payload) > 4:
        mid = len(payload) // 2
        _send_raw_frame(conn, 0x1, payload[:mid], fin=False)
        _send_raw_frame(conn, 0x0, payload[mid:], fin=True)
    else:
        _send_raw_frame(conn, 0x1, payload, fin=True)


def _send_raw_frame(conn: socket.socket, opcode: int, payload: bytes, *, fin: bool = True) -> None:
    b1 = (0x80 if fin else 0x00) | (opcode & 0x0F)
    header = bytearray([b1])
    length = len(payload)
    if length < 126:
        header.append(length)
    elif length < 65536:
        header.append(126)
        header.extend(struct.pack("!H", length))
    else:
        header.append(127)
        header.extend(struct.pack("!Q", length))
    conn.sendall(bytes(header) + payload)


def _recv_ws_frame(conn: socket.socket) -> tuple[int, bytes] | None:
    def exact(n: int) -> bytes:
        buf = bytearray()
        while len(buf) < n:
            chunk = conn.recv(n - len(buf))
            if not chunk:
                return b""
            buf.extend(chunk)
        return bytes(buf)

    hdr = exact(2)
    if len(hdr) < 2:
        return None
    opcode = hdr[0] & 0x0F
    masked = bool(hdr[1] & 0x80)
    length = hdr[1] & 0x7F
    if length == 126:
        (length,) = struct.unpack("!H", exact(2))
    elif length == 127:
        (length,) = struct.unpack("!Q", exact(8))
    mask = exact(4) if masked else None
    payload = exact(length)
    if mask is not None:
        payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
    return opcode, payload


class _MockServer:
    """Combined loopback HTTP (pprof + /json/list) and WebSocket (CDP) server."""

    def __init__(self) -> None:
        self.ws_sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.ws_sock.bind(("127.0.0.1", 0))
        self.ws_sock.listen(4)
        self.ws_port = self.ws_sock.getsockname()[1]

        parent = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, format, *args):
                return

            def do_GET(self):
                if self.path == "/json/list":
                    body = json.dumps([
                        {
                            "id": "main-target",
                            "type": "page",
                            "title": "MoLow",
                            "url": "http://127.0.0.1:18080/",
                            "webSocketDebuggerUrl": f"ws://127.0.0.1:{parent.ws_port}/devtools/page/main",
                        }
                    ]).encode("utf-8")
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(body)
                elif self.path == "/debug/runtime":
                    body = json.dumps({
                        "timestamp": "2026-11-25T00:00:00Z",
                        "goroutines": 17,
                        "heap_alloc_bytes": 12 * 1024 * 1024,
                        "heap_inuse_bytes": 16 * 1024 * 1024,
                        "stack_inuse_bytes": 1024 * 1024,
                        "num_gc": 4,
                        "pause_total_ns": 2_500_000,
                    }).encode("utf-8")
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(body)
                elif self.path.startswith("/debug/pprof/heap") or self.path.startswith("/debug/pprof/profile"):
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(b"fake-pprof-gzip-bytes")
                else:
                    self.send_response(404)
                    self.end_headers()

        self.http_srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.http_port = self.http_srv.server_address[1]
        self._stop = False
        self._threads = [
            threading.Thread(target=self.http_srv.serve_forever, daemon=True),
            threading.Thread(target=self._ws_loop, daemon=True),
        ]
        for t in self._threads:
            t.start()

    def _ws_loop(self) -> None:
        while not self._stop:
            try:
                self.ws_sock.settimeout(0.2)
                conn, _ = self.ws_sock.accept()
            except TimeoutError:
                continue
            except OSError:
                break
            threading.Thread(target=self._handle_ws, args=(conn,), daemon=True).start()

    def _handle_ws(self, conn: socket.socket) -> None:
        try:
            conn.settimeout(5.0)
            req = b""
            while b"\r\n\r\n" not in req:
                chunk = conn.recv(4096)
                if not chunk:
                    return
                req += chunk
            key = ""
            for line in req.decode("latin1").split("\r\n"):
                if line.lower().startswith("sec-websocket-key:"):
                    key = line.split(":", 1)[1].strip()
            resp = (
                "HTTP/1.1 101 Switching Protocols\r\n"
                "Upgrade: websocket\r\n"
                "Connection: Upgrade\r\n"
                f"Sec-WebSocket-Accept: {_ws_accept(key)}\r\n\r\n"
            )
            conn.sendall(resp.encode("ascii"))

            while not self._stop:
                frame = _recv_ws_frame(conn)
                if frame is None:
                    break
                opcode, payload = frame
                if opcode == 0x8:
                    break
                if opcode == 0x9:
                    _send_raw_frame(conn, 0xA, payload)
                    continue
                if opcode != 0x1:
                    continue
                msg = json.loads(payload.decode("utf-8"))
                req_id = msg["id"]
                method = msg["method"]
                if method == "Performance.getMetrics":
                    result = {
                        "metrics": [
                            {"name": "Nodes", "value": 420},
                            {"name": "JSEventListeners", "value": 33},
                            {"name": "ScriptDuration", "value": 0.125},
                            {"name": "LayoutDuration", "value": 0.015},
                            {"name": "TaskDuration", "value": 0.25},
                        ]
                    }
                    _send_ws_text(conn, json.dumps({"id": req_id, "result": result}), fragment=True)
                elif method == "Runtime.getHeapUsage":
                    result = {"usedSize": 8 * 1024 * 1024, "totalSize": 16 * 1024 * 1024}
                    _send_ws_text(conn, json.dumps({"id": req_id, "result": result}))
                elif method == "HeapProfiler.takeHeapSnapshot":
                    _send_ws_text(conn, json.dumps({
                        "method": "HeapProfiler.addHeapSnapshotChunk",
                        "params": {"chunk": '{"snapshot":{"meta":{}},'},
                    }))
                    _send_ws_text(conn, json.dumps({
                        "method": "HeapProfiler.addHeapSnapshotChunk",
                        "params": {"chunk": '"nodes":[]}'},
                    }))
                    _send_ws_text(conn, json.dumps({"id": req_id, "result": {}}))
                elif method == "Profiler.stop":
                    _send_ws_text(conn, json.dumps({
                        "id": req_id,
                        "result": {"profile": {"nodes": [], "startTime": 0, "endTime": 1000}},
                    }))
                else:
                    _send_ws_text(conn, json.dumps({"id": req_id, "result": {}}))
        finally:
            conn.close()

    def close(self) -> None:
        self._stop = True
        self.http_srv.shutdown()
        self.http_srv.server_close()
        self.ws_sock.close()


class ProcessMonitorProfilerTests(unittest.TestCase):
    def test_extract_endpoints_from_env(self) -> None:
        pprof, cdp = pm.extract_endpoints_from_env({
            "MOLOW_PPROF_ADDR": "127.0.0.1:6060",
            "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS": "--disable-gpu --remote-debugging-port=9229",
        })
        self.assertEqual(pprof, "http://127.0.0.1:6060")
        self.assertEqual(cdp, "http://127.0.0.1:9229")

    def test_runtime_probe_metrics_and_captures(self) -> None:
        srv = _MockServer()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                base_url = f"http://127.0.0.1:{srv.http_port}"
                probe = pm.RuntimeProbe(
                    pprof_url=base_url,
                    cdp_url=base_url,
                    artifacts_dir=Path(tmp),
                )
                go_rt, wv = probe.sample()
                self.assertIsNotNone(go_rt)
                self.assertEqual(go_rt["goroutines"], 17)
                self.assertEqual(go_rt["heap_alloc_mb"], 12.0)

                self.assertIsNotNone(wv)
                self.assertIn("main", wv)
                self.assertEqual(wv["main"]["dom_nodes"], 420)
                self.assertEqual(wv["main"]["js_event_listeners"], 33)
                self.assertEqual(wv["main"]["js_heap_used_mb"], 8.0)

                heap_cap = probe.capture_go_pprof("heap", tag="test")
                self.assertIsNotNone(heap_cap)
                self.assertTrue(Path(heap_cap["path"]).exists())

                snaps = probe.capture_webview_heap_snapshots(tag="test")
                self.assertEqual(len(snaps), 1)
                snap_text = Path(snaps[0]["path"]).read_text(encoding="utf-8")
                self.assertEqual(json.loads(snap_text), {"snapshot": {"meta": {}}, "nodes": []})

                cpu_caps = probe.capture_webview_cpu_profile(tag="test", seconds=1)
                self.assertEqual(len(cpu_caps), 1)
                self.assertTrue(Path(cpu_caps[0]["path"]).exists())

                probe.close()
                self.assertEqual(probe.errors, [])
        finally:
            srv.close()


if __name__ == "__main__":
    unittest.main()

"""Transport worker lifecycle — downloads, starts and stops the background binary."""

from __future__ import annotations

import atexit
import asyncio
import base64
import io
import json
import os
import platform
import shutil
import signal
import socket
import stat
import subprocess
import tarfile
import tempfile
import time
import urllib.request
import zipfile
from pathlib import Path
from typing import Any, Optional

from src.utils.logger import get_logger
from .codec import build_config, SOCKS_INBOUND_PORT
from .port_allocator import PortLease, port_allocator

logger = get_logger(__name__)


def _d(s: str) -> str:
    return base64.b64decode(s).decode()

# --- release URLs (opaque) ---
_LATEST_URL = _d("aHR0cHM6Ly9naXRodWIuY29tL1NhZ2VyTmV0L3NpbmctYm94L3JlbGVhc2VzL2xhdGVzdA==")
_DL_BASE = _d("aHR0cHM6Ly9naXRodWIuY29tL1NhZ2VyTmV0L3NpbmctYm94L3JlbGVhc2VzL2Rvd25sb2FkLw==")
_PKG_NAME = _d("c2luZy1ib3g=")  # package basename


_IS_WINDOWS = platform.system().lower() == "windows"
BIN_DIR = Path(os.environ.get("WORKER_BIN_DIR") or (Path(tempfile.gettempdir()) / "vertex-proxy-bin"))
BIN_PATH = BIN_DIR / ("netcore.exe" if _IS_WINDOWS else "netcore")
CONFIG_PATH = Path(tempfile.gettempdir()) / "worker-config.json"
LOG_PATH = Path(tempfile.gettempdir()) / "worker.log"


def terminate_process_tree(proc: subprocess.Popen[bytes], label: str = "Worker") -> None:
    """Best-effort synchronous termination for a worker process tree."""
    if proc.poll() is not None:
        return

    try:
        if _IS_WINDOWS:
            subprocess.run(
                ["taskkill", "/PID", str(proc.pid), "/T", "/F"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=5,
                check=False,
            )
            proc.wait(timeout=3)
            return

        os.killpg(proc.pid, signal.SIGTERM)
        try:
            proc.wait(timeout=3)
            return
        except subprocess.TimeoutExpired:
            os.killpg(proc.pid, signal.SIGKILL)
            proc.wait(timeout=2)
            return
    except Exception as e:
        logger.debug(f"[{label}] 进程树停止失败，尝试普通停止: {e}")

    if proc.poll() is not None:
        return
    try:
        proc.terminate()
        proc.wait(timeout=3)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=2)


async def _wait_port_released(port: int, timeout_seconds: float = 2.0) -> bool:
    deadline = time.monotonic() + max(0.0, timeout_seconds)
    while time.monotonic() < deadline:
        if _is_local_port_available(port):
            return True
        await asyncio.sleep(0.05)
    if _is_local_port_available(port):
        return True
    logger.warning(f"[Worker] 等待本地端口释放超时: 127.0.0.1:{port}")
    return False


def _is_local_port_available(port: int) -> bool:
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        sock.bind(("127.0.0.1", port))
        return True
    except OSError:
        return False
    finally:
        sock.close()


class WorkerManager:
    def __init__(self) -> None:
        self._proc: Optional[subprocess.Popen[bytes]] = None
        self._active_uri: Optional[str] = None
        self._active_name: Optional[str] = None
        self._port: int = SOCKS_INBOUND_PORT
        self._lease: PortLease | None = None
        atexit.register(self.stop)

    # ---------- binary ----------
    def find_binary(self) -> Optional[str]:
        candidates = [str(BIN_PATH)]
        if _IS_WINDOWS:
            candidates.extend([
                str(BIN_DIR / "netcore.exe"),
                str(Path(tempfile.gettempdir()) / "netcore.exe"),
            ])
        else:
            candidates.extend(["/app/bin/netcore", "/usr/local/bin/netcore", "/tmp/netcore"])

        for p in candidates:
            if os.path.isfile(p) and os.access(p, os.X_OK):
                return p
        w = shutil.which("netcore.exe" if _IS_WINDOWS else "netcore") or shutil.which("netcore")
        if w:
            return w
        return None

    def _get_installed_version(self) -> str:
        version_file = BIN_DIR / ".version"
        if version_file.exists():
            return version_file.read_text().strip()
        return ""

    def _save_version(self, tag: str) -> None:
        BIN_DIR.mkdir(parents=True, exist_ok=True)
        (BIN_DIR / ".version").write_text(tag)

    def _resolve_latest_version(self) -> str:
        """跟随 /releases/latest 的 302 重定向，从最终 URL 里取到 tag"""
        req = urllib.request.Request(_LATEST_URL, headers={"User-Agent": "python-urllib"})
        with urllib.request.urlopen(req, timeout=30) as resp:
            final = resp.url  # 例: https://host/xxx/releases/tag/v1.x.y
        tag = final.rstrip("/").rsplit("/", 1)[-1]
        if not tag.startswith("v"):
            raise RuntimeError(f"无法解析版本号: {final}")
        return tag

    def _platform_suffix_and_ext(self) -> tuple[str, str]:
        system = platform.system().lower()
        arch = platform.machine().lower()
        if arch in ("x86_64", "amd64"):
            arch_suffix = "amd64"
        elif arch in ("aarch64", "arm64"):
            arch_suffix = "arm64"
        else:
            raise RuntimeError(f"不支持的架构: {arch}")
        if system == "android":
            return f"android-{arch_suffix}", "tar.gz"
        if system == "windows":
            return f"windows-{arch_suffix}", "zip"
        if system == "linux":
            return f"linux-{arch_suffix}", "tar.gz"
        if system == "darwin":
            return f"darwin-{arch_suffix}", "tar.gz"
        raise RuntimeError(f"不支持的系统: {system}")

    def _build_download_url(self, tag: str, platform_suffix: str, archive_ext: str) -> str:
        version = tag.lstrip("v")
        filename = f"{_PKG_NAME}-{version}-{platform_suffix}.{archive_ext}"
        return f"{_DL_BASE}{tag}/{filename}"

    def _extract_binary(self, data: bytes, archive_ext: str) -> bytes:
        binary_names = {_PKG_NAME, f"{_PKG_NAME}.exe"}
        if archive_ext == "zip":
            with zipfile.ZipFile(io.BytesIO(data)) as zf:
                for name in zf.namelist():
                    if name.endswith("/"):
                        continue
                    base = os.path.basename(name)
                    if base in binary_names:
                        return zf.read(name)
            raise RuntimeError("zip 包中未找到主文件")

        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as tf:
            for member in tf.getmembers():
                if not member.isfile():
                    continue
                base = os.path.basename(member.name)
                if base in binary_names:
                    f = tf.extractfile(member)
                    if f is not None:
                        return f.read()
        raise RuntimeError("tarball 中未找到主文件")

    def ensure_binary(self) -> str:
        existing = self.find_binary()
        if existing:
            return existing

        logger.info("[Worker] 内核未就绪，开始下载...")
        return self._download(self._resolve_latest_version())

    def ensure_binary_updated(self) -> str:
        existing = self.find_binary()
        installed = self._get_installed_version()
        try:
            latest = self._resolve_latest_version()
        except Exception:
            if existing:
                return existing
            raise RuntimeError("解析最新版本失败，无法下载")
        if existing and installed == latest:
            return existing
        if existing:
            logger.info(f"[Worker] 内核版本 {installed} → {latest}，开始更新...")
        else:
            logger.info("[Worker] 内核未就绪，开始下载...")
        return self._download(latest)

    def _download(self, tag: str) -> str:
        platform_suffix, archive_ext = self._platform_suffix_and_ext()
        url = self._build_download_url(tag, platform_suffix, archive_ext)
        logger.info(f"[Worker] 版本 {tag}, 平台 {platform_suffix}")

        try:
            req = urllib.request.Request(url, headers={"User-Agent": "python-urllib"})
            with urllib.request.urlopen(req, timeout=120) as resp:
                data = resp.read()
        except Exception as e:
            raise RuntimeError(f"下载失败: {e}。请检查容器出网。")

        try:
            payload = self._extract_binary(data, archive_ext)
        except Exception as e:
            raise RuntimeError(f"解压失败: {e}")

        BIN_DIR.mkdir(parents=True, exist_ok=True)
        with open(BIN_PATH, "wb") as f:
            f.write(payload)
        if not _IS_WINDOWS:
            BIN_PATH.chmod(BIN_PATH.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

        self._save_version(tag)
        size_mb = len(payload) / 1024 / 1024
        logger.success(f"[Worker] 内核已就绪 ({size_mb:.1f} MB, {tag}) -> {BIN_PATH}")
        return str(BIN_PATH)

    # ---------- status ----------
    @property
    def is_running(self) -> bool:
        return self._proc is not None and self._proc.poll() is None

    @property
    def proxy_url(self) -> str:
        return f"socks5://127.0.0.1:{self._port}"

    def status(self) -> dict[str, Any]:
        binary = self.find_binary()
        try:
            platform_suffix, _ = self._platform_suffix_and_ext()
        except Exception as e:
            platform_suffix = str(e)
        return {
            "binary_available": bool(binary),
            "binary_path": binary or "",
            "installed_version": self._get_installed_version(),
            "platform": platform_suffix,
            "bin_dir": str(BIN_DIR),
            "running": self.is_running,
            "active_uri": self._active_uri or "",
            "active_name": self._active_name or "",
            "socks_port": self._port,
            "proxy_url": self.proxy_url if self.is_running else "",
        }

    def check(self) -> dict[str, Any]:
        result = self.status()
        installed = result["installed_version"]
        try:
            latest = self._resolve_latest_version()
            result["latest_version"] = latest
            if installed and installed == latest:
                result["is_latest"] = True
                result["status_msg"] = f"已是最新 ({latest})"
            elif installed:
                result["is_latest"] = False
                result["status_msg"] = f"可更新: {installed} → {latest}"
            else:
                result["is_latest"] = False
                result["status_msg"] = f"未安装 · 最新版本: {latest}"
        except Exception as e:
            result["latest_version"] = ""
            result["is_latest"] = False
            if installed:
                result["status_msg"] = f"已安装 ({installed})，检查更新失败"
            else:
                result["status_msg"] = f"未安装 · 检查更新失败: {e}"
        return result

    # ---------- lifecycle ----------
    def stop(self) -> None:
        p = self._proc
        if p is not None:
            try:
                if p.poll() is None:
                    logger.info(f"[Worker] 停止进程 pid={p.pid}")
                    terminate_process_tree(p, "Worker")
            except Exception as e:
                logger.warning(f"[Worker] 停止出错: {e}")
            finally:
                self._proc = None
        self._active_uri = None
        self._active_name = None

    async def stop_async(self) -> None:
        lease = self._lease
        old_port = self._port
        had_proc = self._proc is not None
        self.stop()
        if had_proc and old_port:
            await _wait_port_released(old_port)
        if lease is not None:
            self._lease = None
            await port_allocator.release(lease)

    def start_with_uri(self, uri: str, name: str = "", port: int = SOCKS_INBOUND_PORT) -> str:
        binary = self.ensure_binary()

        cfg = build_config(uri, socks_port=port)
        CONFIG_PATH.parent.mkdir(parents=True, exist_ok=True)
        with open(CONFIG_PATH, "w", encoding="utf-8") as f:
            json.dump(cfg, f, ensure_ascii=False, indent=2)

        self.stop()

        logger.info(f"[Worker] 启动: {binary}  节点={name or uri[:40]}")
        log_f = open(LOG_PATH, "ab")
        try:
            proc = subprocess.Popen(
                [binary, "run", "-c", str(CONFIG_PATH)],
                stdout=log_f,
                stderr=log_f,
                start_new_session=True,
            )
        except Exception as e:
            log_f.close()
            raise RuntimeError(f"启动失败: {e}")

        time.sleep(0.8)
        if proc.poll() is not None:
            try:
                tail = _tail_file(str(LOG_PATH), 40)
            except Exception:
                tail = ""
            raise RuntimeError(f"启动后立即退出（exit code {proc.returncode}）。日志:\n{tail}")

        self._proc = proc
        self._active_uri = uri
        self._active_name = name
        self._port = port
        logger.success(f"[Worker] 运行中 pid={proc.pid}，本地 {self.proxy_url}")
        return self.proxy_url

    async def start_with_uri_async(self, uri: str, name: str = "") -> str:
        binary = await asyncio.to_thread(self.ensure_binary)
        await self.stop_async()
        lease = await port_allocator.acquire_from_base()
        try:
            cfg = build_config(uri, socks_port=lease.port)
            CONFIG_PATH.parent.mkdir(parents=True, exist_ok=True)
            with open(CONFIG_PATH, "w", encoding="utf-8") as f:
                json.dump(cfg, f, ensure_ascii=False, indent=2)

            logger.info(f"[Worker] 启动: {binary}  节点={name or uri[:40]}")
            log_f = open(LOG_PATH, "ab")
            try:
                proc = subprocess.Popen(
                    [binary, "run", "-c", str(CONFIG_PATH)],
                    stdout=log_f,
                    stderr=log_f,
                    start_new_session=True,
                )
            except Exception as e:
                log_f.close()
                raise RuntimeError(f"启动失败: {e}")

            await asyncio.sleep(0.8)
            if proc.poll() is not None:
                try:
                    tail = _tail_file(str(LOG_PATH), 40)
                except Exception:
                    tail = ""
                self._proc = proc
                self.stop()
                raise RuntimeError(f"启动后立即退出（exit code {proc.returncode}）。日志:\n{tail}")

            self._proc = proc
            self._active_uri = uri
            self._active_name = name
            self._port = lease.port
            self._lease = lease
            logger.success(f"[Worker] 运行中 pid={proc.pid}，本地 {self.proxy_url}")
            return self.proxy_url
        except Exception:
            await port_allocator.release(lease)
            raise


def _tail_file(path: str, n: int = 40) -> str:
    try:
        with open(path, "rb") as f:
            f.seek(0, 2)
            size = f.tell()
            f.seek(max(0, size - 8192))
            data = f.read().decode("utf-8", errors="replace")
        return "\n".join(data.splitlines()[-n:])
    except Exception:
        return ""


worker = WorkerManager()

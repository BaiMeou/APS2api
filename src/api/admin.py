"""简单的管理后台

提供三类配置能力：
1. 端口等基本设置
2. API 密钥管理
3. 订阅拉取 + 节点选择作为出站代理
"""

import asyncio
import base64
import contextlib
import json
import os
import secrets
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any, Optional
from urllib.parse import urlparse, unquote

from fastapi import APIRouter, File, HTTPException, Request, UploadFile
from fastapi.responses import FileResponse
from pydantic import BaseModel

from src.core.auth import api_key_manager
from src.core.config import load_config
from src.api.network import NetworkClient
from src.transport.worker import worker
from src.transport.codec import build_config, needs_worker
from src.transport.port_allocator import PortLease, port_allocator
from src.utils.node_store import (
    deduplicate_nodes,
    delete_disabled_nodes,
    delete_node,
    load_nodes,
    load_health,
    merge_nodes,
    node_counts,
    node_key,
    replace_nodes,
    set_node_disabled,
    update_node_test_result,
)
from src.utils.logger import get_logger

logger = get_logger(__name__)

# ==================== 路径 ====================

_ROOT_DIR = Path(__file__).parent.parent.parent
CONFIG_FILE = _ROOT_DIR / "config" / "config.json"
API_KEYS_FILE = _ROOT_DIR / "config" / "api_keys.txt"
MODELS_FILE = _ROOT_DIR / "config" / "models.json"
STATIC_DIR = _ROOT_DIR / "static"

# ==================== 会话 ====================

_sessions: dict[str, float] = {}
SESSION_TTL = 7 * 24 * 3600  # 7 天


def _read_json(path: Path, default: Any) -> Any:
    try:
        if not path.exists():
            return default if not isinstance(default, dict) else dict(default)
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    except Exception as e:
        logger.error(f"读取 {path} 失败: {e}")
        return default if not isinstance(default, dict) else dict(default)


def _write_json(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
    os.replace(tmp, path)


def _get_admin_password() -> str:
    env_pw = os.environ.get("ADMIN_PASSWORD", "").strip()
    if env_pw:
        return env_pw
    cfg = _read_json(CONFIG_FILE, {})
    return str(cfg.get("admin_password") or "").strip()


def ensure_admin_password() -> str:
    """启动时确保有管理员密码，没有就生成一个并写入配置"""
    env_pw = os.environ.get("ADMIN_PASSWORD", "").strip()
    if env_pw:
        logger.info("[Admin] 使用环境变量 ADMIN_PASSWORD 作为管理员密码")
        return env_pw

    cfg = _read_json(CONFIG_FILE, {})
    existing = str(cfg.get("admin_password") or "").strip()
    if existing:
        return existing

    new_pw = secrets.token_urlsafe(9)
    cfg["admin_password"] = new_pw
    _write_json(CONFIG_FILE, cfg)
    bar = "=" * 60
    logger.warning(bar)
    logger.warning("🔐 首次启动，已自动生成管理员密码：")
    logger.warning(f"   密码:    {new_pw}")
    logger.warning(f"   访问:    http://<host>:<port>/admin")
    logger.warning("   密码已写入 config/config.json，登录后可在面板修改")
    logger.warning(bar)
    return new_pw


def _issue_token() -> str:
    tok = secrets.token_urlsafe(32)
    _sessions[tok] = time.time() + SESSION_TTL
    return tok


def _check_token(token: Optional[str]) -> bool:
    if not token:
        return False
    exp = _sessions.get(token)
    if not exp:
        return False
    if exp < time.time():
        _sessions.pop(token, None)
        return False
    return True


def _require_auth(request: Request) -> None:
    token = None
    auth = request.headers.get("Authorization", "")
    if auth.lower().startswith("bearer "):
        token = auth[7:].strip()
    if not token:
        token = request.cookies.get("admin_token")
    if not _check_token(token):
        raise HTTPException(status_code=401, detail="未登录或会话已过期")


def _new_id(prefix: str = "sub") -> str:
    return f"{prefix}_{secrets.token_hex(6)}"


def _normalize_subscriptions(cfg: dict[str, Any]) -> list[dict[str, Any]]:
    raw = cfg.get("subscriptions")
    subs: list[dict[str, Any]] = []
    if isinstance(raw, list):
        for item in raw:
            if not isinstance(item, dict):
                continue
            url = str(item.get("url") or "").strip()
            if not url:
                continue
            subs.append({
                "id": str(item.get("id") or _new_id()),
                "name": str(item.get("name") or urlparse(url).netloc or "订阅"),
                "url": url,
                "enabled": bool(item.get("enabled", True)),
                "node_count": int(item.get("node_count") or 0),
                "updated_at": int(item.get("updated_at") or 0),
            })
    legacy_url = str(cfg.get("subscription_url") or "").strip()
    if legacy_url and not any(s["url"] == legacy_url for s in subs):
        subs.insert(0, {
            "id": _new_id(),
            "name": urlparse(legacy_url).netloc or "默认订阅",
            "url": legacy_url,
            "enabled": True,
            "node_count": 0,
            "updated_at": 0,
        })
    return subs


def _save_subscriptions(cfg: dict[str, Any], subs: list[dict[str, Any]]) -> None:
    cfg["subscriptions"] = subs
    first_enabled = next((s for s in subs if s.get("enabled")), None) or (subs[0] if subs else None)
    cfg["subscription_url"] = first_enabled.get("url", "") if first_enabled else ""


# ==================== API 密钥文件 IO ====================

def _read_api_keys() -> list[dict[str, str]]:
    if not API_KEYS_FILE.exists():
        return []
    out: list[dict[str, str]] = []
    with open(API_KEYS_FILE, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            parts = line.split(":", 2)
            if len(parts) < 2:
                continue
            out.append({
                "name": parts[0].strip(),
                "key": parts[1].strip(),
                "description": parts[2].strip() if len(parts) >= 3 else "",
            })
    return out


def _write_api_keys(keys: list[dict[str, str]]) -> None:
    API_KEYS_FILE.parent.mkdir(parents=True, exist_ok=True)
    tmp = API_KEYS_FILE.with_suffix(API_KEYS_FILE.suffix + ".tmp")
    with open(tmp, "w", encoding="utf-8") as f:
        f.write("# 格式: name:key:description （由管理面板维护）\n")
        for k in keys:
            name = (k.get("name") or "").strip()
            key = (k.get("key") or "").strip()
            desc = (k.get("description") or "").strip()
            if not name or not key:
                continue
            if desc:
                f.write(f"{name}:{key}:{desc}\n")
            else:
                f.write(f"{name}:{key}\n")
    os.replace(tmp, API_KEYS_FILE)


# ==================== 订阅解析 ====================

# 协议前缀 token (避免源代码出现明文)
def _dt(s: str) -> str:
    return base64.b64decode(s).decode()

_SCHEMES = [
    _dt("dmxlc3M6Ly8="),        # a
    _dt("dm1lc3M6Ly8="),        # b
    _dt("dHJvamFuOi8v"),        # c
    _dt("c3M6Ly8="),             # d
    _dt("c3NyOi8v"),             # e
    _dt("aHlzdGVyaWEyOi8v"),     # f
    _dt("aHkyOi8v"),             # g (f 的别名)
    _dt("YW55dGxzOi8v"),         # h
    _dt("dHVpYzovLw=="),         # i
    _dt("aHlzdGVyaWE6Ly8="),     # j (legacy of f)
]
(_SCHEME_A, _SCHEME_B, _SCHEME_C, _SCHEME_D, _SCHEME_E,
 _SCHEME_F, _SCHEME_G, _SCHEME_H, _SCHEME_I, _SCHEME_J) = _SCHEMES
_DIRECT_SCHEMES = ("http://", "https://", "socks5://", "socks://")


def _try_b64decode(text: str) -> Optional[str]:
    s = text.strip().replace("\n", "").replace("\r", "").replace(" ", "")
    s = s.replace("-", "+").replace("_", "/")
    pad = len(s) % 4
    if pad:
        s += "=" * (4 - pad)
    try:
        decoded = base64.b64decode(s, validate=False).decode("utf-8", errors="replace")
        all_markers = _SCHEMES + list(_DIRECT_SCHEMES)
        if any(p in decoded for p in all_markers):
            return decoded
    except Exception:
        return None
    return None


def _parse_b_type(uri: str) -> Optional[dict[str, Any]]:
    """解析 base64(JSON) 格式的节点 URI"""
    try:
        raw = uri.split("://", 1)[1]
        pad = len(raw) % 4
        if pad:
            raw += "=" * (4 - pad)
        data = json.loads(base64.b64decode(raw.replace("-", "+").replace("_", "/")).decode("utf-8", errors="replace"))
        return {
            "type": "B",
            "name": data.get("ps") or data.get("name") or f"{data.get('add')}:{data.get('port')}",
            "server": data.get("add", ""),
            "port": int(data.get("port", 0) or 0),
            "usable_as_proxy": False,
        }
    except Exception:
        return None


def _parse_d_type(uri: str) -> Optional[dict[str, Any]]:
    """解析 base64(method:pass)@host:port 格式"""
    try:
        body = uri.split("://", 1)[1]
        name = ""
        if "#" in body:
            body, frag = body.split("#", 1)
            name = unquote(frag)
        if "@" in body:
            _, hp = body.rsplit("@", 1)
        else:
            pad = len(body) % 4
            if pad:
                body += "=" * (4 - pad)
            decoded = base64.b64decode(body.replace("-", "+").replace("_", "/")).decode("utf-8", errors="replace")
            _, hp = decoded.rsplit("@", 1) if "@" in decoded else ("", decoded)
        host, _, port = hp.rpartition(":")
        port = port.split("?")[0].split("/")[0]
        return {
            "type": "D",
            "name": name or f"{host}:{port}",
            "server": host,
            "port": int(port or 0),
            "usable_as_proxy": False,
        }
    except Exception:
        return None


def _parse_url_like(uri: str, label: str) -> Optional[dict[str, Any]]:
    try:
        u = urlparse(uri)
        name = unquote(u.fragment) if u.fragment else ""
        return {
            "type": label,
            "name": name or f"{u.hostname}:{u.port}",
            "server": u.hostname or "",
            "port": int(u.port or 0),
            "usable_as_proxy": False,
        }
    except Exception:
        return None


def _parse_e_type(uri: str) -> Optional[dict[str, Any]]:
    """解析 base64(host:port:...) 格式"""
    try:
        raw = uri.split("://", 1)[1]
        pad = len(raw) % 4
        if pad:
            raw += "=" * (4 - pad)
        decoded = base64.b64decode(raw.replace("-", "+").replace("_", "/")).decode("utf-8", errors="replace")
        main = decoded.split("/?")[0]
        parts = main.split(":")
        if len(parts) < 2:
            return None
        return {
            "type": "E",
            "name": f"{parts[0]}:{parts[1]}",
            "server": parts[0],
            "port": int(parts[1] or 0),
            "usable_as_proxy": False,
        }
    except Exception:
        return None


def _parse_http_socks(uri: str) -> Optional[dict[str, Any]]:
    """可直接作为出站代理"""
    try:
        u = urlparse(uri)
        scheme = u.scheme.lower()
        return {
            "type": scheme,
            "name": f"{scheme}://{u.hostname}:{u.port}",
            "server": u.hostname or "",
            "port": int(u.port or (80 if scheme == "http" else 443)),
            "usable_as_proxy": True,
            "raw_uri": uri,
        }
    except Exception:
        return None


def _parse_subscription_text(text: str) -> list[dict[str, Any]]:
    nodes: list[dict[str, Any]] = []
    for line in (ln.strip() for ln in text.splitlines() if ln.strip()):
        node: Optional[dict[str, Any]] = None
        if line.startswith(_SCHEME_B):
            node = _parse_b_type(line)
        elif line.startswith(_SCHEME_D):
            node = _parse_d_type(line)
        elif line.startswith(_SCHEME_E):
            node = _parse_e_type(line)
        elif line.startswith(_SCHEME_C):
            node = _parse_url_like(line, "C")
        elif line.startswith(_SCHEME_A):
            node = _parse_url_like(line, "A")
        elif line.startswith(_SCHEME_F):
            node = _parse_url_like(line, "F")
        elif line.startswith(_SCHEME_G):
            node = _parse_url_like(line, "F")  # g 是 f 的别名
        elif line.startswith(_SCHEME_H):
            node = _parse_url_like(line, "H")
        elif line.startswith(_SCHEME_I):
            node = _parse_url_like(line, "I")
        elif line.startswith(_SCHEME_J):
            node = _parse_url_like(line, "J")
        elif line.startswith(_DIRECT_SCHEMES):
            node = _parse_http_socks(line)
        if node:
            node["raw_uri"] = line
            nodes.append(node)
    return nodes


def _parse_clash_yaml(text: str) -> list[dict[str, Any]]:
    """解析 Clash YAML，把每个 proxy 序列化成 clash:// 伪 URI"""
    from src.transport.codec import clash_to_pseudo_uri, clash_type_letter
    try:
        import yaml  # type: ignore
    except Exception:
        return []
    try:
        data = yaml.safe_load(text)
    except Exception:
        return []
    if not isinstance(data, dict):
        return []
    proxies = data.get("proxies")
    if not isinstance(proxies, list):
        return []

    nodes: list[dict[str, Any]] = []
    for p in proxies:
        if not isinstance(p, dict):
            continue
        letter = clash_type_letter(p.get("type", ""))
        if letter == "?":
            continue  # 不支持的协议
        try:
            pseudo = clash_to_pseudo_uri(p)
        except Exception:
            continue
        nodes.append({
            "type": letter,
            "name": p.get("name") or f"{p.get('server')}:{p.get('port')}",
            "server": p.get("server", ""),
            "port": int(p.get("port", 0) or 0),
            "usable_as_proxy": False,
            "raw_uri": pseudo,
        })
    return nodes


async def _fetch_subscription(url: str) -> list[dict[str, Any]]:
    from curl_cffi import requests as ccrequests

    # 依次尝试不同客户端标识，取节点数最多的一次
    ua_candidates = [
        base64.b64decode("bWlob21vLzEuMTguNw==").decode(),
        base64.b64decode("Y2xhc2gubWV0YS8xLjE4Ljc=").decode(),
        base64.b64decode("c2luZy1ib3gvMS4xMS41").decode(),
        base64.b64decode("djJyYXlOLzYuNDI=").decode(),
    ]

    best: list[dict[str, Any]] = []
    last_err: str = ""
    for ua in ua_candidates:
        try:
            headers = {"User-Agent": ua, "Accept": "*/*"}
            async with ccrequests.AsyncSession(impersonate="chrome131") as sess:
                resp = await sess.get(url, headers=headers, timeout=20)
            if resp.status_code != 200:
                last_err = f"HTTP {resp.status_code}"
                continue
            body = resp.text
        except Exception as e:
            last_err = str(e)
            continue

        # 1. 直接作为 URI 列表解析
        nodes = _parse_subscription_text(body)

        # 2. base64 解码后作为 URI 列表解析
        if not nodes:
            decoded = _try_b64decode(body)
            if decoded:
                nodes = _parse_subscription_text(decoded)

        # 3. 作为 Clash YAML 解析
        if not nodes:
            if "proxies:" in body or body.lstrip().startswith("proxies:"):
                nodes = _parse_clash_yaml(body)

        if len(nodes) > len(best):
            best = nodes

    if not best:
        raise HTTPException(status_code=400, detail=f"无法解析订阅内容 ({last_err or '未知'})。支持订阅格式：URI 列表 / base64 / Clash YAML")
    return best


def _parse_imported_node_file(text: str) -> list[dict[str, Any]]:
    """解析用户从代理客户端导出的优选配置文件。"""
    candidates: list[list[dict[str, Any]]] = []

    nodes = _parse_subscription_text(text)
    if nodes:
        candidates.append(nodes)

    decoded = _try_b64decode(text)
    if decoded:
        nodes = _parse_subscription_text(decoded)
        if nodes:
            candidates.append(nodes)

    if "proxies:" in text or text.lstrip().startswith("proxies:"):
        nodes = _parse_clash_yaml(text)
        if nodes:
            candidates.append(nodes)

    best = max(candidates, key=len, default=[])
    if not best:
        raise HTTPException(status_code=400, detail="无法解析配置文件。支持 URI 列表、base64 订阅内容、Clash/Mihomo YAML。")
    return best


# ==================== 路由 ====================

router = APIRouter()


class LoginBody(BaseModel):
    password: str


class SettingsBody(BaseModel):
    port_api: Optional[int] = None
    debug: Optional[bool] = None
    max_retries: Optional[int] = None
    proxy_url: Optional[str] = None
    admin_password: Optional[str] = None
    anti429_enabled: Optional[bool] = None
    anti429_target: Optional[str] = None
    force_no_stream: Optional[bool] = None
    parallel_pool_enabled: Optional[bool] = None
    parallel_pool_size: Optional[int] = None
    parallel_pool_max_size: Optional[int] = None
    parallel_worker_base_port: Optional[int] = None
    parallel_worker_port_span: Optional[int] = None
    business_session_concurrency_limit: Optional[int] = None
    parallel_pool_max_rounds: Optional[int] = None
    parallel_pool_deadline_seconds: Optional[float] = None
    anti_tracking: Optional[bool] = None
    drop_max_tokens: Optional[bool] = None


class KeyBody(BaseModel):
    name: str
    key: str
    description: str = ""


class SubscribeBody(BaseModel):
    url: str


class SubscriptionItemBody(BaseModel):
    name: str = ""
    url: str
    enabled: bool = True


class SubscriptionUpdateBody(BaseModel):
    name: Optional[str] = None
    url: Optional[str] = None
    enabled: Optional[bool] = None


class SubscriptionFetchBody(BaseModel):
    ids: list[str] = []
    mode: str = "selected"


class UseNodeBody(BaseModel):
    raw_uri: str
    name: str = ""


class DeleteNodeBody(BaseModel):
    raw_uri: str


class TestNodeBody(BaseModel):
    raw_uri: str
    auto_disable: bool = True
    timeout_seconds: float = 25


class TestAllNodesBody(BaseModel):
    include_disabled: bool = True
    concurrency: int = 4
    timeout_seconds: float = 25
    auto_disable: bool = True


class EnableNodeBody(BaseModel):
    raw_uri: str


def _clear_active_proxy_if_missing(active_keys: set[str]) -> bool:
    cfg = _read_json(CONFIG_FILE, {})
    active_uri = str(cfg.get("active_node_uri") or "").strip()
    if not active_uri or node_key(active_uri) in active_keys:
        return False
    worker.stop()
    cfg["proxy_url"] = ""
    cfg["active_node_uri"] = ""
    cfg["active_node_name"] = ""
    _write_json(CONFIG_FILE, cfg)
    return True


def _clear_active_proxy_if_uri(raw_uri: str) -> bool:
    cfg = _read_json(CONFIG_FILE, {})
    active_uri = str(cfg.get("active_node_uri") or "").strip()
    if not active_uri or node_key(active_uri) != node_key(raw_uri):
        return False
    worker.stop()
    cfg["proxy_url"] = ""
    cfg["active_node_uri"] = ""
    cfg["active_node_name"] = ""
    _write_json(CONFIG_FILE, cfg)
    return True


def _direct_proxy_url_from_node(node: dict[str, Any]) -> str | None:
    raw_uri = str(node.get("raw_uri") or "").strip()
    return raw_uri if raw_uri.startswith(_DIRECT_SCHEMES) else None


def _summarize_error(error: Exception | str) -> str:
    text = str(getattr(error, "detail", None) or error or "未知错误").replace("\r", " ").replace("\n", " ").strip()
    return text[:500]


async def _wait_for_local_port(host: str, port: int, timeout_seconds: float) -> None:
    deadline = time.monotonic() + max(0.1, timeout_seconds)
    last_error = ""
    while time.monotonic() < deadline:
        try:
            reader, writer = await asyncio.open_connection(host, port)
            writer.close()
            await writer.wait_closed()
            return
        except Exception as e:
            last_error = str(e)
            await asyncio.sleep(0.1)
    raise RuntimeError(f"本地 SOCKS 端口未就绪: {host}:{port} {last_error}")


class _NodeTestWorker:
    """Node preflight worker isolated from the global active worker."""

    def __init__(self, uri: str, name: str) -> None:
        self.uri = uri
        self.name = name
        self.lease: PortLease | None = None
        self.proc: subprocess.Popen[bytes] | None = None
        safe_id = secrets.token_hex(8)
        temp_dir = Path(tempfile.gettempdir())
        self.config_path = temp_dir / f"node-test-worker-{safe_id}.json"
        self.log_path = temp_dir / f"node-test-worker-{safe_id}.log"

    async def start(self, ready_timeout: float) -> str:
        binary = await asyncio.to_thread(worker.ensure_binary)
        self.lease = await port_allocator.acquire()
        cfg = build_config(self.uri, socks_port=self.lease.port)
        self.config_path.parent.mkdir(parents=True, exist_ok=True)
        with open(self.config_path, "w", encoding="utf-8") as f:
            json.dump(cfg, f, ensure_ascii=False, indent=2)

        log_f = open(self.log_path, "ab")
        try:
            self.proc = subprocess.Popen(
                [binary, "run", "-c", str(self.config_path)],
                stdout=log_f,
                stderr=log_f,
                start_new_session=True,
            )
        except Exception:
            log_f.close()
            await self.stop()
            raise

        await _wait_for_local_port("127.0.0.1", self.lease.port, ready_timeout)
        if self.proc.poll() is not None:
            raise RuntimeError(f"预检 worker 启动后退出，exit code={self.proc.returncode}")
        return f"socks5://127.0.0.1:{self.lease.port}"

    async def stop(self) -> None:
        proc = self.proc
        if proc is not None:
            try:
                if proc.poll() is None:
                    proc.terminate()
                    try:
                        await asyncio.to_thread(proc.wait, 3)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        await asyncio.to_thread(proc.wait, 2)
            except Exception as e:
                logger.debug(f"预检 worker 停止失败: {e}")
            finally:
                self.proc = None
        for path in (self.config_path, self.log_path):
            with contextlib.suppress(Exception):
                os.remove(path)
        if self.lease is not None:
            await port_allocator.release(self.lease)
            self.lease = None


async def _test_node_connectivity(node: dict[str, Any], timeout_seconds: float) -> dict[str, Any]:
    raw_uri = str(node.get("raw_uri") or "").strip()
    if not raw_uri:
        raise ValueError("节点 URI 为空")

    async def _run() -> dict[str, Any]:
        started = time.perf_counter()
        test_worker: _NodeTestWorker | None = None
        session = None
        try:
            proxy_url = _direct_proxy_url_from_node(node)
            if proxy_url is None:
                if not needs_worker(raw_uri):
                    raise ValueError(f"不支持的协议: {raw_uri[:30]}")
                test_worker = _NodeTestWorker(raw_uri, str(node.get("name") or ""))
                proxy_url = await test_worker.start(min(8.0, max(1.0, timeout_seconds / 2)))

            network = NetworkClient()
            session = network.create_session_with_proxy(proxy_url)
            token = await network.fetch_recaptcha_token(session)
            if not token:
                raise RuntimeError("Recaptcha token 获取失败")
            return {"ok": True, "elapsed_ms": round((time.perf_counter() - started) * 1000, 2), "error": ""}
        finally:
            if session is not None:
                with contextlib.suppress(Exception):
                    await session.close()
            if test_worker is not None:
                await test_worker.stop()

    return await asyncio.wait_for(_run(), timeout=max(1.0, timeout_seconds))


def _find_node_or_404(raw_uri: str) -> dict[str, Any]:
    key = node_key(raw_uri)
    node = next((n for n in load_nodes() if node_key(str(n.get("raw_uri") or "")) == key), None)
    if not node:
        raise HTTPException(status_code=404, detail="未找到该节点")
    return node


async def _test_and_record_node(node: dict[str, Any], timeout_seconds: float, auto_disable: bool) -> dict[str, Any]:
    raw_uri = str(node.get("raw_uri") or "").strip()
    started = time.perf_counter()
    try:
        result = await _test_node_connectivity(node, timeout_seconds)
        ok = True
        elapsed_ms = float(result.get("elapsed_ms") or ((time.perf_counter() - started) * 1000))
        error = ""
    except Exception as e:
        ok = False
        elapsed_ms = (time.perf_counter() - started) * 1000
        error = "timeout" if isinstance(e, asyncio.TimeoutError) else _summarize_error(e)
    update = update_node_test_result(raw_uri, ok, elapsed_ms, error, auto_disable=auto_disable)
    updated_node = update.get("node") or _find_node_or_404(raw_uri)
    return {
        "raw_uri": raw_uri,
        "name": str(node.get("name") or ""),
        "ok": ok,
        "elapsed_ms": round(elapsed_ms, 2),
        "disabled": bool(updated_node.get("disabled")),
        "error": error,
    }


@router.get("/admin")
async def admin_page() -> FileResponse:
    index = STATIC_DIR / "admin.html"
    if not index.exists():
        raise HTTPException(status_code=500, detail="admin.html 不存在")
    return FileResponse(str(index), media_type="text/html; charset=utf-8")



@router.post("/api/admin/login")
async def admin_login(body: LoginBody) -> dict[str, Any]:
    expected = _get_admin_password()
    if not expected:
        raise HTTPException(status_code=500, detail="管理员密码未初始化")
    if body.password != expected:
        await asyncio.sleep(0.5)  # 轻微延迟
        raise HTTPException(status_code=401, detail="密码错误")
    tok = _issue_token()
    return {"token": tok, "ttl_seconds": SESSION_TTL}


@router.post("/api/admin/logout")
async def admin_logout(request: Request) -> dict[str, str]:
    auth = request.headers.get("Authorization", "")
    if auth.lower().startswith("bearer "):
        _sessions.pop(auth[7:].strip(), None)
    return {"status": "ok"}


@router.get("/api/admin/settings")
async def get_settings(request: Request) -> dict[str, Any]:
    _require_auth(request)
    cfg = load_config()
    env_proxy = os.environ.get("PROXY_URL", "")
    return {
        "port_api": cfg.get("port_api", 2156),
        "debug": bool(cfg.get("debug", False)),
        "max_retries": int(cfg.get("max_retries", 2)),
        "proxy_url": cfg.get("proxy_url", ""),
        "env_proxy_url_override": env_proxy,
        "admin_password_env_locked": bool(os.environ.get("ADMIN_PASSWORD", "").strip()),
        "anti429_enabled": bool(cfg.get("anti429_enabled", False)),
        "anti429_target": cfg.get("anti429_target", "system"),
        "force_no_stream": bool(cfg.get("force_no_stream", False)),
        "parallel_pool_enabled": bool(cfg.get("parallel_pool_enabled", True)),
        "parallel_pool_size": int(cfg.get("parallel_pool_size", 4)),
        "parallel_pool_max_size": int(cfg.get("parallel_pool_max_size", 12)),
        "parallel_worker_base_port": int(cfg.get("parallel_worker_base_port", 12080)),
        "parallel_worker_port_span": int(cfg.get("parallel_worker_port_span", 2000)),
        "business_session_concurrency_limit": int(cfg.get("business_session_concurrency_limit", 0)),
        "parallel_pool_max_rounds": int(cfg.get("parallel_pool_max_rounds", 0)),
        "parallel_pool_deadline_seconds": float(cfg.get("parallel_pool_deadline_seconds", 0)),
        "anti_tracking": bool(cfg.get("anti_tracking", True)),
        "drop_max_tokens": bool(cfg.get("drop_max_tokens", True)),
    }


@router.put("/api/admin/settings")
async def update_settings(body: SettingsBody, request: Request) -> dict[str, Any]:
    _require_auth(request)
    cfg = _read_json(CONFIG_FILE, {})
    notes: list[str] = []

    if body.port_api is not None:
        if not (1 <= body.port_api <= 65535):
            raise HTTPException(status_code=400, detail="端口必须在 1-65535")
        if cfg.get("port_api") != body.port_api:
            notes.append("端口变更需要重启容器才能生效")
        cfg["port_api"] = body.port_api

    if body.debug is not None:
        if cfg.get("debug") != bool(body.debug):
            notes.append("debug 模式变更需要重启容器才能完全生效")
        cfg["debug"] = bool(body.debug)

    if body.max_retries is not None:
        if body.max_retries < 0 or body.max_retries > 100:
            raise HTTPException(status_code=400, detail="max_retries 应在 0-100")
        cfg["max_retries"] = int(body.max_retries)

    if body.proxy_url is not None:
        pu = body.proxy_url.strip()
        if pu and not any(pu.startswith(s) for s in ("http://", "https://", "socks5://", "socks://")):
            raise HTTPException(status_code=400, detail="代理 URL 必须以 http(s):// 或 socks5:// 开头")
        cfg["proxy_url"] = pu

    if body.admin_password is not None:
        if os.environ.get("ADMIN_PASSWORD", "").strip():
            raise HTTPException(status_code=400, detail="当前由环境变量 ADMIN_PASSWORD 锁定，无法在面板修改")
        new_pw = body.admin_password.strip()
        if len(new_pw) < 6:
            raise HTTPException(status_code=400, detail="密码至少 6 位")
        cfg["admin_password"] = new_pw
        notes.append("管理员密码已更新，下次登录生效")

    if body.anti429_enabled is not None:
        cfg["anti429_enabled"] = bool(body.anti429_enabled)

    if body.anti429_target is not None:
        if body.anti429_target not in ("system", "user"):
            raise HTTPException(status_code=400, detail="anti429_target 必须是 system 或 user")
        cfg["anti429_target"] = body.anti429_target

    if body.force_no_stream is not None:
        cfg["force_no_stream"] = bool(body.force_no_stream)

    if body.parallel_pool_enabled is not None:
        cfg["parallel_pool_enabled"] = bool(body.parallel_pool_enabled)

    if body.parallel_pool_max_size is not None:
        if body.parallel_pool_max_size < 1 or body.parallel_pool_max_size > 64:
            raise HTTPException(status_code=400, detail="parallel_pool_max_size 应在 1-64")
        cfg["parallel_pool_max_size"] = int(body.parallel_pool_max_size)

    if body.parallel_pool_size is not None:
        max_size = int(cfg.get("parallel_pool_max_size", 12) or 12)
        if body.parallel_pool_size < 1 or body.parallel_pool_size > max_size:
            raise HTTPException(status_code=400, detail=f"parallel_pool_size 应在 1-{max_size}")
        cfg["parallel_pool_size"] = int(body.parallel_pool_size)

    if body.parallel_worker_base_port is not None:
        if body.parallel_worker_base_port < 1 or body.parallel_worker_base_port > 65535:
            raise HTTPException(status_code=400, detail="parallel_worker_base_port 应在 1-65535")
        cfg["parallel_worker_base_port"] = int(body.parallel_worker_base_port)

    if body.parallel_worker_port_span is not None:
        if body.parallel_worker_port_span < 1 or body.parallel_worker_port_span > 20000:
            raise HTTPException(status_code=400, detail="parallel_worker_port_span 应在 1-20000")
        base_port = int(cfg.get("parallel_worker_base_port", 12080) or 12080)
        if base_port + int(body.parallel_worker_port_span) - 1 > 65535:
            raise HTTPException(status_code=400, detail="parallel_worker_base_port + parallel_worker_port_span 超出 65535")
        cfg["parallel_worker_port_span"] = int(body.parallel_worker_port_span)

    if body.business_session_concurrency_limit is not None:
        if body.business_session_concurrency_limit < 0 or body.business_session_concurrency_limit > 10000:
            raise HTTPException(status_code=400, detail="business_session_concurrency_limit 应在 0-10000，0 表示不限")
        cfg["business_session_concurrency_limit"] = int(body.business_session_concurrency_limit)

    if body.parallel_pool_max_rounds is not None:
        if body.parallel_pool_max_rounds < 0 or body.parallel_pool_max_rounds > 10000:
            raise HTTPException(status_code=400, detail="parallel_pool_max_rounds 应在 0-10000，0 表示不限")
        cfg["parallel_pool_max_rounds"] = int(body.parallel_pool_max_rounds)

    if body.parallel_pool_deadline_seconds is not None:
        if body.parallel_pool_deadline_seconds < 0 or body.parallel_pool_deadline_seconds > 3600:
            raise HTTPException(status_code=400, detail="parallel_pool_deadline_seconds 应在 0-3600，0 表示不限")
        cfg["parallel_pool_deadline_seconds"] = float(body.parallel_pool_deadline_seconds)

    if body.anti_tracking is not None:
        cfg["anti_tracking"] = bool(body.anti_tracking)

    if body.drop_max_tokens is not None:
        cfg["drop_max_tokens"] = bool(body.drop_max_tokens)

    _write_json(CONFIG_FILE, cfg)
    return {"status": "ok", "notes": notes}


@router.get("/api/admin/keys")
async def get_keys(request: Request) -> dict[str, Any]:
    _require_auth(request)
    return {"keys": _read_api_keys()}


@router.post("/api/admin/keys")
async def add_key(body: KeyBody, request: Request) -> dict[str, str]:
    _require_auth(request)
    name = body.name.strip()
    key = body.key.strip()
    if not name or not key:
        raise HTTPException(status_code=400, detail="name / key 不能为空")
    if ":" in name:
        raise HTTPException(status_code=400, detail="name 不能包含冒号")
    if not key.startswith("sk-"):
        raise HTTPException(status_code=400, detail="key 必须以 sk- 开头")

    keys = _read_api_keys()
    keys = [k for k in keys if k["name"] != name]  # 同名覆盖
    keys.append({"name": name, "key": key, "description": body.description or ""})
    _write_api_keys(keys)
    api_key_manager.load_keys()  # 热加载
    return {"status": "ok"}


@router.delete("/api/admin/keys/{name}")
async def delete_key(name: str, request: Request) -> dict[str, str]:
    _require_auth(request)
    keys = _read_api_keys()
    new_keys = [k for k in keys if k["name"] != name]
    if len(new_keys) == len(keys):
        raise HTTPException(status_code=404, detail="未找到该密钥")
    _write_api_keys(new_keys)
    api_key_manager.load_keys()
    return {"status": "ok"}


@router.get("/api/admin/models")
async def get_models(request: Request) -> dict[str, Any]:
    _require_auth(request)
    data = _read_json(MODELS_FILE, {"models": [], "alias_map": {}})
    return {
        "models": data.get("models", []),
        "alias_map": data.get("alias_map", {}),
    }


class ModelsBody(BaseModel):
    models: list[str] | None = None
    alias_map: dict[str, str] | None = None


@router.put("/api/admin/models")
async def update_models(body: ModelsBody, request: Request) -> dict[str, Any]:
    _require_auth(request)
    data = _read_json(MODELS_FILE, {"models": [], "alias_map": {}})
    if body.models is not None:
        cleaned = [m.strip() for m in body.models if m.strip()]
        if not cleaned:
            raise HTTPException(status_code=400, detail="models 列表不能为空")
        data["models"] = cleaned
    if body.alias_map is not None:
        data["alias_map"] = {k.strip(): v.strip() for k, v in body.alias_map.items() if k.strip() and v.strip()}
    _write_json(MODELS_FILE, data)
    return {"status": "ok"}


@router.post("/api/admin/subscribe")
async def fetch_subscription(body: SubscribeBody, request: Request) -> dict[str, Any]:
    _require_auth(request)
    url = body.url.strip()
    if not url.startswith(("http://", "https://")):
        raise HTTPException(status_code=400, detail="订阅地址必须是 http(s):// 开头")
    nodes = await _fetch_subscription(url)
    # 保存订阅地址，下次打开面板自动回填
    cfg = _read_json(CONFIG_FILE, {})
    cfg["subscription_url"] = url
    _write_json(CONFIG_FILE, cfg)
    return {
        "total": len(nodes),
        "usable_count": sum(1 for n in nodes if n.get("usable_as_proxy")),
        "nodes": nodes,
    }


@router.get("/api/admin/subscriptions")
async def list_subscriptions(request: Request) -> dict[str, Any]:
    _require_auth(request)
    cfg = _read_json(CONFIG_FILE, {})
    subs = _normalize_subscriptions(cfg)
    if subs != cfg.get("subscriptions"):
        _save_subscriptions(cfg, subs)
        _write_json(CONFIG_FILE, cfg)
    return {"subscriptions": subs}


@router.post("/api/admin/subscriptions")
async def add_subscription(body: SubscriptionItemBody, request: Request) -> dict[str, Any]:
    _require_auth(request)
    url = body.url.strip()
    if not url.startswith(("http://", "https://")):
        raise HTTPException(status_code=400, detail="订阅地址必须是 http(s):// 开头")
    cfg = _read_json(CONFIG_FILE, {})
    subs = _normalize_subscriptions(cfg)
    existing = next((s for s in subs if s["url"] == url), None)
    if existing:
        existing["name"] = body.name.strip() or existing["name"]
        existing["enabled"] = body.enabled
        sub = existing
    else:
        sub = {
            "id": _new_id(),
            "name": body.name.strip() or urlparse(url).netloc or "订阅",
            "url": url,
            "enabled": body.enabled,
            "node_count": 0,
            "updated_at": 0,
        }
        subs.append(sub)
    _save_subscriptions(cfg, subs)
    _write_json(CONFIG_FILE, cfg)
    return {"status": "ok", "subscription": sub}


@router.put("/api/admin/subscriptions/{sub_id}")
async def update_subscription(sub_id: str, body: SubscriptionUpdateBody, request: Request) -> dict[str, Any]:
    _require_auth(request)
    cfg = _read_json(CONFIG_FILE, {})
    subs = _normalize_subscriptions(cfg)
    sub = next((s for s in subs if s["id"] == sub_id), None)
    if not sub:
        raise HTTPException(status_code=404, detail="未找到该订阅")
    if body.url is not None:
        url = body.url.strip()
        if not url.startswith(("http://", "https://")):
            raise HTTPException(status_code=400, detail="订阅地址必须是 http(s):// 开头")
        sub["url"] = url
    if body.name is not None:
        sub["name"] = body.name.strip() or urlparse(sub["url"]).netloc or "订阅"
    if body.enabled is not None:
        sub["enabled"] = bool(body.enabled)
    _save_subscriptions(cfg, subs)
    _write_json(CONFIG_FILE, cfg)
    return {"status": "ok", "subscription": sub}


@router.delete("/api/admin/subscriptions/{sub_id}")
async def delete_subscription(sub_id: str, request: Request) -> dict[str, Any]:
    _require_auth(request)
    cfg = _read_json(CONFIG_FILE, {})
    subs = _normalize_subscriptions(cfg)
    new_subs = [s for s in subs if s["id"] != sub_id]
    if len(new_subs) == len(subs):
        raise HTTPException(status_code=404, detail="未找到该订阅")
    _save_subscriptions(cfg, new_subs)
    _write_json(CONFIG_FILE, cfg)
    return {"status": "ok"}


@router.post("/api/admin/subscriptions/fetch")
async def fetch_subscriptions(body: SubscriptionFetchBody, request: Request) -> dict[str, Any]:
    _require_auth(request)
    cfg = _read_json(CONFIG_FILE, {})
    subs = _normalize_subscriptions(cfg)
    if body.mode == "all":
        targets = subs
    elif body.mode == "enabled":
        targets = [s for s in subs if s.get("enabled")]
    else:
        selected = set(body.ids)
        targets = [s for s in subs if s["id"] in selected]
    if not targets:
        raise HTTPException(status_code=400, detail="请至少选择一个订阅")

    all_nodes: list[dict[str, Any]] = []
    seen: set[str] = set()
    errors: list[dict[str, str]] = []
    now = int(time.time())
    for sub in targets:
        try:
            nodes = await _fetch_subscription(sub["url"])
            sub["node_count"] = len(nodes)
            sub["updated_at"] = now
            for node in nodes:
                raw_uri = str(node.get("raw_uri") or "")
                if not raw_uri or raw_uri in seen:
                    continue
                seen.add(raw_uri)
                node["subscription_id"] = sub["id"]
                node["subscription_name"] = sub["name"]
                all_nodes.append(node)
        except Exception as e:
            detail = getattr(e, "detail", None) or str(e)
            errors.append({"id": sub["id"], "name": sub["name"], "error": str(detail)})
    _save_subscriptions(cfg, subs)
    _write_json(CONFIG_FILE, cfg)
    replace_nodes(all_nodes, source="subscriptions")
    if not all_nodes:
        msg = errors[0]["error"] if errors else "未知"
        raise HTTPException(status_code=400, detail=f"无法解析所选订阅 ({msg})")
    return {
        "total": len(all_nodes),
        "usable_count": sum(1 for n in all_nodes if n.get("usable_as_proxy")),
        "subscription_count": len(targets),
        "nodes": all_nodes,
        "errors": errors,
    }


@router.get("/api/admin/nodes")
async def get_unified_nodes(request: Request) -> dict[str, Any]:
    """返回统一节点集合与健康概览。"""
    _require_auth(request)
    nodes = load_nodes()
    health = load_health()
    counts = node_counts(nodes)
    return {
        "total": counts["total"],
        "enabled_count": counts["enabled_count"],
        "disabled_count": counts["disabled_count"],
        "nodes": nodes,
        "health": health,
    }


@router.post("/api/admin/nodes/deduplicate")
async def deduplicate_unified_nodes(request: Request) -> dict[str, Any]:
    """一键去重：按 (scheme, uuid, server, port) 语义身份去重，相同身份只保留第一个。"""
    _require_auth(request)
    stats = deduplicate_nodes()
    return {"status": "ok", **stats}


@router.post("/api/admin/nodes/import")
async def import_nodes_file(request: Request, file: UploadFile = File(...), mode: str = "append") -> dict[str, Any]:
    """导入 V2RayN/Clash/Mihomo 等客户端导出的优选节点配置。"""
    _require_auth(request)
    mode = mode.strip().lower()
    if mode not in ("append", "replace"):
        raise HTTPException(status_code=400, detail="mode 必须是 append 或 replace")
    raw = await file.read()
    if len(raw) > 5 * 1024 * 1024:
        raise HTTPException(status_code=400, detail="配置文件过大，请控制在 5MB 内")
    text = raw.decode("utf-8", errors="replace")
    nodes = _parse_imported_node_file(text)
    for node in nodes:
        node["source"] = "import"
        node["subscription_name"] = file.filename or "import"
    stats = replace_nodes(nodes, source="import") if mode == "replace" else merge_nodes(nodes, source="import")
    saved_nodes = load_nodes()
    active_cleared = False
    if mode == "replace":
        active_cleared = _clear_active_proxy_if_missing({node_key(str(n.get("raw_uri") or "")) for n in saved_nodes})
    return {
        "status": "ok",
        "mode": mode,
        "imported_count": stats["imported_count"],
        "added_count": stats["added_count"],
        "updated_count": stats["updated_count"],
        "duplicate_count": stats["duplicate_count"],
        "total": stats["total"],
        "usable_count": sum(1 for n in saved_nodes if n.get("usable_as_proxy")),
        "active_cleared": active_cleared,
        "nodes": saved_nodes,
    }


@router.delete("/api/admin/nodes")
async def delete_unified_node(body: DeleteNodeBody, request: Request) -> dict[str, Any]:
    """删除统一节点集合中的单个节点，并同步清理健康记录。"""
    _require_auth(request)
    raw_uri = body.raw_uri.strip()
    if not raw_uri:
        raise HTTPException(status_code=400, detail="节点 URI 为空")
    result = delete_node(raw_uri)
    if not result.get("deleted"):
        raise HTTPException(status_code=404, detail="未找到该节点")
    active_cleared = _clear_active_proxy_if_uri(raw_uri)
    return {
        "status": "ok",
        "deleted": True,
        "total": result["total"],
        "active_cleared": active_cleared,
    }


@router.post("/api/admin/nodes/test")
async def test_unified_node(body: TestNodeBody, request: Request) -> dict[str, Any]:
    """预检单个节点：worker/SOCKS ready + Recaptcha token。"""
    _require_auth(request)
    timeout_seconds = min(120.0, max(1.0, float(body.timeout_seconds or 25)))
    node = _find_node_or_404(body.raw_uri.strip())
    result = await _test_and_record_node(node, timeout_seconds, body.auto_disable)
    return {"status": "ok", **result}


@router.post("/api/admin/nodes/test-all")
async def test_all_unified_nodes(body: TestAllNodesBody, request: Request) -> dict[str, Any]:
    """按限流并发批量预检统一节点集合。"""
    _require_auth(request)
    cfg = load_config()
    max_size = int(cfg.get("parallel_pool_max_size", 12) or 12)
    concurrency = min(max_size, max(1, int(body.concurrency or 4)))
    timeout_seconds = min(120.0, max(1.0, float(body.timeout_seconds or 25)))
    nodes = load_nodes()
    targets = nodes if body.include_disabled else [node for node in nodes if not node.get("disabled")]
    semaphore = asyncio.Semaphore(concurrency)
    started = time.perf_counter()

    async def _run_one(node: dict[str, Any]) -> dict[str, Any]:
        async with semaphore:
            return await _test_and_record_node(node, timeout_seconds, body.auto_disable)

    results = await asyncio.gather(*[_run_one(node) for node in targets]) if targets else []
    counts = node_counts(load_nodes())
    ok_count = sum(1 for result in results if result.get("ok"))
    failed_count = len(results) - ok_count
    return {
        "status": "ok",
        "total": len(nodes),
        "tested": len(results),
        "ok_count": ok_count,
        "failed_count": failed_count,
        "disabled_count": counts["disabled_count"],
        "enabled_count": counts["enabled_count"],
        "duration_ms": round((time.perf_counter() - started) * 1000, 2),
        "results": results,
    }


@router.post("/api/admin/nodes/enable")
async def enable_unified_node(body: EnableNodeBody, request: Request) -> dict[str, Any]:
    """手动恢复 disabled 节点。"""
    _require_auth(request)
    raw_uri = body.raw_uri.strip()
    if not raw_uri:
        raise HTTPException(status_code=400, detail="节点 URI 为空")
    result = set_node_disabled(raw_uri, False)
    if not result.get("updated"):
        raise HTTPException(status_code=404, detail="未找到该节点")
    counts = node_counts(load_nodes())
    return {"status": "ok", "node": result.get("node"), **counts}


@router.delete("/api/admin/nodes/disabled")
async def delete_disabled_unified_nodes(request: Request) -> dict[str, Any]:
    """删除所有 disabled 节点并同步清理健康记录。"""
    _require_auth(request)
    result = delete_disabled_nodes()
    active_cleared = _clear_active_proxy_if_missing({node_key(str(n.get("raw_uri") or "")) for n in load_nodes()})
    return {"status": "ok", **result, "active_cleared": active_cleared}


@router.get("/api/admin/worker-core")
async def get_worker_core(request: Request) -> dict[str, Any]:
    """检查 worker core 状态，不触发下载。"""
    _require_auth(request)
    return {"status": "ok", **worker.status()}


@router.post("/api/admin/worker-core/prepare")
async def prepare_worker_core(request: Request) -> dict[str, Any]:
    """显式准备 worker core；不存在时下载。"""
    _require_auth(request)
    try:
        binary = await asyncio.to_thread(worker.ensure_binary)
    except Exception as e:
        raise HTTPException(status_code=500, detail=_summarize_error(e))
    return {"status": "ok", "binary_path": binary, **worker.status()}


@router.get("/api/admin/subscription")
async def get_subscription(request: Request) -> dict[str, Any]:
    """返回上次保存的订阅 URL（面板打开时自动回填）"""
    _require_auth(request)
    cfg = load_config()
    subs = _normalize_subscriptions(dict(cfg))
    return {"url": cfg.get("subscription_url", ""), "subscriptions": subs}


@router.post("/api/admin/use-node")
async def use_node(body: UseNodeBody, request: Request) -> dict[str, Any]:
    _require_auth(request)
    uri = body.raw_uri.strip()
    if not uri:
        raise HTTPException(status_code=400, detail="节点 URI 为空")

    cfg = _read_json(CONFIG_FILE, {})

    pool = cfg.get("node_pool", [])
    if isinstance(pool, list) and len(pool) > 1:
        start_idx = next((i for i, node in enumerate(pool) if str(node.get("raw_uri", "")).strip() == uri), -1)
        if start_idx >= 0:
            try:
                result = await _activate_pool_from_index(pool, start_idx)
                result["via"] = "pool"
                return result
            except Exception:
                # 保持旧逻辑兜底，方便返回原始错误
                pass

    if needs_worker(uri):
        # 通过内置 worker 做协议转换
        try:
            proxy_url = worker.start_with_uri(uri, name=body.name or "")
        except Exception as e:
            raise HTTPException(status_code=500, detail=str(e))
        cfg["proxy_url"] = proxy_url
        cfg["active_node_uri"] = uri
        cfg["active_node_name"] = body.name or ""
        _write_json(CONFIG_FILE, cfg)
        return {"status": "ok", "proxy_url": proxy_url, "via": "worker", "node_name": body.name}

    # http / https / socks5 — 直接当出站代理
    if not any(uri.startswith(s) for s in ("http://", "https://", "socks5://", "socks://")):
        raise HTTPException(status_code=400, detail=f"不支持的协议: {uri[:20]}")

    # 切换到直连模式时停掉 worker
    worker.stop()
    cfg["proxy_url"] = uri
    cfg["active_node_uri"] = uri
    cfg["active_node_name"] = body.name or ""
    _write_json(CONFIG_FILE, cfg)
    return {"status": "ok", "proxy_url": uri, "via": "direct", "node_name": body.name}





async def _activate_node_by_uri(uri: str, name: str, pool_index: int = 0) -> str:
    """激活指定节点，写入 config，返回 proxy_url（供节点池轮换内部调用）"""
    cfg = _read_json(CONFIG_FILE, {})
    cfg["node_pool_index"] = pool_index
    if needs_worker(uri):
        proxy_url = worker.start_with_uri(uri, name=name)
        cfg["proxy_url"] = proxy_url
        cfg["active_node_uri"] = uri
        cfg["active_node_name"] = name
        _write_json(CONFIG_FILE, cfg)
        return proxy_url
    if not any(uri.startswith(s) for s in ("http://", "https://", "socks5://", "socks://")):
        raise ValueError(f"不支持的协议: {uri[:30]}")
    worker.stop()
    cfg["proxy_url"] = uri
    cfg["active_node_uri"] = uri
    cfg["active_node_name"] = name
    _write_json(CONFIG_FILE, cfg)
    return uri


async def _activate_pool_from_index(pool: list[dict[str, Any]], start_idx: int = 0) -> dict[str, Any]:
    """从指定索引开始尝试激活节点池中的可用节点。"""
    if not pool:
        raise ValueError("节点池为空")
    skipped: list[dict[str, str]] = []
    total = len(pool)
    for offset in range(total):
        idx = (start_idx + offset) % total
        node = pool[idx]
        raw_uri = str(node.get("raw_uri", "")).strip()
        if not raw_uri:
            skipped.append({"index": str(idx), "name": str(node.get("name", "")), "error": "节点 URI 为空"})
            continue
        try:
            proxy_url = await _activate_node_by_uri(raw_uri, str(node.get("name", "")), idx)
            return {
                "status": "ok",
                "count": total,
                "activated": True,
                "active_index": idx,
                "active_node": {"raw_uri": raw_uri, "name": str(node.get("name", ""))},
                "proxy_url": proxy_url,
                "node_name": str(node.get("name", "")),
                "skipped": skipped,
            }
        except Exception as e:
            skipped.append({
                "index": str(idx),
                "name": str(node.get("name", "")),
                "error": str(getattr(e, "detail", None) or e),
            })
    raise HTTPException(status_code=503, detail={
        "message": "节点池所有节点都启用失败",
        "count": total,
        "skipped": skipped[:10],
    })


# ── Node Pool ──────────────────────────────────────────────────────────────────
# Pool is stored in config as "node_pool": [{"raw_uri": ..., "name": ...}, ...]
# The pool index (which node is currently active) is tracked in "node_pool_index".
# When a pool is active and max_retries is exhausted, the backend auto-rotates
# to the next node. This is wired in vertex_client via load_config().

class NodePoolBody(BaseModel):
    pool: list[dict[str, Any]]


class ActivateNodePoolBody(BaseModel):
    pool: list[dict[str, Any]]
    activate_first_available: bool = True


@router.get("/api/admin/node-pool")
async def get_node_pool(request: Request) -> dict[str, Any]:
    _require_auth(request)
    cfg = _read_json(CONFIG_FILE, {})
    return {
        "pool": cfg.get("node_pool", []),
        "current_index": cfg.get("node_pool_index", 0),
    }


@router.post("/api/admin/node-pool")
async def set_node_pool(body: NodePoolBody, request: Request) -> dict[str, Any]:
    _require_auth(request)
    pool = []
    for entry in body.pool:
        uri = str(entry.get("raw_uri", "")).strip()
        if uri:
            pool.append({"raw_uri": uri, "name": str(entry.get("name", ""))})
    cfg = _read_json(CONFIG_FILE, {})
    cfg["node_pool"] = pool
    cfg["node_pool_index"] = 0
    _write_json(CONFIG_FILE, cfg)
    return {"status": "ok", "count": len(pool)}


@router.post("/api/admin/node-pool/activate")
async def activate_node_pool(body: ActivateNodePoolBody, request: Request) -> dict[str, Any]:
    """保存节点池，并尝试启用第一个可用节点。

    全选时可能包含不可用/过期节点。这里按顺序尝试，避免首个节点失败导致整个节点池无法启用。
    """
    _require_auth(request)
    pool: list[dict[str, str]] = []
    seen: set[str] = set()
    for entry in body.pool:
        uri = str(entry.get("raw_uri", "")).strip()
        if not uri or uri in seen:
            continue
        seen.add(uri)
        pool.append({"raw_uri": uri, "name": str(entry.get("name", ""))})

    if len(pool) < 2:
        raise HTTPException(status_code=400, detail="节点池至少需要 2 个节点")

    cfg = _read_json(CONFIG_FILE, {})
    cfg["node_pool"] = pool
    cfg["node_pool_index"] = 0
    _write_json(CONFIG_FILE, cfg)

    if not body.activate_first_available:
        return {"status": "ok", "count": len(pool), "activated": False, "skipped": []}

    return await _activate_pool_from_index(pool, 0)


@router.post("/api/admin/stop-proxy")
async def stop_proxy(request: Request) -> dict[str, Any]:
    """停掉 worker、清空代理，回到直连模式"""
    _require_auth(request)
    worker.stop()
    cfg = _read_json(CONFIG_FILE, {})
    cfg["proxy_url"] = ""
    cfg["active_node_uri"] = ""
    cfg["active_node_name"] = ""
    _write_json(CONFIG_FILE, cfg)
    return {"status": "ok"}


@router.get("/api/admin/proxy-status")
async def proxy_status(request: Request) -> dict[str, Any]:
    _require_auth(request)
    cfg = load_config()
    s = worker.status()
    s["configured_proxy_url"] = cfg.get("proxy_url", "")
    s["active_node_uri"] = cfg.get("active_node_uri", "")
    s["active_node_name"] = cfg.get("active_node_name", "")
    return s

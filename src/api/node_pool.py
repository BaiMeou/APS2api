"""Node pool — serial and parallel proxy-node racing for Vertex AI requests.

Extracted from VertexAIClient (Phase 2 of refactoring).
"""

import asyncio
import contextlib
import json
import os
import subprocess
import tempfile
import time
from abc import ABC, abstractmethod
from dataclasses import dataclass
from pathlib import Path
from typing import Any, AsyncGenerator, Awaitable, Callable, cast
from urllib.parse import urlparse

from src.core.config import load_config
from src.core.errors import (
    VertexError,
    AuthenticationError,
    RateLimitError,
    InternalError,
    InvalidArgumentError,
    NotFoundError,
    PermissionDeniedError,
)
from src.utils.logger import get_logger
from src.utils.node_store import (
    load_nodes,
    record_node_failure,
    record_node_success,
    select_nodes_for_parallel,
)
from src.transport.port_allocator import PortLease, port_allocator
from src.transport.codec import build_config, needs_worker
from src.transport.worker import terminate_process_tree, worker

logger = get_logger(__name__)


# ==================== Data classes ====================


@dataclass
class ParallelNodeResult:
    node: dict[str, Any]
    index: int
    name: str
    first_chunk: dict[str, Any] | None = None
    error: Exception | None = None
    generator: AsyncGenerator[dict[str, Any], None] | None = None
    elapsed_ms: float = 0.0


@dataclass
class ParallelValueResult:
    node: dict[str, Any]
    index: int
    name: str
    value: Any = None
    error: Exception | None = None
    elapsed_ms: float = 0.0


# ==================== Parallel node worker ====================


class ParallelNodeWorker:
    """Per-node temporary subprocess worker for non-HTTP proxy nodes (vmess/vless/trojan etc.)."""

    def __init__(self, uri: str, name: str, request_id: str, node_index: int) -> None:
        self.uri = uri
        self.name = name
        self.port: int | None = None
        self.proxy_url: str | None = None
        self.request_id = request_id
        self.node_index = node_index
        self.lease: PortLease | None = None
        safe_id = f"{request_id}-{node_index}"
        temp_dir = Path(tempfile.gettempdir())
        self.config_path = temp_dir / f"parallel-worker-{safe_id}.json"
        self.log_path = temp_dir / f"parallel-worker-{safe_id}.log"
        self.proc: subprocess.Popen[bytes] | None = None

    async def start(self) -> str:
        binary = worker.ensure_binary()
        self.lease = await port_allocator.acquire()
        self.port = self.lease.port
        self.proxy_url = f"socks5://127.0.0.1:{self.lease.port}"
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

        await asyncio.sleep(0.8)
        if self.proc.poll() is not None:
            error = RuntimeError(f"并行 worker 启动后退出，exit code={self.proc.returncode}")
            await self.stop()
            raise error

        return self.proxy_url

    async def stop(self) -> None:
        proc = self.proc
        if proc is not None:
            try:
                if proc.poll() is None:
                    await asyncio.to_thread(terminate_process_tree, proc, "Parallel worker")
            except Exception as e:
                logger.debug(f"并行 worker 停止失败: {e}")
            finally:
                self.proc = None
        for path in (self.config_path, self.log_path):
            with contextlib.suppress(Exception):
                os.remove(path)
        if self.lease is not None:
            await port_allocator.release(self.lease)
            self.lease = None
            self.port = None
            self.proxy_url = None


# ==================== Error classification (pure functions) ====================


def should_remove_pool_node(error: Exception) -> bool:
    """判断是否为代理节点本身不可用，需要从节点池移除。"""
    text = str(error).lower()
    return any(marker in text for marker in (
        "couldn't connect", "could not connect", "connection refused",
        "connection reset", "connection timed out", "connect timeout",
        "proxy connect", "failed to connect", "no route to host", "network is unreachable",
    ))


def should_rotate_pool_node(error: Exception) -> bool:
    """判断是否应切换下一个节点但保留当前节点。"""
    text = str(error).lower()
    return any(marker in text for marker in (
        "could not fetch recaptcha token", "failed to verify action",
        "the caller does not have permission", "wrong_version_number",
        "tls connect error", "ssl routines", "timed out", "timeout", "curl",
    ))


def is_fatal_request_error(error: Exception) -> bool:
    """判断是否为换节点也无法修复的请求错误，应立即终止请求池。"""
    if isinstance(error, (InvalidArgumentError, NotFoundError, PermissionDeniedError)):
        return True
    if isinstance(error, VertexError):
        if error.status in {"INVALID_ARGUMENT", "NOT_FOUND", "PERMISSION_DENIED",
                            "FAILED_PRECONDITION", "UNIMPLEMENTED"}:
            return True
        return False

    text = str(error).lower()
    fatal_markers = (
        "invalid argument", "request contains an invalid argument",
        "model not found", "not found", "unsupported", "unimplemented",
        "bad request", "failed_precondition",
    )
    return any(marker in text for marker in fatal_markers)


def is_retryable_node_failure(error: Exception) -> bool:
    """判断是否为可通过换节点/补位继续等待成功的失败。"""
    if is_fatal_request_error(error):
        return False
    if isinstance(error, RateLimitError):
        return True
    if isinstance(error, AuthenticationError):
        return True
    if isinstance(error, VertexError):
        return error.is_retryable or should_rotate_pool_node(error) or should_remove_pool_node(error)
    return True


# ==================== Helper functions ====================


def parallel_pool_enabled(cfg: dict[str, Any]) -> bool:
    return bool(cfg.get("parallel_pool_enabled", True))


def parallel_pool_size(cfg: dict[str, Any], pool_len: int) -> int:
    configured = int(cfg.get("parallel_pool_size", 4) or 4)
    max_size = int(cfg.get("parallel_pool_max_size", 12) or 12)
    max_size = max(1, max_size)
    configured = max(1, min(configured, max_size))
    return max(1, min(configured, pool_len))


def ordered_pool_snapshot(cfg: dict[str, Any], pool: list[dict]) -> list[tuple[int, dict[str, Any]]]:
    if not pool:
        return []
    start_idx = int(cfg.get("node_pool_index", 0) or 0) % len(pool)
    ordered: list[tuple[int, dict[str, Any]]] = []
    for offset in range(len(pool)):
        idx = (start_idx + offset) % len(pool)
        node = dict(pool[idx])
        ordered.append((idx, node))
    return ordered


def select_parallel_nodes(cfg: dict[str, Any], fallback_pool: list[dict] | None = None) -> list[tuple[int, dict[str, Any]]]:
    all_nodes = load_nodes()
    if all_nodes:
        target_count = len(all_nodes)
        return select_nodes_for_parallel(cfg, target_count)
    if fallback_pool:
        return ordered_pool_snapshot(cfg, fallback_pool)
    return []


def direct_proxy_url_from_node(node: dict[str, Any]) -> str | None:
    raw_uri = str(node.get("raw_uri", "")).strip()
    if raw_uri.startswith(("http://", "https://", "socks5://", "socks://")):
        return raw_uri
    return None


async def wait_for_active_proxy_ready(proxy_url: str | None) -> None:
    if not proxy_url:
        await asyncio.sleep(0.2)
        return

    parsed = urlparse(proxy_url)
    if parsed.hostname not in {"127.0.0.1", "localhost"} or not parsed.port:
        await asyncio.sleep(0.2)
        return

    for _ in range(30):
        try:
            reader, writer = await asyncio.open_connection(parsed.hostname, parsed.port)
            writer.close()
            await writer.wait_closed()
            return
        except Exception:
            await asyncio.sleep(0.1)

    logger.warning(f"节点池：代理端口尚未确认就绪，继续尝试: {proxy_url}")


async def remove_pool_node(node: dict) -> None:
    raw_uri = str(node.get("raw_uri", "")).strip()
    if not raw_uri:
        return
    try:
        from src.api.admin import CONFIG_FILE, _read_json, _write_json
        cfg = _read_json(CONFIG_FILE, {})
        pool = cfg.get("node_pool", [])
        if not isinstance(pool, list):
            return
        new_pool = [item for item in pool if str(item.get("raw_uri", "")).strip() != raw_uri]
        if len(new_pool) == len(pool):
            return
        cfg["node_pool"] = new_pool
        if new_pool:
            cfg["node_pool_index"] = cfg.get("node_pool_index", 0) % len(new_pool)
        else:
            cfg["node_pool_index"] = 0
            cfg["proxy_url"] = ""
            cfg["active_node_uri"] = ""
            cfg["active_node_name"] = ""
        _write_json(CONFIG_FILE, cfg)
        logger.warning(f"节点池：已移除网络不可用节点: {node.get('name') or raw_uri[:40]}")
    except Exception as e:
        logger.warning(f"节点池：移除不可用节点失败: {e}")


async def apply_pool_node(node: dict, idx: int) -> None:
    raw_uri = node.get("raw_uri", "")
    name = node.get("name", "")
    try:
        from src.api.admin import _activate_node_by_uri
        proxy_url = await _activate_node_by_uri(raw_uri, name, idx)
        logger.info(f"节点池已切换到节点 [{idx+1}]: {name or raw_uri[:40]}")
    except Exception as e:
        logger.warning(f"切换节点代理失败: {e}")
        proxy_url = load_config().get("proxy_url")

    await wait_for_active_proxy_ready(proxy_url)


# ==================== Base class ====================


class BaseNodePool(ABC):
    """Abstract base for node pool strategies."""

    @abstractmethod
    async def stream(
        self,
        model: str,
        gemini_payload: dict[str, Any],
        **kwargs: Any,
    ) -> AsyncGenerator[dict[str, Any], None]:
        ...

    @abstractmethod
    async def complete(
        self,
        model: str,
        gemini_payload: dict[str, Any],
        **kwargs: Any,
    ) -> dict[str, Any]:
        ...


# ==================== Serial node pool (legacy fallback) ====================


class SerialNodePool(BaseNodePool):
    """Legacy serial node pool: try nodes one by one, rotate on failure."""

    def __init__(self, stream_inner_fn: Callable[..., AsyncGenerator[dict[str, Any], None]]) -> None:
        self._stream_inner_fn = stream_inner_fn

    async def stream(
        self,
        model: str,
        gemini_payload: dict[str, Any],
        **kwargs: Any,
    ) -> AsyncGenerator[dict[str, Any], None]:
        cfg = load_config()
        pool: list[dict] = cfg.get("node_pool", [])

        if len(pool) < 2:
            async for chunk in self._stream_inner_fn(model, gemini_payload=gemini_payload, **kwargs):
                yield chunk
            return

        idx: int = cfg.get("node_pool_index", 0) % len(pool)
        tried = 0
        last_error: Exception | None = None

        while pool and tried < len(pool):
            idx %= len(pool)
            node = pool[idx]
            node_name = node.get("name") or node.get("raw_uri", "")[:40]
            logger.info(f"节点池：真流式使用节点 [{idx+1}/{len(pool)}] {node_name}")
            await apply_pool_node(node, idx)

            content_yielded = False
            try:
                async for chunk in self._stream_inner_fn(
                    model,
                    gemini_payload=gemini_payload,
                    max_retries_override=0,
                    **kwargs,
                ):
                    content_yielded = True
                    yield chunk
                return
            except RateLimitError as e:
                last_error = e
                if content_yielded:
                    raise
                tried += 1
                idx = (idx + 1) % len(pool)
                logger.warning(f"节点池：节点 [{node_name}] 触发 429 限流，保留节点并切换下一个")
                continue
            except VertexError as e:
                last_error = e
                if content_yielded:
                    raise
                if should_remove_pool_node(e):
                    logger.warning(f"节点池：节点 [{node_name}] 网络不可用，移除并切换下一个: {e}")
                    await remove_pool_node(node)
                    pool.pop(idx)
                    if not pool:
                        break
                    continue
                if should_rotate_pool_node(e):
                    tried += 1
                    idx = (idx + 1) % len(pool)
                    logger.warning(f"节点池：节点 [{node_name}] 验证/请求失败，保留节点并切换下一个: {e}")
                    continue
                raise
            except Exception as e:
                last_error = e
                if content_yielded:
                    raise
                if should_remove_pool_node(e):
                    logger.warning(f"节点池：节点 [{node_name}] 网络不可用，移除并切换下一个: {e}")
                    await remove_pool_node(node)
                    pool.pop(idx)
                    if not pool:
                        break
                    continue
                if should_rotate_pool_node(e):
                    tried += 1
                    idx = (idx + 1) % len(pool)
                    logger.warning(f"节点池：节点 [{node_name}] 验证/请求失败，保留节点并切换下一个: {e}")
                    continue
                tried += 1
                idx = (idx + 1) % len(pool)
                logger.warning(f"节点池：节点 [{node_name}] 请求失败，切换下一个: {e}")
                continue

        if last_error:
            raise last_error
        raise InternalError(message="节点池所有节点均不可用")

    async def complete(
        self,
        model: str,
        gemini_payload: dict[str, Any],
        **kwargs: Any,
    ) -> dict[str, Any]:
        raise NotImplementedError("SerialNodePool.complete is not used directly")


# ==================== Parallel node pool ====================


class ParallelNodePool(BaseNodePool):
    """Racing parallel node pool: fixed N slots, failure refill, first-chunk wins.

    This handles both streaming (via _stream_inner_fn) and value-based (via complete).
    """

    def __init__(
        self,
        network: Any,
        stream_inner_fn: Callable[..., AsyncGenerator[dict[str, Any], None]],
    ) -> None:
        self._network = network
        self._stream_inner_fn = stream_inner_fn

    async def stream(
        self,
        model: str,
        gemini_payload: dict[str, Any],
        **kwargs: Any,
    ) -> AsyncGenerator[dict[str, Any], None]:
        cfg = load_config()
        pool: list[dict] = cfg.get("node_pool", [])
        unified_nodes = load_nodes()

        if not parallel_pool_enabled(cfg):
            async for chunk in self._stream_inner_fn(model, gemini_payload=gemini_payload, **kwargs):
                yield chunk
            return

        if parallel_pool_enabled(cfg) and unified_nodes:
            async for chunk in self._stream_realtime_parallel_pool(model, gemini_payload, cfg, [], **kwargs):
                yield chunk
            return

        if len(pool) < 2:
            async for chunk in self._stream_inner_fn(model, gemini_payload=gemini_payload, **kwargs):
                yield chunk
            return

        if parallel_pool_enabled(cfg):
            async for chunk in self._stream_realtime_parallel_pool(model, gemini_payload, cfg, pool, **kwargs):
                yield chunk
            return

        serial = SerialNodePool(self._stream_inner_fn)
        async for chunk in serial.stream(model, gemini_payload, **kwargs):
            yield chunk

    async def complete(
        self,
        model: str,
        gemini_payload: dict[str, Any],
        **kwargs: Any,
    ) -> dict[str, Any]:
        from .transform import ResponseAggregator
        aggregator = ResponseAggregator()
        _raw_image_response = kwargs.pop('_raw_image_response', False)
        return await aggregator.aggregate_stream(
            self.stream(model, gemini_payload=gemini_payload, **kwargs),
            _raw_image_response=_raw_image_response,
        )

    async def run_parallel_value(
        self,
        operation_name: str,
        node_operation: Callable[[Any, str | None], Awaitable[Any]],
        cfg: dict[str, Any],
        fallback_pool: list[dict] | None = None,
        business_session_id: str | None = None,
    ) -> Any:
        """Unified non-streaming parallel pool: parallel node selection, failure refill."""
        fallback_pool = fallback_pool or []
        all_nodes = load_nodes()
        total_nodes = len(all_nodes) if all_nodes else len(fallback_pool)

        if not parallel_pool_enabled(cfg) or total_nodes <= 0:
            session = self._network.create_session()
            try:
                return await node_operation(session, None)
            finally:
                await session.close()

        parallel_size_val = parallel_pool_size(cfg, total_nodes)
        request_id = business_session_id or f"pool-{int(time.time() * 1000) % 1000000}"
        max_rounds = int(cfg.get("parallel_pool_max_rounds", 0) or 0)
        deadline_seconds = float(cfg.get("parallel_pool_deadline_seconds", 0) or 0)
        deadline_at = time.monotonic() + deadline_seconds if deadline_seconds > 0 else 0.0
        logger.info(
            f"业务请求池：启动 operation={operation_name}, session={request_id}, "
            f"并发={parallel_size_val}, 总节点={total_nodes}, 最大轮次={'不限' if max_rounds <= 0 else max_rounds}"
        )

        pending_nodes: list[tuple[int, dict[str, Any]]] = []
        running: dict[asyncio.Task[ParallelValueResult], tuple[int, dict[str, Any]]] = {}
        active_keys: set[str] = set()
        failures: list[ParallelValueResult] = []
        attempt_round = 0
        attempted_count = 0

        def node_key_for(node: dict[str, Any]) -> str:
            return str(node.get("raw_uri") or node.get("name") or id(node))

        def expired() -> bool:
            return bool(deadline_at and time.monotonic() >= deadline_at)

        def refill_candidates() -> None:
            nonlocal attempt_round, pending_nodes
            if pending_nodes or expired() or (max_rounds > 0 and attempt_round >= max_rounds):
                return
            attempt_round += 1
            selected = select_parallel_nodes(cfg, fallback_pool)
            pending_nodes = [(idx, node) for idx, node in selected if node_key_for(node) not in active_keys]
            logger.info(
                f"业务请求池：生成候选 operation={operation_name}, session={request_id}, "
                f"轮次={attempt_round}, 候选={len(pending_nodes)}, 运行中={len(running)}"
            )

        async def run_node(node: dict[str, Any], idx: int) -> ParallelValueResult:
            node_name = str(node.get("name") or node.get("raw_uri", "")[:40] or f"node-{idx+1}")
            raw_uri = str(node.get("raw_uri", "")).strip()
            proxy_url = direct_proxy_url_from_node(node)
            temp_worker: ParallelNodeWorker | None = None
            session: Any | None = None
            started_at = time.perf_counter()
            try:
                if not proxy_url and raw_uri and needs_worker(raw_uri):
                    temp_worker = ParallelNodeWorker(
                        uri=raw_uri, name=node_name, request_id=request_id, node_index=idx,
                    )
                    proxy_url = await temp_worker.start()
                if not proxy_url:
                    raise InternalError(message="节点 URI 不是可用代理地址，也不是支持的订阅节点格式")

                session = self._network.create_session_with_proxy(proxy_url)
                value = await node_operation(session, proxy_url)
                elapsed_ms = (time.perf_counter() - started_at) * 1000
                return ParallelValueResult(node=node, index=idx, name=node_name, value=value, elapsed_ms=elapsed_ms)
            except asyncio.CancelledError:
                raise
            except Exception as e:
                elapsed_ms = (time.perf_counter() - started_at) * 1000
                return ParallelValueResult(node=node, index=idx, name=node_name, error=e, elapsed_ms=elapsed_ms)
            finally:
                if session is not None:
                    with contextlib.suppress(Exception):
                        await session.close()
                if temp_worker is not None:
                    await temp_worker.stop()

        async def start_next(reason: str = "启动") -> None:
            nonlocal attempted_count
            refill_candidates()
            if not pending_nodes:
                return
            idx, node = pending_nodes.pop(0)
            active_keys.add(node_key_for(node))
            attempted_count += 1
            node_name = node.get("name") or node.get("raw_uri", "")[:40]
            logger.info(
                f"业务请求池：{reason}槽位 operation={operation_name}, session={request_id}, "
                f"[{idx+1}] {node_name}, 已尝试={attempted_count}, 运行中={len(running)+1}/{parallel_size_val}"
            )
            task = asyncio.create_task(run_node(node, idx))
            running[task] = (idx, node)

        try:
            for _ in range(parallel_size_val):
                await start_next()

            while running:
                wait_timeout = max(0.0, deadline_at - time.monotonic()) if deadline_at else None
                done, _ = await asyncio.wait(running.keys(), timeout=wait_timeout, return_when=asyncio.FIRST_COMPLETED)
                if not done:
                    raise InternalError(message="业务请求池等待上游成功超时")

                for task in done:
                    _, finished_node = running.pop(task, (None, None))
                    if finished_node is not None:
                        active_keys.discard(node_key_for(finished_node))
                    try:
                        result = await task
                    except asyncio.CancelledError:
                        raise
                    except Exception as e:
                        node = finished_node or {}
                        result = ParallelValueResult(
                            node=node, index=-1,
                            name=str(node.get("name") or node.get("raw_uri") or "unknown"),
                            error=e,
                        )

                    if result.error is None:
                        record_node_success(result.node, result.elapsed_ms)
                        logger.success(
                            f"业务请求池：winner operation={operation_name}, session={request_id}, "
                            f"[{result.index+1}] {result.name}, 耗时={result.elapsed_ms:.0f}ms"
                        )
                        for running_task in running:
                            running_task.cancel()
                        if running:
                            await asyncio.gather(*running.keys(), return_exceptions=True)
                        running.clear()
                        return result.value

                    failures.append(result)
                    err = result.error or InternalError(message="节点未知失败")
                    if not is_retryable_node_failure(err):
                        logger.error(
                            f"业务请求池：检测到不可重试错误，终止 operation={operation_name}, "
                            f"session={request_id}, error={err}"
                        )
                        for running_task in running:
                            running_task.cancel()
                        if running:
                            await asyncio.gather(*running.keys(), return_exceptions=True)
                        running.clear()
                        raise err
                    record_node_failure(result.node, err)
                    logger.warning(
                        f"业务请求池：槽位失败 operation={operation_name}, session={request_id}, "
                        f"[{result.index+1}] {result.name}: {err}"
                    )
                    await start_next("补位")

                while len(running) < parallel_size_val and not expired():
                    before = len(running)
                    await start_next("补位")
                    if len(running) == before:
                        break

            last_error = failures[-1].error if failures and failures[-1].error else None
            if last_error:
                raise last_error
            raise InternalError(message="业务请求池所有节点均不可用")
        finally:
            for task in running:
                task.cancel()
            if running:
                await asyncio.gather(*running.keys(), return_exceptions=True)

    async def _prime_realtime_node(
        self,
        node: dict[str, Any],
        idx: int,
        model: str,
        gemini_payload: dict[str, Any],
        kwargs: dict[str, Any],
        cfg: dict[str, Any],
        request_id: str,
    ) -> ParallelNodeResult:
        node_name = str(node.get("name") or node.get("raw_uri", "")[:40] or f"node-{idx+1}")
        node_kwargs = dict(kwargs)
        node_kwargs["max_retries_override"] = 0
        raw_uri = str(node.get("raw_uri", "")).strip()
        proxy_url = direct_proxy_url_from_node(node)
        temp_worker: ParallelNodeWorker | None = None
        started_at = time.perf_counter()

        try:
            if not proxy_url and raw_uri and needs_worker(raw_uri):
                temp_worker = ParallelNodeWorker(
                    uri=raw_uri, name=node_name, request_id=request_id, node_index=idx,
                )
                proxy_url = await temp_worker.start()

            if not proxy_url:
                raise InternalError(message="节点 URI 不是可用代理地址，也不是支持的订阅节点格式")

            generator = self._stream_inner_fn(
                model,
                gemini_payload=gemini_payload,
                session_override=self._network.create_session_with_proxy(proxy_url),
                session_proxy_override=proxy_url,
                worker_override=temp_worker,
                **node_kwargs,
            )

            first_chunk = await anext(generator)
            elapsed_ms = (time.perf_counter() - started_at) * 1000
            return ParallelNodeResult(node=node, index=idx, name=node_name, first_chunk=first_chunk, generator=generator, elapsed_ms=elapsed_ms)
        except StopAsyncIteration:
            if temp_worker:
                await temp_worker.stop()
            return ParallelNodeResult(node=node, index=idx, name=node_name, error=InternalError(message="节点未返回任何内容"))
        except asyncio.CancelledError:
            if temp_worker:
                await temp_worker.stop()
            raise
        except Exception as e:
            if temp_worker:
                await temp_worker.stop()
            return ParallelNodeResult(node=node, index=idx, name=node_name, error=e)

    async def _stream_realtime_parallel_pool(
        self,
        model: str,
        gemini_payload: dict[str, Any],
        cfg: dict[str, Any],
        pool: list[dict],
        **kwargs: Any,
    ) -> AsyncGenerator[dict[str, Any], None]:
        all_nodes = load_nodes()
        total_nodes = len(all_nodes) if all_nodes else len(pool)
        if total_nodes <= 0:
            raise InternalError(message="统一节点集合为空，请先在管理页面拉取订阅节点")

        parallel_size_val = parallel_pool_size(cfg, total_nodes)
        request_id = f"parallel-{int(time.time() * 1000) % 1000000}"
        max_rounds = int(cfg.get("parallel_pool_max_rounds", 0) or 0)
        deadline_seconds = float(cfg.get("parallel_pool_deadline_seconds", 0) or 0)
        deadline_at = time.monotonic() + deadline_seconds if deadline_seconds > 0 else 0.0
        logger.info(
            f"统一节点集合：启用滚动并行请求池 request={request_id}, 并发={parallel_size_val}, "
            f"总节点={total_nodes}, 最大轮次={'不限' if max_rounds <= 0 else max_rounds}, "
            f"总超时={'不限' if deadline_seconds <= 0 else f'{deadline_seconds:.0f}s'}"
        )

        pending_nodes: list[tuple[int, dict[str, Any]]] = []
        running: dict[asyncio.Task[ParallelNodeResult], tuple[int, dict[str, Any]]] = {}
        failures: list[ParallelNodeResult] = []
        winner: ParallelNodeResult | None = None
        active_keys: set[str] = set()
        attempt_round = 0
        attempted_count = 0

        def node_key_for(node: dict[str, Any]) -> str:
            return str(node.get("raw_uri") or node.get("name") or id(node))

        def expired() -> bool:
            return bool(deadline_at and time.monotonic() >= deadline_at)

        def refill_candidates() -> None:
            nonlocal attempt_round, pending_nodes
            if pending_nodes or expired() or (max_rounds > 0 and attempt_round >= max_rounds):
                return
            attempt_round += 1
            selected = select_parallel_nodes(cfg, pool)
            pending_nodes = [(idx, node) for idx, node in selected if node_key_for(node) not in active_keys]
            logger.info(
                f"统一节点集合：生成补位候选 request={request_id}, 轮次={attempt_round}, "
                f"候选={len(pending_nodes)}, 运行中={len(running)}"
            )

        async def start_next(reason: str = "启动") -> None:
            nonlocal attempted_count
            refill_candidates()
            if not pending_nodes:
                return
            idx, node = pending_nodes.pop(0)
            node_name = node.get("name") or node.get("raw_uri", "")[:40]
            active_keys.add(node_key_for(node))
            attempted_count += 1
            logger.info(
                f"统一节点集合：并行{reason}节点 request={request_id} [{idx+1}] {node_name}, "
                f"轮次={attempt_round}, 已尝试={attempted_count}, 剩余补位={len(pending_nodes)}, 运行中={len(running)+1}/{parallel_size_val}"
            )
            task = asyncio.create_task(self._prime_realtime_node(node, idx, model, gemini_payload, kwargs, cfg, request_id))
            running[task] = (idx, node)

        try:
            for _ in range(parallel_size_val):
                await start_next()

            while running and winner is None:
                wait_timeout = None
                if deadline_at:
                    wait_timeout = max(0.0, deadline_at - time.monotonic())
                done, _ = await asyncio.wait(running.keys(), timeout=wait_timeout, return_when=asyncio.FIRST_COMPLETED)
                if not done:
                    raise InternalError(message="并行请求池等待首包超时")

                for task in done:
                    _, finished_node = running.pop(task, (None, None))
                    if finished_node is not None:
                        active_keys.discard(node_key_for(finished_node))
                    try:
                        result = await task
                    except asyncio.CancelledError:
                        raise
                    except Exception as e:
                        node = finished_node or {}
                        result = ParallelNodeResult(
                            node=node, index=-1,
                            name=str(node.get("name") or node.get("raw_uri") or "unknown"),
                            error=e,
                        )
                    if result.first_chunk is not None and result.generator is not None:
                        winner = result
                        record_node_success(result.node, result.elapsed_ms)
                        logger.success(f"统一节点集合：并行 winner request={request_id} [{result.index+1}] {result.name} 首包={result.elapsed_ms:.0f}ms")
                        break

                    failures.append(result)
                    err = result.error or InternalError(message="节点未知失败")
                    if not is_retryable_node_failure(err):
                        logger.error(f"统一节点集合：检测到不可重试错误，终止 request={request_id}: {err}")
                        for running_task in running:
                            running_task.cancel()
                        if running:
                            await asyncio.gather(*running.keys(), return_exceptions=True)
                        running.clear()
                        raise err
                    record_node_failure(result.node, err)
                    logger.warning(f"统一节点集合：并行节点失败 request={request_id} [{result.index+1}] {result.name}: {err}")
                    await start_next("补位")

                while winner is None and len(running) < parallel_size_val and not expired():
                    before = len(running)
                    await start_next("补位")
                    if len(running) == before:
                        break

            if winner is None:
                last_error = failures[-1].error if failures and failures[-1].error else None
                if last_error:
                    raise last_error
                raise InternalError(message="节点池所有节点均不可用")

            for task in running:
                task.cancel()
            if running:
                await asyncio.gather(*running.keys(), return_exceptions=True)
            running.clear()

            yield winner.first_chunk
            async for chunk in winner.generator:
                yield chunk
        finally:
            for task in running:
                task.cancel()
            if running:
                await asyncio.gather(*running.keys(), return_exceptions=True)
            if winner and winner.generator:
                with contextlib.suppress(Exception):
                    await winner.generator.aclose()

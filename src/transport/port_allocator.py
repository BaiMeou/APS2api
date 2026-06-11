"""Async local port lease allocator for temporary worker processes."""

from __future__ import annotations

import asyncio
import contextlib
import socket
from dataclasses import dataclass
from collections import deque

from src.core.config import load_config
from src.utils.logger import get_logger

logger = get_logger(__name__)


@dataclass(frozen=True)
class PortLease:
    """A leased localhost port."""

    port: int


class PortLeaseAllocator:
    """Process-local async allocator for worker listening ports.

    Request slots submit acquire requests and suspend on their own Future. This
    allocator owns the request queue and wakes exactly the queued request that
    receives a port lease. Release notifications trigger queue processing.
    """

    def __init__(self) -> None:
        self._leased: set[int] = set()
        self._lock = asyncio.Lock()
        self._waiters: deque[tuple[asyncio.Future[PortLease], str]] = deque()
        self._cursor = 0

    def _range_from_config(self) -> tuple[int, int]:
        cfg = load_config()
        base = int(cfg.get("parallel_worker_base_port", 12080) or 12080)
        span = int(cfg.get("parallel_worker_port_span", 2000) or 2000)
        return base, max(1, span)

    @staticmethod
    def _is_os_port_available(port: int) -> bool:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            sock.bind(("127.0.0.1", port))
            return True
        except OSError:
            return False
        finally:
            sock.close()

    def _find_available_port_locked(self) -> PortLease | None:
        base, span = self._range_from_config()
        for offset in range(span):
            idx = (self._cursor + offset) % span
            port = base + idx
            if port in self._leased:
                continue
            if not self._is_os_port_available(port):
                continue
            self._leased.add(port)
            self._cursor = (idx + 1) % span
            logger.debug(f"端口租约：分配 127.0.0.1:{port}, 已租用={len(self._leased)}/{span}")
            return PortLease(port=port)
        return None

    def _find_available_port_from_base_locked(self) -> PortLease | None:
        base, span = self._range_from_config()
        for offset in range(span):
            port = base + offset
            if port in self._leased:
                continue
            if not self._is_os_port_available(port):
                continue
            self._leased.add(port)
            logger.debug(f"端口租约：从 base 分配 127.0.0.1:{port}, 已租用={len(self._leased)}/{span}")
            return PortLease(port=port)
        return None

    def _process_waiters_locked(self) -> None:
        base, span = self._range_from_config()
        while self._waiters:
            waiter, strategy = self._waiters[0]
            if waiter.cancelled():
                self._waiters.popleft()
                continue

            if strategy == "base":
                lease = self._find_available_port_from_base_locked()
            else:
                lease = self._find_available_port_locked()
            if lease is None:
                logger.warning(
                    f"端口租约：端口池暂无可用资源 base={base}, span={span}, "
                    f"排队申请={len(self._waiters)}"
                )
                return

            self._waiters.popleft()
            waiter.set_result(lease)

    async def acquire(self) -> PortLease:
        """Submit a port-use request and wait until allocator grants a lease."""
        return await self._acquire("cursor")

    async def acquire_from_base(self) -> PortLease:
        """Acquire the first available port from the configured base.

        This is intended for single-node workers that stop the previous worker
        before starting the next one. It deliberately does not advance the
        cursor used by parallel worker allocation.
        """
        return await self._acquire("base")

    async def _acquire(self, strategy: str) -> PortLease:
        loop = asyncio.get_running_loop()
        waiter: asyncio.Future[PortLease] = loop.create_future()
        async with self._lock:
            self._waiters.append((waiter, strategy))
            logger.debug(f"端口租约：收到使用申请，排队申请={len(self._waiters)}")
            self._process_waiters_locked()

        try:
            return await waiter
        except BaseException:
            async with self._lock:
                if not waiter.done():
                    waiter.cancel()
                else:
                    with contextlib.suppress(Exception):
                        lease = waiter.result()
                        self._leased.discard(lease.port)
                self._process_waiters_locked()
            raise

    async def release(self, lease: PortLease | int | None) -> None:
        """Release a previously acquired port lease."""
        if lease is None:
            return
        port = lease.port if isinstance(lease, PortLease) else int(lease)
        async with self._lock:
            if port in self._leased:
                self._leased.remove(port)
                logger.debug(f"端口租约：释放 127.0.0.1:{port}, 已租用={len(self._leased)}")
            self._process_waiters_locked()

    def snapshot(self) -> dict[str, int]:
        base, span = self._range_from_config()
        return {"base_port": base, "span": span, "leased": len(self._leased), "waiting": len(self._waiters)}


port_allocator = PortLeaseAllocator()

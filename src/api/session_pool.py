"""统一业务会话与请求池。

业务端点通过本模块创建一次下游会话；会话内部为每个需要转发到
Vertex AI Studio 匿名上游的请求创建滚动并行请求池。管理端点不走本模块。
"""

import time
import uuid
import asyncio
from contextlib import asynccontextmanager
from dataclasses import dataclass
from enum import Enum
from typing import Any, AsyncGenerator

from src.core.config import load_config
from src.utils.logger import get_logger
from .vertex_client import VertexAIClient

logger = get_logger(__name__)


class SessionState(str, Enum):
    WAITING = "waiting"
    ACTIVE = "active"
    CLOSED = "closed"


@dataclass
class _SessionPoolState:
    active_count: int = 0
    waiting_count: int = 0


class BusinessSessionPool:
    """进程内业务会话池状态管理。"""

    def __init__(self) -> None:
        self._condition = asyncio.Condition()
        self._state = _SessionPoolState()

    def _limit_from_config(self) -> int:
        cfg = load_config()
        limit = int(cfg.get("business_session_concurrency_limit", 0) or 0)
        return max(0, limit)

    async def activate(self, session: "GatewaySession") -> None:
        async with self._condition:
            self._state.waiting_count += 1
            session.state = SessionState.WAITING
            logger.debug(
                f"业务会话进入等待: {session.session_id}, "
                f"active={self._state.active_count}, waiting={self._state.waiting_count}"
            )
            try:
                while True:
                    limit = self._limit_from_config()
                    if limit <= 0 or self._state.active_count < limit:
                        self._state.active_count += 1
                        self._state.waiting_count -= 1
                        session.state = SessionState.ACTIVE
                        logger.debug(
                            f"业务会话激活: {session.session_id}, "
                            f"active={self._state.active_count}, waiting={self._state.waiting_count}, limit={'不限' if limit <= 0 else limit}"
                        )
                        return
                    await self._condition.wait()
            except BaseException:
                self._state.waiting_count = max(0, self._state.waiting_count - 1)
                session.state = SessionState.CLOSED
                self._condition.notify(1)
                raise

    async def close(self, session: "GatewaySession") -> None:
        async with self._condition:
            if session.state == SessionState.ACTIVE:
                self._state.active_count = max(0, self._state.active_count - 1)
            elif session.state == SessionState.WAITING:
                self._state.waiting_count = max(0, self._state.waiting_count - 1)
            session.state = SessionState.CLOSED
            self._condition.notify(1)


business_session_pool = BusinessSessionPool()


class GatewaySession:
    """单个下游请求会话。"""

    def __init__(self, vertex_client: VertexAIClient, session_type: str = "business", source: str = "unknown") -> None:
        self.vertex_client = vertex_client
        self.session_type = session_type
        self.source = source
        self.session_id = f"{session_type}-{uuid.uuid4().hex[:12]}"
        self.started_at = time.perf_counter()
        self.state = SessionState.WAITING
        logger.debug(f"创建{self.session_type}会话: {self.session_id}, source={self.source}")

    async def close(self) -> None:
        await business_session_pool.close(self)
        elapsed_ms = (time.perf_counter() - self.started_at) * 1000
        logger.debug(f"清理{self.session_type}会话: {self.session_id}, state={self.state}, elapsed={elapsed_ms:.0f}ms")

    async def stream_gemini(self, model: str, gemini_payload: dict[str, Any], **kwargs: Any) -> AsyncGenerator[dict[str, Any], None]:
        """通过请求池流式返回 Gemini chunk。"""
        async for chunk in self.vertex_client.stream_chat_realtime(
            model=model,
            gemini_payload=gemini_payload,
            business_session_id=self.session_id,
            **kwargs,
        ):
            yield chunk

    async def complete_gemini(self, model: str, gemini_payload: dict[str, Any], **kwargs: Any) -> dict[str, Any]:
        """通过请求池获取完整 Gemini 响应。"""
        return await self.vertex_client.complete_chat(
            model=model,
            gemini_payload=gemini_payload,
            business_session_id=self.session_id,
            **kwargs,
        )

    async def count_tokens(self, model: str, contents: list[dict[str, Any]], **kwargs: Any) -> int:
        """业务会话内执行 countTokens。"""
        return await self.vertex_client.count_tokens(
            model=model,
            contents=contents,
            business_session_id=self.session_id,
            **kwargs,
        )


@asynccontextmanager
async def business_session(vertex_client: VertexAIClient, source: str = "unknown") -> AsyncGenerator[GatewaySession, None]:
    """创建、纳入会话池并自动清理业务会话。"""
    session = GatewaySession(vertex_client, session_type="business", source=source)
    try:
        await business_session_pool.activate(session)
        yield session
    finally:
        await session.close()


@asynccontextmanager
async def management_session(vertex_client: VertexAIClient, source: str = "admin") -> AsyncGenerator[GatewaySession, None]:
    """创建并自动清理管理会话。当前管理端点多数本地处理，保留统一抽象。"""
    session = GatewaySession(vertex_client, session_type="management", source=source)
    try:
        yield session
    finally:
        await session.close()

"""FastAPI路由模块"""

import asyncio
import base64
import json
import mimetypes
import time
import uuid
import random
import string
from typing import Any, AsyncGenerator, cast
import collections.abc
from fastapi import FastAPI, Request, UploadFile
from fastapi.responses import StreamingResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from fastapi.middleware.cors import CORSMiddleware
from starlette.datastructures import UploadFile as StarletteUploadFile
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.types import ASGIApp

from src.core import MODELS_CONFIG_FILE
from src.core.errors import (
    VertexError,
    InvalidArgumentError,
    InternalError,
    RateLimitError,
    AuthenticationError,
)
from src.api.vertex_client import VertexAIClient
from src.api.session_pool import business_session
from src.api.oai_adapter import OAIImageRequestConverter, OAIRequestConverter, OAIResponseConverter
from src.api.transform import _is_safety_block
from src.core.auth import api_key_manager
from src.utils.logger import get_logger, set_request_id


def _inject_anti429(gemini_payload: dict[str, Any]) -> dict[str, Any]:
    """如果配置开启了防429，在 gemini_payload 里插入100位随机数"""
    from src.core.config import load_config
    cfg = load_config()
    if not cfg.get("anti429_enabled", False):
        return gemini_payload

    rand_str = ''.join(random.choices(string.digits, k=100))
    target = cfg.get("anti429_target", "system")

    if target == "system":
        si = gemini_payload.get("systemInstruction", {})
        if isinstance(si, dict):
            parts = si.get("parts", [])
            if not isinstance(parts, list):
                # 保留原始 parts（即使是 tuple/set 等），尝试转换为 list
                try:
                    parts = list(parts)  # type: ignore[call-overload]
                except TypeError:
                    parts = []
        elif isinstance(si, str):
            parts = [{"text": si}]
        else:
            parts = []
        parts.insert(0, {"text": rand_str})
        gemini_payload["systemInstruction"] = {"parts": parts}
    else:
        # 插入到第一条 user 消息的 parts 最前面
        contents = gemini_payload.get("contents", [])
        if isinstance(contents, str):
            gemini_payload["contents"] = [{"role": "user", "parts": [{"text": rand_str}, {"text": contents}]}]
        elif isinstance(contents, list) and contents:
            found = False
            for c in contents:
                if isinstance(c, dict) and c.get("role") == "user":
                    parts = c.get("parts", [])
                    if not isinstance(parts, list):
                        try:
                            parts = list(parts)  # type: ignore[call-overload]
                        except TypeError:
                            parts = []
                    parts.insert(0, {"text": rand_str})
                    c["parts"] = parts
                    found = True
                    break
            if not found:
                # 没有 user 消息，插入一条新的
                contents.insert(0, {"role": "user", "parts": [{"text": rand_str}]})
        else:
            gemini_payload["contents"] = [{"role": "user", "parts": [{"text": rand_str}]}]

    return gemini_payload





# 初始化日志
logger = get_logger(__name__)


def extract_api_key_from_request(request: Request) -> str | None:
    """
    从请求中提取API密钥
    支持三种方式（按优先级）：
    1. Authorization: Bearer <key> (OpenAI 标准 Header)
    2. x-goog-api-key: <key> (Google/Gemini 标准 Header)
    3. ?key=<key> (Google/Gemini 标准 Query Param)
    """
    
    # 1. 尝试 OpenAI 标准 Authorization Header
    auth_header = request.headers.get("Authorization")
    if auth_header and auth_header.lower().startswith("bearer "):
        return auth_header[7:].strip()

    # 2. 尝试 x-goog-api-key Header
    goog_api_key = request.headers.get("x-goog-api-key")
    if goog_api_key:
        return goog_api_key.strip()
        
    # 3. 尝试 URL Query Parameter
    query_key = request.query_params.get("key")
    if query_key:
        return query_key.strip()

    return None


class APIKeyMiddleware(BaseHTTPMiddleware):
    """API密钥认证中间件"""

    def __init__(self, app: ASGIApp, excluded_paths: list[str] | None = None, excluded_prefixes: list[str] | None = None):
        super().__init__(app)
        self.excluded_paths: list[str] = excluded_paths or ["/", "/health"]
        self.excluded_prefixes: list[str] = excluded_prefixes or []

    async def dispatch(self, request: Request, call_next: collections.abc.Callable[[Request], collections.abc.Awaitable[Any]]):
        # 为每个请求设置唯一的请求ID
        set_request_id()
        
        path = request.url.path
        method = request.method
        client_ip = request.client.host if request.client else "unknown"
        
        logger.debug(f"收到请求: {method} {path} from {client_ip}")

        # 检查是否是完全排除的路径
        if self.excluded_paths and path in self.excluded_paths:
            logger.debug(f"路径 {path} 在排除列表中，跳过认证")
            return await call_next(request)

        # 检查前缀排除（管理后台、静态资源）
        if any(path.startswith(p) for p in self.excluded_prefixes):
            return await call_next(request)

        # 获取API密钥
        api_key = extract_api_key_from_request(request)
        if not api_key:
            logger.warning(f"请求 {path} 缺少 API 密钥")
            return JSONResponse(
                status_code=401,
                content={
                    "error": {
                        "code": 401,
                        "message": "Method doesn't allow unregistered callers (callers without established identity). Please use API Key or other form of API consumer identity to call this API.",
                        "status": "UNAUTHENTICATED"
                    }
                }
            )

        # 验证API密钥
        if not api_key_manager.validate_key(api_key):
            logger.warning(f"请求 {path} 使用了无效的 API 密钥: {api_key[:8]}...")
            return JSONResponse(
                status_code=400,
                content={
                    "error": {
                        "code": 400,
                        "message": "API key not valid. Please pass a valid API key.",
                        "status": "INVALID_ARGUMENT"
                    }
                }
            )

        # 将API密钥存储在请求状态中
        request.state.api_key = api_key
        logger.debug(f"API 密钥验证成功: {api_key[:8]}...")

        start_time = time.time()
        response = await call_next(request)
        process_time = time.time() - start_time
        
        logger.info(f"{method} {path} - {response.status_code} ({process_time:.3f}s)")
        
        return response


def create_app(vertex_client: VertexAIClient) -> FastAPI:
    """创建FastAPI应用"""
    logger.info("创建 FastAPI 应用")
    
    app = FastAPI(
        title="Vertex AI Proxy (Anonymous)",
        description="Vertex AI 代理服务，兼容 Gemini API",
        version="1.1.0"
    )

    # 添加中间件（顺序很重要）
    logger.debug("添加中间件")
    app.add_middleware(
        APIKeyMiddleware,
        excluded_paths=["/", "/health", "/admin"],
        excluded_prefixes=["/api/admin/", "/admin/", "/static/"],
    )
    app.add_middleware(
        CORSMiddleware,
        allow_origins=["*"],
        allow_credentials=True,
        allow_methods=["*"],
        allow_headers=["*"],
        expose_headers=["*"],
    )

    # 挂载静态文件
    app.mount("/static", StaticFiles(directory="static"), name="static")

    # 挂载管理后台路由
    from src.api.admin import router as admin_router, session_cleanup_loop
    app.include_router(admin_router)

    # 启动后台 session 清理任务
    asyncio.create_task(session_cleanup_loop())

    # ==================== 全局异常处理 ====================
    
    @app.exception_handler(VertexError)
    async def vertex_exception_handler(request: Request, exc: VertexError):  # type: ignore[misc]
        """处理所有 VertexError 及其子类"""
        logger.error(f"VertexError: {exc.message} (code={exc.code}, status={exc.status})")
        return JSONResponse(
            status_code=exc.code,
            content=exc.to_Dict(),
        )

    @app.exception_handler(Exception)
    async def general_exception_handler(request: Request, exc: Exception):  # type: ignore[misc]
        """处理所有未捕获异常"""
        logger.error(f"Unhandled Exception: {exc}", exc_info=True)
        error = InternalError(message=str(exc))
        return JSONResponse(
            status_code=500,
            content=error.to_Dict(),
        )

    # ==================== 基础端点 ====================

    async def root() -> dict[str, str]:
        """根路径，返回服务信息"""
        logger.debug("处理根路径请求")
        return {
            "message": "Vertex AI Proxy Server (Anonymous Edition)",
            "version": "1.1.0",
            "auth": "API Key Authentication Required",
            "docs": "Only Gemini API Compatible"
        }
    app.get("/")(root)

    async def health_check() -> dict[str, str | int]:
        """健康检查端点"""
        logger.debug("处理健康检查请求")
        api_keys_count = len(api_key_manager.api_keys)
        logger.debug(f"当前加载的 API 密钥数量: {api_keys_count}")
        return {
            "status": "healthy",
            "timestamp": int(time.time()),
            "api_keys_loaded": api_keys_count
        }
    app.get("/health")(health_check)
    
    async def list_models_oai() -> dict[str, str | list[dict[str, Any]]]:
        """返回可用模型列表 (OpenAI 兼容格式)"""
        logger.debug("处理模型列表请求")
        current_time = int(time.time())
        models: list[str] = _load_models_config()
        logger.debug(f"返回 {len(models)} 个可用模型")
        return {
            "object": "list",
            "data": [
                {"id": m, "object": "model", "created": current_time, "owned_by": "google", "permission": []}
                for m in models
            ]
        }

    async def list_models_gemini() -> dict[str, list[dict[str, Any]]]:
        """返回可用模型列表 (Gemini 兼容格式)"""
        logger.debug("处理 Gemini 模型列表请求")
        models: list[str] = _load_models_config()
        return {
            "models": [
                {
                    "name": f"models/{m}",
                    "version": m,
                    "displayName": m,
                    "description": "Vertex AI Studio anonymous model",
                    "inputTokenLimit": 1048576,
                    "outputTokenLimit": 65536,
                    "supportedGenerationMethods": _supported_generation_methods(m),
                }
                for m in models
            ]
        }

    app.get("/v1/models")(list_models_oai)
    app.get("/v1beta/models")(list_models_gemini)

    async def get_model_info(model: str, request: Request) -> JSONResponse:
        """按名称返回模型详细信息 (Gemini 兼容格式)"""
        logger.debug(f"处理模型信息请求: {model}")
        models: list[str] = _load_models_config()
        # 支持 models/xxx 或裸 xxx 两种形式
        model_name = model
        if model_name.startswith("models/"):
            model_name = model_name[7:]
        if model_name not in models:
            return JSONResponse(
                status_code=404,
                content={
                    "error": {
                        "code": 404,
                        "message": f"Model '{model}' not found.",
                        "status": "NOT_FOUND"
                    }
                }
            )
        return JSONResponse(content={
            "name": f"models/{model_name}",
            "version": model_name,
            "displayName": model_name,
            "description": "Vertex AI Studio anonymous model",
            "inputTokenLimit": 1048576,
            "outputTokenLimit": 65536,
            "supportedGenerationMethods": _supported_generation_methods(model_name),
        })

    app.get("/v1beta/models/{model}", response_model=None)(get_model_info)
    app.get("/v1/models/{model}", response_model=None)(get_model_info)

    # ==================== 内部 Gemini 处理函数 ====================

    async def _internal_stream_gemini(
        model: str,
        gemini_payload: dict[str, Any],
        source: str,
        **kwargs: Any,
    ) -> AsyncGenerator[dict[str, Any], None]:
        async with business_session(vertex_client, source=source) as session:
            async for chunk in session.stream_gemini(model=model, gemini_payload=gemini_payload, **kwargs):
                yield chunk

    async def _internal_complete_gemini(
        model: str,
        gemini_payload: dict[str, Any],
        source: str,
        **kwargs: Any,
    ) -> dict[str, Any]:
        async with business_session(vertex_client, source=source) as session:
            return await session.complete_gemini(model=model, gemini_payload=gemini_payload, **kwargs)

    # ==================== Gemini 兼容端点 ====================

    async def stream_generate_content(model: str, request: Request) -> StreamingResponse | JSONResponse:
        """Gemini 格式的流式生成接口"""
        logger.info(f"收到流式生成请求: 模型={model}")
        
        try:
            body_any = await request.json()
        except json.JSONDecodeError as e:
            raise InvalidArgumentError(f"Invalid JSON in request body: {e}")

        if not isinstance(body_any, dict):
                raise InvalidArgumentError("Request body must be a JSON object")
        body: dict[str, Any] = cast(dict[str, Any], body_any)
        
        logger.debug(f"请求体大小: {len(str(body))} 字符")
        logger.debug_json("下游请求体", body)

        from src.core.config import load_config as _load_cfg
        force_no_stream_enabled = _load_cfg().get("force_no_stream", False)

        if force_no_stream_enabled:
            logger.info("force_no_stream 已开启，Gemini 假流式返回")
            async def fake_stream_generator():
                try:
                    response = await _internal_complete_gemini(
                        model=model,
                        gemini_payload=_inject_anti429(body),
                        source="gemini.streamGenerateContent.fake",
                        skip_probe=("image" in model.lower()),
                    )
                    yield "data: " + json.dumps(response, ensure_ascii=False, separators=(',', ':')) + "\n\n"
                except VertexError as e:
                    yield e.to_sse()
                except Exception as e:
                    logger.error(f"假流式生成未知错误: {e}")
                    error = InternalError(message=str(e))
                    yield error.to_sse()

            return StreamingResponse(
                fake_stream_generator(),
                media_type="text/event-stream",
                headers={
                    "Cache-Control": "no-cache",
                    "Connection": "keep-alive",
                    "X-Accel-Buffering": "no",
                },
            )

        async def stream_generator():
            chunk_count = 0
            try:
                async for chunk in _internal_stream_gemini(
                    model=model, gemini_payload=_inject_anti429(body),
                    source="gemini.streamGenerateContent",
                    skip_probe=("image" in model.lower()),
                ):
                        chunk_count += 1
                        yield "data: " + json.dumps(chunk, ensure_ascii=False, separators=(',', ':')) + "\n\n"
            except VertexError as e:
                msg = (e.message or "").lower()
                status = (e.status or "").lower()
                if any(k in msg or k in status for k in ("safety", "content_filter", "block_reason")):
                    logger.warning(f"流式安全拦截，返回标准格式: {e.message}")
                    safety_chunk = {
                        "candidates": [{
                            "content": {"parts": [], "role": "model"},
                            "finishReason": "SAFETY",
                            "safetyRatings": [],
                            "index": 0,
                        }],
                        "promptFeedback": {
                            "blockReason": e.status or "SAFETY",
                            "safetyRatings": [],
                            "blockReasonMessage": e.message,
                        },
                    }
                    yield "data: " + json.dumps(safety_chunk, ensure_ascii=False, separators=(',', ':')) + "\n\n"
                else:
                    logger.error(f"流式生成 Vertex 错误: {e.message}")
                    yield e.to_sse()
            except Exception as e:
                logger.error(f"流式生成未知错误: {e}")
                error = InternalError(message=str(e))
                yield error.to_sse()
            finally:
                logger.debug(f"流式生成完成，共发送 {chunk_count} 个数据块")

        return StreamingResponse(
            stream_generator(),
            media_type="text/event-stream",
            headers={
                "Cache-Control": "no-cache",
                "Connection": "keep-alive",
                "X-Accel-Buffering": "no",
            },
        )
    app.post("/v1beta/models/{model}:streamGenerateContent", response_model=None)(stream_generate_content)
    app.post("/v1/models/{model}:streamGenerateContent", response_model=None)(stream_generate_content)

    async def generate_content(model: str, request: Request) -> JSONResponse | dict[str, Any]:
        """Gemini 格式的非流式生成接口"""
        logger.info(f"收到非流式生成请求: 模型={model}")
        
        try:
            body_any = await request.json()
        except json.JSONDecodeError as e:
            raise InvalidArgumentError(f"Invalid JSON in request body: {e}")

        if not isinstance(body_any, dict):
                raise InvalidArgumentError("Request body must be a JSON object")
        body: dict[str, Any] = cast(dict[str, Any], body_any)
        
        logger.debug(f"请求体大小: {len(str(body))} 字符")
        logger.debug_json("下游请求体", body)

        start_time = time.time()
        
        try:
            response = await _internal_complete_gemini(
                model=model,
                gemini_payload=_inject_anti429(body),
                source="gemini.generateContent",
                skip_probe=("image" in model.lower()),
            )
        except VertexError as e:
            msg = (e.message or "").lower()
            status = (e.status or "").lower()
            if any(k in msg or k in status for k in ("safety", "content_filter", "block_reason")):
                logger.warning(f"上游安全拦截，返回标准格式: {e.message}")
                return JSONResponse(
                    status_code=200,
                    content={
                        "candidates": [],
                        "promptFeedback": {
                            "blockReason": e.status or "SAFETY",
                            "safetyRatings": [],
                            "blockReasonMessage": e.message,
                        },
                    }
                )
            logger.error(f"非流式生成 Vertex 错误: {e.message}")
            return JSONResponse(
                status_code=e.code,
                content=json.loads(e.to_json())
            )
        except Exception as e:
            logger.error(f"非流式生成未知错误: {e}")
            error = InternalError(message=str(e))
            return JSONResponse(
                status_code=500,
                content=json.loads(error.to_json())
            )
        
        process_time = time.time() - start_time
        logger.success(f"非流式生成完成: 模型={model}, 耗时={process_time:.3f}s")
        
        return response
    app.post("/v1beta/models/{model}:generateContent", response_model=None)(generate_content)
    app.post("/v1/models/{model}:generateContent", response_model=None)(generate_content)

    # ==================== OpenAI 兼容端点 ====================

    async def oai_chat_completions(request: Request) -> StreamingResponse | JSONResponse:
        """OpenAI 格式的 Chat Completion 接口"""
        try:
            body_any = await request.json()
        except json.JSONDecodeError as e:
            return JSONResponse(status_code=400, content={"error": {"message": f"Invalid JSON: {e}", "type": "invalid_request_error", "code": None}})

        if not isinstance(body_any, dict):
            return JSONResponse(status_code=400, content={"error": {"message": "Request body must be a JSON object", "type": "invalid_request_error", "code": None}})

        body: dict[str, Any] = cast(dict[str, Any], body_any)
        stream = body.get("stream", False)

        from src.core.config import load_config as _load_cfg
        force_no_stream_enabled = _load_cfg().get("force_no_stream", False)

        if stream and force_no_stream_enabled:
            logger.info("force_no_stream 已开启，OpenAI 假流式输出")

        try:
            model, gemini_payload = OAIRequestConverter.convert(body)
        except (KeyError, ValueError) as e:
            return JSONResponse(status_code=400, content={"error": {"message": str(e), "type": "invalid_request_error", "code": None}})

        gemini_payload = _inject_anti429(gemini_payload)

        logger.info(f"收到 OAI 请求: 模型={model}, stream={stream}")

        if stream:
            request_id = uuid.uuid4().hex[:24]

            async def oai_stream_generator():
                is_first = True
                has_finish = False
                has_tool_calls = False
                try:
                    if force_no_stream_enabled:
                        # 假流式：一次性获取完整 Gemini 响应
                        gemini_response = await _internal_complete_gemini(model, gemini_payload, "openai.chat.stream.fake")
                        oai_response = OAIResponseConverter.gemini_json_to_oai_json(gemini_response, model)
                        created_time = oai_response.get("created", int(time.time()))
                        msg = oai_response.get("choices", [{}])[0].get("message", {})
                        content = msg.get("content")
                        reasoning_content = msg.get("reasoning_content")
                        tool_calls = msg.get("tool_calls")
                        finish_reason = oai_response.get("choices", [{}])[0].get("finish_reason", "stop")
                        usage = oai_response.get("usage")
                        
                        base = {
                            "id": oai_response.get("id", f"chatcmpl-{request_id}"),
                            "object": "chat.completion.chunk",
                            "created": created_time,
                            "model": model
                        }
                        
                        yield f"data: {json.dumps({**base, 'choices': [{'index': 0, 'delta': {'role': 'assistant'}, 'finish_reason': None}]}, ensure_ascii=False)}\n\n"
                        if reasoning_content:
                            yield f"data: {json.dumps({**base, 'choices': [{'index': 0, 'delta': {'reasoning_content': reasoning_content}, 'finish_reason': None}]}, ensure_ascii=False)}\n\n"
                        if content:
                            yield f"data: {json.dumps({**base, 'choices': [{'index': 0, 'delta': {'content': content}, 'finish_reason': None}]}, ensure_ascii=False)}\n\n"
                        if tool_calls:
                            yield f"data: {json.dumps({**base, 'choices': [{'index': 0, 'delta': {'tool_calls': tool_calls}, 'finish_reason': None}]}, ensure_ascii=False)}\n\n"
                        
                        finish_evt = {
                            **base,
                            "choices": [{"index": 0, "delta": {}, "finish_reason": finish_reason}]
                        }
                        if usage:
                            finish_evt["usage"] = usage
                        yield f"data: {json.dumps(finish_evt, ensure_ascii=False)}\n\n"
                    else:
                        async for gemini_chunk in _internal_stream_gemini(model, gemini_payload, "openai.chat.stream"):
                            events = OAIResponseConverter.convert_realtime_chunk(
                                gemini_chunk,
                                model,
                                request_id,
                                is_first=is_first,
                                has_prior_tool_calls=has_tool_calls,
                            )
                            is_first = False
                            for event in events:
                                if '"tool_calls"' in event:
                                    has_tool_calls = True
                                if '"finish_reason"' in event and '"finish_reason": null' not in event:
                                    has_finish = True
                                yield event
                    if not has_finish:
                        base = {"id": f"chatcmpl-{request_id}", "object": "chat.completion.chunk", "created": int(time.time()), "model": model}
                        yield f"data: {json.dumps({**base, 'choices': [{'index': 0, 'delta': {}, 'finish_reason': 'stop'}]}, ensure_ascii=False)}\n\n"
                    yield "data: [DONE]\n\n"
                except VertexError as e:
                    if _is_safety_block(e):
                        base = {"id": f"chatcmpl-{request_id}", "object": "chat.completion.chunk", "created": int(time.time()), "model": model}
                        yield f"data: {json.dumps({**base, 'choices': [{'index': 0, 'delta': {}, 'finish_reason': 'content_filter'}]}, ensure_ascii=False)}\n\n"
                    else:
                        err = _vertex_error_to_oai(e)
                        yield f"data: {json.dumps(err, ensure_ascii=False)}\n\n"
                    yield "data: [DONE]\n\n"
                except Exception as e:
                    logger.error(f"OAI 流式错误: {e}")
                    err = {"error": {"message": str(e), "type": "server_error", "code": None}}
                    yield f"data: {json.dumps(err, ensure_ascii=False)}\n\n"
                    yield "data: [DONE]\n\n"

            return StreamingResponse(
                oai_stream_generator(),
                media_type="text/event-stream",
                headers={
                    "Cache-Control": "no-cache",
                    "Connection": "keep-alive",
                    "X-Accel-Buffering": "no",
                },
            )
        else:
            try:
                gemini_response = await _internal_complete_gemini(model, gemini_payload, "openai.chat")
                oai_response = OAIResponseConverter.gemini_json_to_oai_json(gemini_response, model)
                return JSONResponse(content=oai_response)
            except VertexError as e:
                if _is_safety_block(e):
                    request_id = uuid.uuid4().hex[:24]
                    safety_response = {
                        "id": f"chatcmpl-{request_id}",
                        "object": "chat.completion",
                        "created": int(time.time()),
                        "model": model,
                        "choices": [{
                            "index": 0,
                            "message": {"role": "assistant", "content": None},
                            "finish_reason": "content_filter"
                        }],
                        "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
                    }
                    return JSONResponse(status_code=200, content=safety_response)
                err = _vertex_error_to_oai(e)
                return JSONResponse(status_code=e.code, content=err)
            except Exception as e:
                logger.error(f"OAI 非流式错误: {e}")
                return JSONResponse(status_code=500, content={"error": {"message": str(e), "type": "server_error", "code": None}})

    app.post("/v1/chat/completions", response_model=None)(oai_chat_completions)

    # ==================== OpenAI 图片兼容端点 ====================

    async def oai_images_generations(request: Request) -> JSONResponse:
        """OpenAI Images: text-to-image"""
        try:
            body = await _read_json_or_form_fields(request)
            model, gemini_payload, n, response_format = OAIImageRequestConverter.convert_generation(body)
        except ValueError as e:
            return _oai_bad_request(str(e))
        except json.JSONDecodeError as e:
            return _oai_bad_request(f"Invalid JSON: {e}")
        except Exception as e:
            return _oai_bad_request(str(e))

        logger.info(f"收到 OAI 图片生成请求: 模型={model}, n={n}")
        return await _run_oai_image_request(vertex_client, model, gemini_payload, n, response_format, source="openai.images.generations")

    async def oai_images_edits(request: Request) -> JSONResponse:
        """OpenAI Images: image edit / image-to-image"""
        try:
            form = await request.form()
            fields = _form_to_plain_dict(form)
            image_uploads = _form_uploads(form, "image")
            if not image_uploads:
                return _oai_bad_request("image is required")
            images = [await _upload_to_inline_image(file) for file in image_uploads]
            mask_uploads = _form_uploads(form, "mask")
            mask = await _upload_to_inline_image(mask_uploads[0]) if mask_uploads else None

            model = OAIImageRequestConverter.resolve_model(fields.get("model"))
            prompt = str(fields.get("prompt") or "Edit the provided image.")
            if fields.get("negative_prompt"):
                prompt = f"{prompt.strip()}\nAvoid: {fields.get('negative_prompt')}"
            n = _coerce_oai_n(fields.get("n"))
            response_format = str(fields.get("response_format") or "b64_json")
            gemini_payload = OAIImageRequestConverter.build_payload(
                model=model,
                prompt=prompt,
                images=images,
                mask=mask,
                size=fields.get("size"),
                quality=fields.get("quality"),
                style=fields.get("style"),
                background=fields.get("background"),
                output_format=fields.get("output_format"),
                mode="edit",
            )
        except Exception as e:
            return _oai_bad_request(str(e))

        logger.info(f"收到 OAI 图片编辑请求: 模型={model}, n={n}, images={len(images)}")
        return await _run_oai_image_request(vertex_client, model, gemini_payload, n, response_format, source="openai.images.edits")

    async def oai_images_variations(request: Request) -> JSONResponse:
        """OpenAI Images: image variation"""
        try:
            form = await request.form()
            fields = _form_to_plain_dict(form)
            image_uploads = _form_uploads(form, "image")
            if not image_uploads:
                return _oai_bad_request("image is required")
            images = [await _upload_to_inline_image(file) for file in image_uploads]

            model = OAIImageRequestConverter.resolve_model(fields.get("model"))
            prompt = str(fields.get("prompt") or "Create a variation of the provided image.")
            if fields.get("negative_prompt"):
                prompt = f"{prompt.strip()}\nAvoid: {fields.get('negative_prompt')}"
            n = _coerce_oai_n(fields.get("n"))
            response_format = str(fields.get("response_format") or "b64_json")
            gemini_payload = OAIImageRequestConverter.build_payload(
                model=model,
                prompt=prompt,
                images=images,
                size=fields.get("size"),
                quality=fields.get("quality"),
                style=fields.get("style"),
                output_format=fields.get("output_format"),
                mode="variation",
            )
        except Exception as e:
            return _oai_bad_request(str(e))

        logger.info(f"收到 OAI 图片变体请求: 模型={model}, n={n}")
        return await _run_oai_image_request(vertex_client, model, gemini_payload, n, response_format, source="openai.images.variations")

    app.post("/v1/images/generations", response_model=None)(oai_images_generations)
    app.post("/v1/images/edits", response_model=None)(oai_images_edits)
    app.post("/v1/images/variations", response_model=None)(oai_images_variations)

    # ==================== Gemini 辅助端点 ====================

    async def count_tokens(model: str, request: Request) -> JSONResponse:
        """Gemini countTokens 兼容端点"""
        try:
            body_any = await request.json()
        except json.JSONDecodeError as e:
            raise InvalidArgumentError(f"Invalid JSON in request body: {e}")
        if not isinstance(body_any, dict):
            raise InvalidArgumentError("Request body must be a JSON object")

        body = cast(dict[str, Any], body_any)

        # 优先读取 generateContentRequest 中的字段，否则直接用顶层 body
        supported_count_token_fields = set(vertex_client.transformer._supported_variable_fields()) - {"contents"}
        request_obj = body.get("generateContentRequest")
        if isinstance(request_obj, dict):
            contents = request_obj.get("contents", [])
            extra_fields = {
                k: v
                for k, v in request_obj.items()
                if k in supported_count_token_fields
            }
        else:
            contents = body.get("contents", [])
            extra_fields = {
                k: v
                for k, v in body.items()
                if k in supported_count_token_fields
            }

        normalized = vertex_client.transformer._normalize_gemini_payload({"contents": contents})

        async with business_session(vertex_client, source="gemini.countTokens") as session:
            total_tokens = await session.count_tokens(
                model=model,
                contents=cast(list[dict[str, Any]], normalized.get("contents", [])),
                extra_fields=extra_fields,
            )
        return JSONResponse(content={"totalTokens": total_tokens})

    app.post("/v1beta/models/{model}:countTokens", response_model=None)(count_tokens)
    app.post("/v1/models/{model}:countTokens", response_model=None)(count_tokens)

    logger.info("FastAPI 应用创建完成")
    return app


# ==================== 辅助函数 ====================

def _load_models_config() -> list[str]:
    """加载模型配置"""
    try:
        with open(MODELS_CONFIG_FILE, 'r', encoding='utf-8') as f:
            config = json.load(f)
            return cast(list[str], config.get('models', []))
    except Exception:
        return ["gemini-1.5-pro", "gemini-1.5-flash", "gemini-2.0-flash-exp", "gemini-2.0-pro-exp-02-05", "gemini-2.5-flash"]


def _supported_generation_methods(model: str) -> list[str]:
    return ["generateContent", "streamGenerateContent", "countTokens"]


def _vertex_error_to_oai(e: VertexError) -> dict[str, Any]:
    """将 VertexError 转为 OAI 错误格式"""
    if isinstance(e, InvalidArgumentError):
        err_type = "invalid_request_error"
    elif isinstance(e, RateLimitError):
        err_type = "rate_limit_error"
    elif isinstance(e, AuthenticationError):
        err_type = "authentication_error"
    else:
        err_type = "server_error"
    return {"error": {"message": e.message, "type": err_type, "code": None}}


async def _read_json_or_form_fields(request: Request) -> dict[str, Any]:
    content_type = request.headers.get("content-type", "").lower()
    if "multipart/form-data" in content_type or "application/x-www-form-urlencoded" in content_type:
        form = await request.form()
        return _form_to_plain_dict(form)
    body_any = await request.json()
    if not isinstance(body_any, dict):
        raise ValueError("Request body must be a JSON object")
    return cast(dict[str, Any], body_any)


def _form_to_plain_dict(form: Any) -> dict[str, Any]:
    data: dict[str, Any] = {}
    for key, value in form.multi_items():
        if _is_upload_file(value):
            continue
        if key in data:
            existing = data[key]
            if isinstance(existing, list):
                existing.append(value)
            else:
                data[key] = [existing, value]
        else:
            data[key] = value
    return data


def _form_uploads(form: Any, key: str) -> list[UploadFile]:
    uploads: list[UploadFile] = []
    for item_key, value in form.multi_items():
        if item_key != key and item_key != f"{key}[]" and not item_key.startswith(f"{key}["):
            continue
        if _is_upload_file(value):
            uploads.append(cast(UploadFile, value))
    return uploads


def _is_upload_file(value: Any) -> bool:
    return isinstance(value, (UploadFile, StarletteUploadFile))


async def _upload_to_inline_image(file: UploadFile) -> dict[str, str]:
    data = await file.read()
    if not data:
        raise ValueError(f"{file.filename or 'image'} is empty")
    mime_type = file.content_type or mimetypes.guess_type(file.filename or "")[0] or "image/png"
    return {
        "mimeType": mime_type,
        "data": base64.b64encode(data).decode("ascii"),
    }


def _coerce_oai_n(value: Any) -> int:
    try:
        n = int(value)
    except (TypeError, ValueError):
        n = 1
    return max(1, min(n, 8))


async def _run_oai_image_request(
    vertex_client: VertexAIClient,
    model: str,
    gemini_payload: dict[str, Any],
    n: int,
    response_format: str,
    source: str = "openai.images",
) -> JSONResponse:
    image_items: list[dict[str, Any]] = []
    normalized_response_format = "url" if response_format == "url" else "b64_json"

    try:
        async with business_session(vertex_client, source=source) as session:
            tasks = [session.complete_gemini(model=model, gemini_payload=gemini_payload, skip_probe=True) for _ in range(n)]
            gemini_responses = await asyncio.gather(*tasks)
            for gemini_response in gemini_responses:
                image_items.extend(
                    OAIResponseConverter.gemini_json_to_oai_image_data(
                        gemini_response,
                        response_format=normalized_response_format
                    )
                )
                if len(image_items) >= n:
                    break
    except VertexError as e:
        return JSONResponse(status_code=e.code, content=_vertex_error_to_oai(e))
    except Exception as e:
        logger.error(f"OAI 图片请求错误: {e}")
        return JSONResponse(status_code=500, content={"error": {"message": str(e), "type": "server_error", "code": None}})

    if not image_items:
        return JSONResponse(
            status_code=502,
            content={"error": {"message": "Upstream response did not contain image data", "type": "server_error", "code": None}},
        )

    return JSONResponse(content={
        "created": int(time.time()),
        "data": image_items[:n],
    })


def _oai_bad_request(message: str) -> JSONResponse:
    return JSONResponse(
        status_code=400,
        content={"error": {"message": message, "type": "invalid_request_error", "code": None}},
    )

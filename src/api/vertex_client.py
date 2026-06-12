"""Vertex AI客户端"""

import asyncio
import glob
import json
import os
import tempfile
import time
from typing import Any, Awaitable, Callable, cast, AsyncGenerator

from src.core.config import load_config

from src.core.errors import (
    VertexError,
    AuthenticationError,
    RateLimitError,
    InternalError,
    parse_error_response,
    raise_for_status,
)
from src.utils.logger import get_logger
from src.stream.parser import clean_streaming_chunk

from .model_config import ModelConfigBuilder
from .transform import RequestTransformer
from .network import NetworkClient
from .node_pool import ParallelNodePool

logger = get_logger(__name__)


class VertexAIClient:
    """Vertex AI API客户端 (Anonymous 模式)"""
    
    def __init__(self):
        logger.info("初始化 Vertex AI 客户端")

        self._cleanup_old_temp_files()

        self.config = load_config()

        self.model_builder = ModelConfigBuilder()
        self.transformer = RequestTransformer(self.model_builder)
        self.network = NetworkClient()

        self.vertex_ai_anonymous_base_api = "https://cloudconsole-pa.clients6.google.com"

        self._node_pool = ParallelNodePool(self.network, self._stream_realtime_inner)

        logger.success("Vertex AI 客户端初始化完成")

    @staticmethod
    def _cleanup_old_temp_files() -> None:
        now = time.time()
        pattern = os.path.join(tempfile.gettempdir(), "parallel-worker-*")
        for f in glob.glob(pattern):
            try:
                if os.path.getmtime(f) < now:
                    os.remove(f)
            except Exception:
                pass

    async def close(self):
        """关闭客户端并释放资源"""
        await self.network.close()

    async def complete_chat(self, model: str, gemini_payload: dict[str, Any], **kwargs: Any) -> dict[str, Any]:
        _raw_image_response = kwargs.pop('_raw_image_response', False)
        return await self._node_pool.complete(
            model=model, gemini_payload=gemini_payload,
            _raw_image_response=_raw_image_response, **kwargs,
        )

    async def _run_with_parallel_request_pool(
        self,
        operation_name: str,
        node_operation: Callable[[Any, str | None], Awaitable[Any]],
        cfg: dict[str, Any],
        fallback_pool: list[dict] | None = None,
        business_session_id: str | None = None,
    ) -> Any:
        return await self._node_pool.run_parallel_value(
            operation_name, node_operation, cfg,
            fallback_pool=fallback_pool,
            business_session_id=business_session_id,
        )

    async def stream_chat_realtime(self, model: str, gemini_payload: dict[str, Any], **kwargs: Any) -> AsyncGenerator[dict[str, Any], None]:
        """真流式聊天，逐 chunk yield Gemini 格式的增量 dict，支持节点池自动轮换"""
        logger.info(f"开始真流式聊天请求: 模型={model}")
        async for chunk in self._node_pool.stream(model, gemini_payload=gemini_payload, **kwargs):
            yield chunk



    def _build_request_payload(self, model: str, gemini_payload: dict[str, Any], recaptcha_token: str, kwargs: dict[str, Any]) -> dict[str, Any]:
        """构建上游请求体（共用逻辑）"""
        new_variables = self.transformer.build_vertex_payload(
            model=model, gemini_payload=gemini_payload, kwargs=kwargs
        )
        new_variables["region"] = "global"
        new_variables["recaptchaToken"] = recaptcha_token
        return {
            "requestContext": self._build_request_context(),
            "querySignature": self.config.get("stream_query_signature", "2/l8eCsMMY49imcDQ/lwwXyL8cYtTjxZBF2dNqy69LodY="),
            "operationName": "StreamGenerateContentAnonymous",
            "variables": new_variables,
        }

    def _build_request_context(self) -> dict[str, Any]:
        """构建 AI Studio 浏览器端常见的 GraphQL requestContext。"""
        return {
            "clientVersion": "boq_cloud-boq-clientweb-vertexaistudio_20260402.09_p0",
            "pagePath": "/vertex-ai/studio/multimodal",
            "jurisdiction": "global",
            "localizationData": {
                "locale": "zh_CN",
                "timezone": "Asia/Shanghai",
            },
        }

    def _build_browser_headers(self) -> dict[str, str]:
        """构建更贴近 console.cloud.google.com 浏览器请求的头。"""
        return {
            "accept": "*/*",
            "accept-language": "zh-CN,zh;q=0.9,en;q=0.8",
            "content-type": "application/json",
            "origin": "https://console.cloud.google.com",
            "referer": "https://console.cloud.google.com/vertex-ai/studio/multimodal",
            "x-goog-authuser": "0",
        }

    async def _execute_streaming_attempt(
        self, session: Any, model: str, gemini_payload: dict[str, Any],
        recaptcha_token: str, kwargs: dict[str, Any], is_first_auth_attempt: bool = False
    ) -> AsyncGenerator[dict[str, Any], None]:
        """真流式：解析上游响应，yield 增量 Gemini dict"""
        new_body = self._build_request_payload(model, gemini_payload, recaptcha_token, kwargs)
        headers = self._build_browser_headers()
        api_key = self.config.get("vertex_api_key", "AIzaSyCI-zsRP85UVOi0DjtiCwWBwQ1djDy741g")
        url = f"{self.vertex_ai_anonymous_base_api}/v3/entityServices/AiplatformEntityService/schemas/AIPLATFORM_GRAPHQL:batchGraphql?key={api_key}&prettyPrint=false"

        async for response in self.network.stream_request(session, 'POST', url, headers=headers, json_data=new_body):
            if response.status_code != 200:
                error_bytes = await response.aread()
                error_text_str = error_bytes.decode('utf-8') if isinstance(error_bytes, bytes) else str(error_bytes)
                await response.aclose()
                if response.status_code in [401, 403] or "Failed to verify action" in error_text_str or "The caller does not have permission" in error_text_str:
                    raise AuthenticationError(message=f"Authentication/Recaptcha failed: {error_text_str}", upstream_response=error_text_str)
                parsed_error = parse_error_response(error_text_str)
                if parsed_error:
                    raise parsed_error
                raise raise_for_status(code=response.status_code, message=f"Upstream Error: {error_text_str}", upstream_response=error_text_str)

            buffer = ""
            scan_pos = 0
            start_idx = 0
            brace_count = 0
            in_string = False
            escape = False
            async for chunk in response.aiter_content():
                if not chunk: continue
                text_chunk = chunk.decode('utf-8') if isinstance(chunk, bytes) else chunk
                buffer += text_chunk

                while True:
                    if scan_pos == 0:
                        start_idx = buffer.find('{')
                        if start_idx == -1:
                            buffer = ""
                            break
                        scan_pos = start_idx
                        brace_count = 0
                        in_string = False
                        escape = False

                    end_idx = -1
                    for i in range(scan_pos, len(buffer)):
                        char = buffer[i]
                        if escape:
                            escape = False
                            continue
                        if char == '\\':
                            escape = True
                            continue
                        if char == '"':
                            in_string = not in_string
                            continue

                        if not in_string:
                            if char == '{':
                                brace_count += 1
                            elif char == '}':
                                brace_count -= 1
                                if brace_count == 0:
                                    end_idx = i
                                    break

                    if end_idx != -1:
                        json_str = buffer[start_idx:end_idx + 1]
                        buffer = buffer[end_idx + 1:]
                        scan_pos = 0

                        try:
                            obj = json.loads(json_str)
                            async for chunk_data in self._process_streaming_object(obj):
                                yield chunk_data
                        except json.JSONDecodeError:
                            pass
                    else:
                        scan_pos = len(buffer)
                        break

    async def _process_streaming_object(self, obj: dict[str, Any]) -> AsyncGenerator[dict[str, Any], None]:
        """从单个上游 JSON 对象中提取增量 chunk"""
        results = obj.get("results", [])
        logger.debug(f"完整上游响应: {obj}")
        logger.debug(f"_process_streaming_object: results 数量={len(results)}")
        for result in results:
            # 错误检测
            errors = result.get("errors")
            if errors and isinstance(errors, list) and len(errors) > 0:
                err_msg = errors[0].get("message", "") if isinstance(errors[0], dict) else str(errors[0])
                # "Failed to verify action" 是匿名接口首次必败的预期错误
                if "Failed to verify action" in err_msg or "The caller does not have permission" in err_msg:
                    raise AuthenticationError(message=err_msg, upstream_response=err_msg)
                logger.error(f"Vertex API 完整错误: {errors}")
                parsed = parse_error_response({"errors": errors})
                if parsed:
                    raise parsed

            data = result.get("data")
            if not isinstance(data, dict):
                logger.debug(f"result.data 不是 dict: type={type(data)}")
                continue

            # 展开 ui.streamGenerateContentAnonymous 包装
            ui = data.get("ui", {})
            if isinstance(ui, dict) and "streamGenerateContentAnonymous" in ui:
                inner = ui["streamGenerateContentAnonymous"]
                logger.debug(f"展开 ui 包装: inner type={type(inner)}, len={len(inner) if isinstance(inner, list) else 'N/A'}")
                if isinstance(inner, dict):
                    data = inner
                elif isinstance(inner, list):
                    # 从外层 data 提取公共元数据，补到每个 item 中
                    outer_meta: dict[str, Any] = {}
                    for meta_key in ('usageMetadata', 'modelVersion', 'responseId', 'promptFeedback'):
                        if data.get(meta_key):
                            outer_meta[meta_key] = data[meta_key]
                    for item in inner:
                        if isinstance(item, dict):
                            for key, val in outer_meta.items():
                                if key not in item:
                                    item[key] = val
                            item = clean_streaming_chunk(item)
                            logger.debug(f"yield list item: keys={list(item.keys())}")
                            yield item
                    continue
                else:
                    continue

            candidates = data.get("candidates")
            chunk: dict[str, Any] = {}
            # 当 candidates key 存在时（即使是空列表 []），也保留它以传递 finishReason 等元数据
            if "candidates" in data and candidates is not None:
                chunk["candidates"] = candidates
            if data.get("usageMetadata"):
                chunk["usageMetadata"] = data["usageMetadata"]
            if data.get("modelVersion"):
                chunk["modelVersion"] = data["modelVersion"]
            if data.get("responseId"):
                chunk["responseId"] = data["responseId"]
            if data.get("promptFeedback"):
                chunk["promptFeedback"] = data["promptFeedback"]
            if chunk:
                chunk = clean_streaming_chunk(chunk)
                yield chunk

    async def _execute_count_tokens_attempt(
        self,
        session: Any,
        model: str,
        contents: list[dict[str, Any]],
        recaptcha_token: str,
        extra_fields: dict[str, Any] | None = None,
    ) -> int:
        """执行一次 CountTokens 上游请求。"""
        target_model = self.model_builder.parse_model_name(model)
        if target_model.startswith("models/"):
            target_model = target_model[7:]

        variables: dict[str, Any] = {
            "contents": contents,
            "endpoint": "",
            "model": target_model,
            "region": "global",
            "recaptchaToken": recaptcha_token,
        }
        # 透传 labels、generationConfig、safetySettings 等可选字段
        if extra_fields:
            for key, value in extra_fields.items():
                if key not in variables and value is not None:
                    variables[key] = value

        payload = {
            "requestContext": self._build_request_context(),
            "querySignature": self.config.get("count_tokens_query_signature", "2/mENOSldfC+HZM+tGhVuJLrl8M6gEyK3HRjUKuA5AM58="),
            "operationName": "CountTokens",
            "variables": variables,
        }
        headers = self._build_browser_headers()
        api_key = self.config.get("vertex_api_key", "AIzaSyCI-zsRP85UVOi0DjtiCwWBwQ1djDy741g")
        url = f"{self.vertex_ai_anonymous_base_api}/v3/entityServices/AiplatformEntityService/schemas/AIPLATFORM_GRAPHQL:batchGraphql?key={api_key}&prettyPrint=false"

        response = await self.network.post_request(session, url, headers, payload)
        if response.status_code != 200:
            text = response.text if hasattr(response, "text") else ""
            if response.status_code in [401, 403] or "Failed to verify action" in text or "The caller does not have permission" in text:
                raise AuthenticationError(message=f"Authentication/Recaptcha failed: {text}", upstream_response=text)
            parsed_error = parse_error_response(text)
            if parsed_error:
                raise parsed_error
            raise raise_for_status(code=response.status_code, message=f"Upstream Error: {text}", upstream_response=text)

        data = response.json()
        items = data if isinstance(data, list) else [data]
        for entry in items:
            if not isinstance(entry, dict):
                continue
            parsed_error = parse_error_response(entry)
            if parsed_error:
                if "Failed to verify action" in parsed_error.message or "The caller does not have permission" in parsed_error.message:
                    raise AuthenticationError(message=parsed_error.message, upstream_response=str(entry))
                raise parsed_error
            for result in entry.get("results", []) or []:
                if not isinstance(result, dict):
                    continue
                parsed_result_error = parse_error_response(result)
                if parsed_result_error:
                    raise parsed_result_error
                data_obj = result.get("data", {})
                if not isinstance(data_obj, dict):
                    continue
                ui_data = data_obj.get("ui", {}) if isinstance(data_obj.get("ui"), dict) else {}
                count_data = ui_data.get("countTokensV2") or data_obj.get("countTokensV2") or data_obj.get("countTokens")
                if isinstance(count_data, dict) and "totalTokens" in count_data:
                    return int(count_data["totalTokens"])
        raise InternalError(message="CountTokens response did not contain totalTokens")

    async def _count_tokens_inner(self, session: Any, model: str, contents: list[dict[str, Any]], extra_fields: dict[str, Any] | None = None) -> int:
        max_retries = int(load_config().get("max_retries", 10))
        recaptcha_token = None
        is_first_auth_attempt = True
        attempt = 0

        while attempt <= max_retries:
            if not recaptcha_token:
                recaptcha_token = await self.network.fetch_recaptcha_token(session)
                is_first_auth_attempt = True
            if not recaptcha_token:
                if attempt == max_retries:
                    raise AuthenticationError("Could not fetch recaptcha token.")
                attempt += 1
                await asyncio.sleep(0)
                continue
            try:
                return await self._execute_count_tokens_attempt(session, model, contents, recaptcha_token, extra_fields=extra_fields)
            except AuthenticationError:
                if is_first_auth_attempt:
                    is_first_auth_attempt = False
                    await asyncio.sleep(0)
                    continue
                recaptcha_token = None
                if attempt >= max_retries:
                    raise
                attempt += 1
                await asyncio.sleep(0)
            except RateLimitError:
                if attempt >= max_retries:
                    raise
                recaptcha_token = None
                attempt += 1
                await asyncio.sleep(0)
            except VertexError as e:
                if not e.is_retryable or attempt >= max_retries:
                    raise
                attempt += 1
                await asyncio.sleep(0)

        raise InternalError(message="CountTokens retry exhausted")

    async def count_tokens(self, model: str, contents: list[dict[str, Any]], **kwargs: Any) -> int:
        """通过统一业务请求池执行 CountTokens。"""
        cfg = load_config()
        fallback_pool: list[dict] = cfg.get("node_pool", [])
        business_session_id = str(kwargs.get("business_session_id") or "") or None
        extra_fields = kwargs.get("extra_fields")

        async def operation(session: Any, proxy_url: str | None) -> int:
            return await self._count_tokens_inner(session, model, contents, extra_fields=extra_fields)

        return cast(int, await self._run_with_parallel_request_pool(
            "CountTokens",
            operation,
            cfg,
            fallback_pool=fallback_pool,
            business_session_id=business_session_id,
        ))

    async def _stream_realtime_inner(self, model: str, gemini_payload: dict[str, Any], **kwargs: Any) -> AsyncGenerator[dict[str, Any], None]:
        """真流式内部方法（含重试逻辑）"""
        max_retries = int(kwargs.pop("max_retries_override", int(load_config().get("max_retries", 10))))
        session_override = kwargs.pop("session_override", None)
        session_proxy_override = kwargs.pop("session_proxy_override", None)
        worker_override = kwargs.pop("worker_override", None)
        skip_probe = kwargs.pop("skip_probe", False)
        content_yielded = False
        recaptcha_token = None
        is_first_auth_attempt = True
        attempt = 0

        session = session_override or self.network.create_session()
        try:
            while attempt <= max_retries:
                if not recaptcha_token:
                    recaptcha_token = await self.network.fetch_recaptcha_token(session)
                    is_first_auth_attempt = True

                if not recaptcha_token:
                    if attempt == max_retries:
                        raise AuthenticationError("Could not fetch recaptcha token.")
                    attempt += 1
                    await asyncio.sleep(0)
                    continue

                try:
                    chunk_count = 0
                    
                    if skip_probe:
                        current_model = model
                        current_payload = gemini_payload
                    else:
                        probe_model = "gemini-2.5-flash"
                        probe_payload = {"contents": [{"parts": [{"text": "Hello"}]}]}
                        current_model = probe_model if is_first_auth_attempt else model
                        current_payload = probe_payload if is_first_auth_attempt else gemini_payload
                    
                    async for chunk in self._execute_streaming_attempt(
                        session, current_model, current_payload, recaptcha_token, kwargs,
                        is_first_auth_attempt=is_first_auth_attempt
                    ):
                        yield chunk
                        content_yielded = True
                        chunk_count += 1

                    if chunk_count == 0 and is_first_auth_attempt:
                        logger.debug("真流式首次请求返回空数据，触发认证重试")
                        is_first_auth_attempt = False
                        await asyncio.sleep(0)
                        continue
                    break

                except AuthenticationError:
                    if is_first_auth_attempt:
                        is_first_auth_attempt = False
                        await asyncio.sleep(0)
                        continue
                    recaptcha_token = None
                    if content_yielded or attempt >= max_retries:
                        raise
                    attempt += 1
                    await asyncio.sleep(0)

                except RateLimitError as e:
                    if content_yielded or attempt >= max_retries:
                        raise
                    logger.info("429 限流，销毁当前 session 并重建以切换出口 IP")
                    await session.close()
                    if session_override is not None:
                        session = self.network.create_session_with_proxy(session_proxy_override)
                    else:
                        session = self.network.create_session()
                    recaptcha_token = None
                    attempt += 1
                    await asyncio.sleep(0)

                except VertexError as e:
                    if not e.is_retryable or content_yielded or attempt >= max_retries:
                        raise
                    attempt += 1
                    await asyncio.sleep(0)

                except Exception as e:
                    if content_yielded or attempt >= max_retries:
                        raise InternalError(message=f"Internal error: {e}") from e
                    attempt += 1
                    await asyncio.sleep(0)
        finally:
            await session.close()
            if worker_override is not None:
                await worker_override.stop()

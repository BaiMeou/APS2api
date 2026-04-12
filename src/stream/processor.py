

import json
import time
from typing import Any, cast
import collections.abc

from src.core.errors import (
    VertexError,
    EmptyResponseError,
    InternalError,
    NotFoundError,
    InvalidArgumentError,
    RateLimitError, 
)
from src.utils.logger import get_logger
from src.utils.token_counter import calculate_usage_metadata
from .parser import parse_upstream_data



logger = get_logger(__name__)


class StreamProcessor:
    """
    流式响应处理器。
    
    职责:
    1. 收集和聚合上游分块返回的流式数据。
    2. 使用 parser 解析并转换聚合后的数据结构。
    3. 将转换后的结果重新包装成 Gemini 标准的 SSE (Server-Sent Events) 格式返回给客户端。
    4. 在没有上游 Token 使用统计时，调用本地计数器估算 Token。
    """
    
    def __init__(self):
        logger.debug("初始化流处理器")
        
        
        self._actual_content_sent = False
        self._request_context: dict[str, Any] = {}
    
    def has_actual_content_sent(self) -> bool:
        """检查是否已发送实际文本内容"""
        return self._actual_content_sent
    
    def set_request_context(self, downstream_payload: dict[str, Any], upstream_payload: dict[str, Any]):
        """设置请求上下文"""
        logger.debug("设置流处理器请求上下文")
        self._request_context = {
            'downstream_payload': downstream_payload,
            'upstream_payload': upstream_payload
        }
    
    def _create_gemini_chunk(
        self,
        parts: list[dict[str, Any]],
        finish_reason: str | None,
        safety_ratings: list[dict[str, Any]],
        citation_metadata: dict[str, Any],
        grounding_metadata: dict[str, Any],
        candidate_index: int,
        prompt_feedback: dict[str, Any],
        usage_metadata: dict[str, Any],
        finish_message: str | None = None,
        token_count: int | None = None,
        avg_logprobs: float | None = None,
        logprobs_result: dict[str, Any] | None = None,
        create_time: str | None = None,
        model_version: str | None = None,
        response_id: str | None = None,
        model_status: dict[str, Any] | None = None,
    ) -> str:
        """根据聚合后的内容，创建一个包含完整上下文的Gemini格式SSE事件。"""
        candidate: dict[str, Any] = {
            "index": candidate_index
        }
        if finish_reason and isinstance(finish_reason, str):
            candidate["finishReason"] = finish_reason.upper()
        if finish_message:
            candidate["finishMessage"] = finish_message
        
        if parts:
            candidate["content"] = {
                "parts": parts,
                "role": "model"
            }
        
        
        if safety_ratings:
            candidate["safetyRatings"] = safety_ratings
        if citation_metadata:
            candidate["citationMetadata"] = citation_metadata
        if grounding_metadata:
            candidate["groundingMetadata"] = grounding_metadata
        if token_count is not None:
            candidate["tokenCount"] = token_count
        if avg_logprobs is not None:
            candidate["avgLogprobs"] = avg_logprobs
        if logprobs_result:
            candidate["logprobsResult"] = logprobs_result
            
        
        
        chunk: dict[str, Any] = {"candidates": [candidate]}
        
        
        if prompt_feedback:
            chunk["promptFeedback"] = prompt_feedback
        if usage_metadata:
            chunk["usageMetadata"] = usage_metadata
        if create_time:
            chunk["createTime"] = create_time
        if model_version:
            chunk["modelVersion"] = model_version
        if response_id:
            chunk["responseId"] = response_id
        if model_status:
            chunk["modelStatus"] = model_status
             
        return "data: " + json.dumps(chunk, ensure_ascii=False, separators=(',', ':')) + "\n\n"

    async def process_stream(
        self,
        response_iterator: collections.abc.AsyncIterator[str],
        model: str = "vertex-ai-proxy"
    ) -> collections.abc.AsyncGenerator[str, None]:
        """
        处理和包装来自上游的流式响应数据。
        由于后端接口有时返回完整 JSON 而非标准 SSE，该处理器实现了“聚合再分发”的模式。
        
        处理步骤：
        1. 数据收集：从响应迭代器中读出所有分块（raw_chunks）。
        2. 结果解析：调用 parse_upstream_data 解析聚合后的原始文本。
           - 处理错误情况：识别权限错误、限流错误或空响应。
           - 捕获异常快照：如果发生 API 错误，将现场保存到错误目录。
        3. Token 补全：如果上游未返回使用详情，利用 TokenCounter 手动计算 prompt 和 candidates 的 token 数。
        4. SSE 组装：将解析出的内容及元数据（安全评级、引用信息等）包装成标准的 data: {...}\n\n 格式。
        5. 产生结果：yield 最终的 JSON 字符串。
        """
        
        
        start_time = time.time()
        raw_chunks: list[str] = []
        
        try:
            chunk_count = 0
            logger.debug("开始收集上游数据块")
            async for chunk in response_iterator:
                chunk_count += 1
                raw_chunks.append(chunk)
            
            logger.debug(f"收集完成，共 {chunk_count} 个数据块")
            raw_data = '\n'.join(raw_chunks)
            
            
            try:
                parsed_data = json.loads(raw_data)
                logger.debug_json("完整原始上游响应", parsed_data)
            except json.JSONDecodeError:
                logger.debug_large("完整原始上游响应", raw_data)
            
            if not raw_data:
                logger.error("上游返回空数据")
                
                from src.utils.error_logger import save_error_snapshot
                save_error_snapshot(
                    downstream_payload=self._request_context.get('downstream_payload', {}),
                    upstream_payload=self._request_context.get('upstream_payload', {}),
                    upstream_response="[EMPTY RESPONSE]",
                    error_type="empty_response"
                )
                raise EmptyResponseError("Upstream returned no data")

            
            result = parse_upstream_data(raw_data)
            
            
            if result["has_error"] and not result["parts"]:
                error_msg = result["error_message"]
                error_obj = result.get("error_obj")
                
                
                is_auth_error = "Failed to verify action" in error_msg or "The caller does not have permission" in error_msg
                is_rate_limit = isinstance(error_obj, RateLimitError) or "resource has been exhausted" in error_msg.lower() or "quota" in error_msg.lower()
                
                if not is_auth_error and not is_rate_limit:
                    logger.error(f"API 错误且无内容: {error_msg}")
                    
                    
                    error_type = "api_error"
                    if error_obj:
                        
                        error_type = f"upstream_{error_obj.code}_{type(error_obj).__name__}"
                    
                    
                    from src.utils.error_logger import save_error_snapshot
                    save_error_snapshot(
                        downstream_payload=self._request_context.get('downstream_payload', {}),
                        upstream_payload=self._request_context.get('upstream_payload', {}),
                        upstream_response=raw_data,
                        error_type=error_type
                    )
                
                
                if error_obj:
                    raise error_obj

                
                error_msg_lower = error_msg.lower()
                if "not found" in error_msg_lower:
                    raise NotFoundError(message=error_msg)
                elif is_rate_limit:
                    raise RateLimitError(message=error_msg)
                elif is_auth_error:
                    from src.core.errors import AuthenticationError
                    raise AuthenticationError(
                        message=f"Authentication/Recaptcha failed: {error_msg}",
                        details={"upstream_response": error_msg},
                        upstream_response=error_msg
                    )
                else:
                    raise InvalidArgumentError(message=error_msg)
            
            finish_reason = result.get("finish_reason") or "STOP"
            
            if not result["parts"] and not result["has_error"]:
                if finish_reason != "STOP":
                    logger.warning(f"上游非 STOP 原因停止且无内容: {finish_reason}")
                    from src.utils.error_logger import save_error_snapshot
                    save_error_snapshot(
                        downstream_payload=self._request_context.get('downstream_payload', {}),
                        upstream_payload=self._request_context.get('upstream_payload', {}),
                        upstream_response=raw_data,
                        error_type=f"finish_{finish_reason.lower()}"
                    )
                elif not result.get("prompt_feedback"):
                    logger.error("上游返回空响应 (无 parts 且 finish_reason=STOP)")
                    
                    from src.utils.error_logger import save_error_snapshot
                    save_error_snapshot(
                        downstream_payload=self._request_context.get('downstream_payload', {}),
                        upstream_payload=self._request_context.get('upstream_payload', {}),
                        upstream_response=raw_data,
                        error_type="stop_no_content"
                    )
                    
                    raise EmptyResponseError("Upstream returned empty response (STOP with no content/metadata)")
            
            
            usage_metadata = result.get("usage_metadata", {})
            if not usage_metadata and self._request_context:
                try:
                    downstream_payload = self._request_context.get('downstream_payload', {})
                    
                    
                    prompt_contents: list[dict[str, Any]] = []
                    if 'gemini_payload' in downstream_payload:
                        gemini_payload = downstream_payload['gemini_payload']
                        if isinstance(gemini_payload, dict) and 'contents' in gemini_payload:
                            prompt_contents = cast(list[dict[str, Any]], gemini_payload['contents'])
                    
                    
                    usage_metadata = await calculate_usage_metadata(
                        prompt_contents=prompt_contents,
                        response_parts=result["parts"],
                        request_context=self._request_context
                    )
                except Exception as e:
                    logger.warning(f"计算 usage metadata 失败: {e}")
                    usage_metadata = {}
            
            final_chunk = self._create_gemini_chunk(
                parts=result["parts"],
                finish_reason=result.get("finish_reason"),
                safety_ratings=result.get("safety_ratings", []),
                citation_metadata=result.get("citation_metadata", {}),
                grounding_metadata=result.get("grounding_metadata", {}),
                candidate_index=result.get("candidate_index", 0),
                prompt_feedback=result.get("prompt_feedback", {}),
                usage_metadata=usage_metadata,
                finish_message=result.get("finish_message"),
                token_count=result.get("token_count"),
                avg_logprobs=result.get("avg_logprobs"),
                logprobs_result=result.get("logprobs_result"),
                create_time=result.get("create_time"),
                model_version=result.get("model_version"),
                response_id=result.get("response_id"),
                model_status=result.get("model_status")
            )
            
            process_time = time.time() - start_time
            logger.success(f"流式响应处理完成: 耗时={process_time:.3f}s, 完成原因={result.get('finish_reason', 'UNKNOWN')}")
            
            yield final_chunk
            self._actual_content_sent = True
            
        except VertexError as e:
            if "Failed to verify action" in e.message or "The caller does not have permission" in e.message:
                from src.core.errors import AuthenticationError
                if not isinstance(e, AuthenticationError):
                    raise AuthenticationError(
                        message=f"Stream contained Authentication error: {e.message}",
                        details={"upstream_response": e.upstream_response or e.message},
                        upstream_response=e.upstream_response or e.message
                    )
            else:
                logger.error(f"流处理 Vertex 错误: {e.message}")
            raise
        except Exception as e:
            logger.error(f"流处理未知错误: {e}")
            
            from src.utils.error_logger import save_error_snapshot
            save_error_snapshot(
                downstream_payload=self._request_context.get('downstream_payload', {}),
                upstream_payload=self._request_context.get('upstream_payload', {}),
                upstream_response=str(e),
                error_type="internal_exception"
            )
            raise InternalError(message=f"Unknown stream processing error: {e}")


def get_stream_processor() -> StreamProcessor:
    """创建流处理器实例"""
    return StreamProcessor()

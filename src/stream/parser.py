"""
Vertex AI 响应解析工具函数

负责解析上游 API 的响应数据，处理错误和元数据提取。
"""

import json
from typing import Any, cast
from src.core.errors import (
    VertexError,
    InternalError,
    parse_error_response,
)
from src.api.part_cleaner import clean_part, merge_content_blocks
from src.utils.logger import get_logger

logger = get_logger(__name__)

def extract_path_index(result: dict[str, Any]) -> int:
    """从 result 对象中提取 path 索引"""
    path = result.get('path', [])
    if not path or not isinstance(path, list):
        return -1
    try:
        # 索引通常是路径的最后一个整数元素
        path_list = cast(list[Any], path)
        for elem in reversed(path_list):
            if isinstance(elem, int):
                return elem
            if isinstance(elem, str) and elem.isdigit():
                return int(elem)
    except (IndexError, ValueError, TypeError):
        pass
    return -1

def clean_json_string(raw_data: str) -> str:
    """清理并规范化 JSON 字符串"""
    cleaned_data = raw_data.strip()
    if cleaned_data.endswith(','):
        cleaned_data = cleaned_data[:-1]
    
    if not cleaned_data.startswith('['):
        cleaned_data = f'[{cleaned_data}]'
    elif not cleaned_data.endswith(']'):
         # 这是一个不完整的数组，尝试补全
         if '}]' not in cleaned_data:
            cleaned_data += ']'
    return cleaned_data

def process_candidate_metadata(candidate_data: dict[str, Any]) -> dict[str, Any]:
    """提取 candidate 级别的元数据（仅提取实际存在的字段）"""
    metadata: dict[str, Any] = {}
    
    finish_reason = candidate_data.get('finishReason')
    if finish_reason:
        metadata['finish_reason'] = finish_reason
        
    if 'finishMessage' in candidate_data:
        metadata['finish_message'] = candidate_data['finishMessage']
    
    # 提取实际存在的元数据字段
    if candidate_data.get('safetyRatings'):
        metadata['safety_ratings'] = candidate_data['safetyRatings']
        
    if candidate_data.get('citationMetadata'):
        metadata['citation_metadata'] = candidate_data['citationMetadata']
        
    if candidate_data.get('groundingMetadata'):
        metadata['grounding_metadata'] = candidate_data['groundingMetadata']
        
    if 'tokenCount' in candidate_data:
        metadata['token_count'] = candidate_data['tokenCount']
        
    if 'avgLogprobs' in candidate_data:
        metadata['avg_logprobs'] = candidate_data['avgLogprobs']
        
    if 'logprobsResult' in candidate_data:
        metadata['logprobs_result'] = candidate_data['logprobsResult']
    
    # index 字段通常存在
    if candidate_data.get('index') is not None:
        metadata['candidate_index'] = candidate_data['index']
        
    return metadata

def _extract_error_message(item: dict[str, Any]) -> str | None:
    """从单个响应项中提取错误信息（如果有）"""
    
    # 1. 检查顶层 error 对象 (标准 Google Cloud 错误)
    error_obj = item.get('error')
    if error_obj:
        if isinstance(error_obj, dict):
            # 显式转换为 Dict 以满足类型检查
            safe_error_obj = cast(dict[str, Any], error_obj)
            return str(safe_error_obj.get('message', str(safe_error_obj)))
        return str(error_obj)

    # 2. 检查顶层 errors 列表 (GraphQL 风格或批处理错误)
    errors = item.get('errors')
    if errors and isinstance(errors, list):
        # 显式转换
        safe_errors = cast(list[Any], errors)
        if safe_errors:
            first_error = safe_errors[0]
            if isinstance(first_error, dict):
                safe_first_error = cast(dict[str, Any], first_error)
                return str(safe_first_error.get('message', str(safe_first_error)))
            return str(first_error)
            
    return None



def clean_streaming_chunk(chunk: dict[str, Any]) -> dict[str, Any]:
    import copy
    chunk = copy.deepcopy(chunk)
    if "candidates" not in chunk:
        return chunk

    for candidate in chunk.get("candidates", []):
        if not isinstance(candidate, dict):
            continue
        content = candidate.get("content")
        if not isinstance(content, dict):
            continue
        content["role"] = "model"
        parts = content.get("parts")
        if not isinstance(parts, list):
            continue

        cleaned_parts: list[dict[str, Any]] = []
        for part in parts:
            if not isinstance(part, dict):
                cleaned_parts.append(part)
                continue

            cleaned = clean_part(part)
            if cleaned:
                cleaned_parts.append(cleaned)

        content["parts"] = cleaned_parts

    return chunk

def parse_upstream_data(raw_data: str) -> dict[str, Any]:
    """
    解析完整的上游原始数据。
    
    Returns:
        包含 parts, finish_reason 和实际存在的元数据的字典
    """
    state: dict[str, Any] = {
        "finish_reason": None,
        "finish_message": None,
        "safety_ratings": [],
        "citation_metadata": {},
        "grounding_metadata": {},
        "token_count": None,
        "avg_logprobs": None,
        "logprobs_result": None,
        "candidate_index": 0,
        "prompt_feedback": {},
        "usage_metadata": {},
        "create_time": None,
        "model_version": None,
        "response_id": None,
        "model_status": None,
        "has_error": False,
        "error_message": "",
        "error_obj": None,
        "parts_by_path": {},
        "unindexed_parts": []
    }

    try:
        cleaned_data = clean_json_string(raw_data)
        data_list = json.loads(cleaned_data)
        
        if not isinstance(data_list, list):
            data_list = [data_list]
        
        # 显式转换为 List[Any]
        safe_data_list = cast(list[Any], data_list)

        for item in safe_data_list:
            if not isinstance(item, dict):
                continue
            
            item_dict = cast(dict[str, Any], item)
            
            # 1. 优先通过统一解析逻辑检查 item 中的错误 (处理 errors 数组)
            parsed_error = parse_error_response(item_dict)
            if parsed_error:
                # 如果是 "Failed to verify action"，我们忽略它，因为这可能是匿名的第一个预期错误
                if "Failed to verify action" in parsed_error.message:
                    logger.debug(f"忽略预期的认证错误: {parsed_error.message}")
                else:
                    state["has_error"] = True
                    state["error_message"] = parsed_error.message
                    state["error_obj"] = parsed_error
                # 继续解析以尝试提取更多上下文
            
            # 2. 检查顶层错误 (作为兜底)
            error_msg = _extract_error_message(item_dict)
            if error_msg and not state["has_error"]:
                state["has_error"] = True
                state["error_message"] = error_msg
            
            # 3. 处理 results 列表
            results = item_dict.get('results', [])
            if not isinstance(results, list):
                continue
            
            typed_results: list[dict[str, Any]] = []
            safe_results = cast(list[Any], results)
            for r in safe_results:
                if isinstance(r, dict):
                    typed_results.append(cast(dict[str, Any], r))

            # 3. 检查 results 中的错误 (使用统一解析逻辑)
            parsed_error = parse_error_response(typed_results)
            if parsed_error:
                state["has_error"] = True
                state["error_message"] = parsed_error.message
                state["error_obj"] = parsed_error
            
            # 4. 提取数据 parts
            for result in typed_results:
                # 如果有 data=null 且有 errors，这已经被上面的 parsed_error 捕获
                # 我们跳过这个 result 的 data 处理，避免 NoneType 错误
                if result.get('data') is None and 'errors' in result:
                    continue

                path_index = extract_path_index(result)
                data = result.get('data')
                
                if isinstance(data, dict):
                    _update_state_from_data(state, cast(dict[str, Any], data), path_index)

    except json.JSONDecodeError as e:
        state["has_error"] = True
        state["error_message"] = f"JSON parse error: {e}"
    except VertexError:
        raise
    except Exception as e:
        # 捕获其他未预期的解析错误
        logger.error(f"解析过程发生未知错误: {e}")
        state["has_error"] = True
        state["error_message"] = f"Parse error: {str(e)}"
    
    # 组装 parts - 先按原有逻辑收集所有parts
    parts_by_path = cast(dict[int, Any], state['parts_by_path'])
    ordered_parts: list[dict[str, Any]] = [parts_by_path[k] for k in sorted(parts_by_path.keys())]
    unindexed_parts = cast(list[Any], state['unindexed_parts'])
    ordered_parts.extend(unindexed_parts)
    
    final_parts = merge_content_blocks(ordered_parts)
    
    result: dict[str, Any] = {
        "parts": final_parts
    }
    # 将 state 中除了临时存储结构外的所有字段合并到 result
    excluded_keys = ['parts_by_path', 'unindexed_parts']
    result.update({k: v for k, v in state.items() if k not in excluded_keys})
    return result

def _update_state_from_data(state: dict[str, Any], data: dict[str, Any], path_index: int):
    """从数据对象更新解析状态（仅提取实际存在的字段）"""
    # 只提取实际存在的顶层元数据
    if data.get('promptFeedback'):
        state['prompt_feedback'] = data['promptFeedback']
    if 'usageMetadata' in data:
        state['usage_metadata'] = data['usageMetadata']
    if 'createTime' in data:
        state['create_time'] = data['createTime']
    if 'modelVersion' in data:
        state['model_version'] = data['modelVersion']
    if 'responseId' in data:
        state['response_id'] = data['responseId']
    if 'modelStatus' in data:
        state['model_status'] = data['modelStatus']
            
    # 处理 candidates
    candidates = data.get('candidates', [])
    for candidate in candidates:
        # 提取 candidate 元数据 (包括 finish_reason)
        meta = process_candidate_metadata(candidate)
        for k, v in meta.items():
            if v is not None and v != [] and v != {}:
                state[k] = v

        # 提取 content parts
        content = candidate.get('content', {})
        parts = content.get('parts', [])
        for part in parts:
            if path_index != -1:
                state['parts_by_path'][path_index] = part
            else:
                state['unindexed_parts'].append(part)

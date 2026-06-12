"""请求和响应转换工具模块

负责：
1. 将 Gemini 格式的 Payload 转换为 Vertex AI 内部格式
2. 将流式响应聚合成完整的非流式响应
"""

import json
import time
from typing import Any, cast

from src.core.errors import (
    VertexError,
    InternalError,
    parse_error_response,
)
from src.api.model_config import ModelConfigBuilder
from src.api.part_cleaner import (
    _FcNameTracker,
    clean_part,
    encode_thought_signature,
    handle_base64_in_contents,
)
from src.core.config import load_config
from src.utils.logger import get_logger
from src.utils.string_utils import snake_to_camel, camel_to_snake

logger = get_logger(__name__)


class RequestTransformer:
    """请求参数转换器"""
    
    def __init__(self, model_builder: ModelConfigBuilder):
        self.model_builder = model_builder

    def build_vertex_payload(
        self,
        model: str,
        gemini_payload: dict[str, Any],
        kwargs: dict[str, Any]
    ) -> dict[str, Any]:
        new_variables: dict[str, Any] = {}

        target_model = self.model_builder.parse_model_name(model)
        new_variables['model'] = target_model

        supported_fields = self._supported_variable_fields()

        try:
            from src.core.types import GeminiPayload
            gemini_payload_obj = GeminiPayload.model_validate(gemini_payload)
            dumped_payload = gemini_payload_obj.model_dump(by_alias=True, exclude_none=True)
            for field in supported_fields:
                if field in dumped_payload:
                    new_variables[field] = dumped_payload[field]
        except Exception as e:
            logger.debug(f"Pydantic validation failed in native, using basic: {e}")
            for field in supported_fields:
                if field in gemini_payload:
                    new_variables[field] = gemini_payload[field]
                else:
                    snake_field = camel_to_snake(field)
                    if snake_field in gemini_payload:
                        new_variables[field] = gemini_payload[snake_field]

        if 'contents' in new_variables:
            converted_contents = self._normalize_contents(new_variables['contents'])
            converted_contents = self._handle_inline_data_case(converted_contents)
            converted_contents = self._normalize_contents(converted_contents)
            converted_contents = handle_base64_in_contents(converted_contents)
            converted_contents = self._filter_empty_contents(converted_contents)
            converted_contents = encode_thought_signature(converted_contents)
            new_variables['contents'] = converted_contents

        if 'tools' in new_variables:
            normalized_tools = self._normalize_tools_format(new_variables['tools'])
            if normalized_tools:
                new_variables['tools'] = normalized_tools
            else:
                del new_variables['tools']
                if 'toolConfig' in new_variables:
                    del new_variables['toolConfig']

        if 'toolConfig' in new_variables:
            new_variables['toolConfig'] = self._convert_tools_format(new_variables['toolConfig'])

        gen_config = self.model_builder.build_generation_config(
            gen_config={},
            gemini_payload=gemini_payload,
            **kwargs
        )
        if gen_config:
            new_variables['generationConfig'] = gen_config

        cfg = load_config()
        if cfg.get("drop_max_tokens", True) and 'generationConfig' in new_variables:
            new_variables['generationConfig'].pop('maxOutputTokens', None)
            if not new_variables['generationConfig']:
                del new_variables['generationConfig']

        if 'safetySettings' not in new_variables and 'safety_settings' not in gemini_payload:
            new_variables['safetySettings'] = self.model_builder.build_safety_settings()

        if new_variables.get('contents'):
            logger.info(f"[Vertex Payload Contents] (model={model}): {json.dumps(new_variables.get('contents'), ensure_ascii=False, default=str)[:2000]}")

        return new_variables

    def _supported_variable_fields(self) -> list[str]:
        """Gemini 下游请求可透传到上游 variables 的字段。"""
        return [
            'contents', 'tools', 'toolConfig', 'systemInstruction',
            'safetySettings', 'generationConfig', 'cachedContent', 'labels',
            # Gemini/Vertex 常见高级字段；上游不支持时会由上游返回明确错误，
            # 这里不主动丢弃，以最大化暴露上游能力。
            'modelArmorConfig', 'cachedContentName', 'requestOptions',
            'session', 'context', 'examples', 'instances', 'parameters',
        ]

    def _normalize_gemini_payload(self, payload: dict[str, Any]) -> dict[str, Any]:
        """兼容 REST、SDK 和部分 OpenAI-like 客户端传来的 Gemini 请求形态。"""
        normalized = payload.copy()
        if 'contents' in normalized:
            normalized['contents'] = self._normalize_contents(normalized['contents'])
        elif 'prompt' in normalized:
            normalized['contents'] = [{"role": "user", "parts": [{"text": str(normalized['prompt'])}]}]
        return normalized

    def _normalize_contents(self, contents: Any) -> Any:
        if contents is None:
            return []
        if isinstance(contents, str):
            return [{"role": "user", "parts": [{"text": contents}]}]
        if isinstance(contents, dict):
            return [self._normalize_content(contents)]
        if isinstance(contents, list):
            normalized: list[Any] = []
            pending_text_parts: list[dict[str, Any]] = []
            for item in cast(list[Any], contents):
                if isinstance(item, str):
                    pending_text_parts.append({"text": item})
                elif isinstance(item, dict):
                    if pending_text_parts:
                        normalized.append({"role": "user", "parts": pending_text_parts})
                        pending_text_parts = []
                    normalized.append(self._normalize_content(cast(dict[str, Any], item)))
            if pending_text_parts:
                normalized.append({"role": "user", "parts": pending_text_parts})
            return normalized
        return contents

    def _normalize_content(self, content: dict[str, Any]) -> dict[str, Any]:
        normalized = content.copy()
        if 'content' in normalized and 'parts' not in normalized:
            normalized['parts'] = self._normalize_parts(normalized.get('content'))
            normalized.pop('content', None)
        elif 'parts' in normalized:
            normalized['parts'] = self._normalize_parts(normalized.get('parts'))
        elif 'text' in normalized:
            normalized['parts'] = [{"text": str(normalized.pop('text'))}]
        else:
            normalized.setdefault('parts', [])

        role = normalized.get('role')
        if role == 'assistant':
            normalized['role'] = 'model'
        elif role == 'tool':
            normalized['role'] = 'function'
        elif not role:
            normalized['role'] = 'user'
        return normalized

    def _normalize_parts(self, parts: Any) -> list[dict[str, Any]]:
        if parts is None:
            return []
        if isinstance(parts, str):
            return [{"text": parts}]
        if isinstance(parts, dict):
            return [self._normalize_part(parts)]
        if isinstance(parts, list):
            normalized: list[dict[str, Any]] = []
            for part in cast(list[Any], parts):
                if isinstance(part, str):
                    normalized.append({"text": part})
                elif isinstance(part, dict):
                    normalized_part = self._normalize_part(cast(dict[str, Any], part))
                    if normalized_part:
                        normalized.append(normalized_part)
            return normalized
        return [{"text": str(parts)}]

    def _normalize_part(self, part: dict[str, Any]) -> dict[str, Any]:
        part_type = part.get('type')
        if part_type in {'text', 'input_text'}:
            return {"text": str(part.get('text', ''))}
        if part_type in {'image_url', 'input_image'}:
            url_obj = part.get('image_url') or part.get('input_image') or {}
            url = url_obj.get('url') if isinstance(url_obj, dict) else url_obj
            if isinstance(url, str) and url.startswith('data:'):
                mime, data = self._parse_data_uri(url)
                if mime and data:
                    return {"inlineData": {"mimeType": mime, "data": data}}
            if isinstance(url, str) and url.startswith(('http://', 'https://', 'gs://')):
                return {"fileData": {"mimeType": self._guess_mime_from_uri(url), "fileUri": url}}
        if part_type in {'media', 'file', 'file_data'}:
            file_uri = part.get('fileUri') or part.get('file_uri') or part.get('uri') or part.get('url')
            mime_type = part.get('mimeType') or part.get('mime_type') or self._guess_mime_from_uri(str(file_uri or ''))
            if file_uri:
                return {"fileData": {"mimeType": str(mime_type), "fileUri": str(file_uri)}}
        if part_type in {'inline_data', 'inlineData'}:
            inline = part.get('inlineData') or part.get('inline_data') or part
            if isinstance(inline, dict):
                data = inline.get('data')
                mime_type = inline.get('mimeType') or inline.get('mime_type') or part.get('mimeType') or part.get('mime_type')
                if data and mime_type:
                    return {"inlineData": {"mimeType": str(mime_type), "data": str(data)}}

        normalized: dict[str, Any] = {}
        for k, v in part.items():
            if k == 'type':
                continue
            normalized[snake_to_camel(k)] = v
        return normalized

    def _parse_data_uri(self, uri: str) -> tuple[str, str]:
        try:
            header, data = uri.split(',', 1)
            mime = header.split(':', 1)[1].split(';', 1)[0]
            return mime, data
        except (ValueError, IndexError):
            return "", ""

    def _guess_mime_from_uri(self, uri: str) -> str:
        lower = uri.lower().split('?', 1)[0].split('#', 1)[0]
        if lower.endswith(('.jpg', '.jpeg')):
            return 'image/jpeg'
        if lower.endswith('.webp'):
            return 'image/webp'
        if lower.endswith('.gif'):
            return 'image/gif'
        if lower.endswith('.png'):
            return 'image/png'
        if lower.endswith('.mp4'):
            return 'video/mp4'
        if lower.endswith('.mov'):
            return 'video/quicktime'
        if lower.endswith('.webm'):
            return 'video/webm'
        if lower.endswith('.mp3'):
            return 'audio/mpeg'
        if lower.endswith('.wav'):
            return 'audio/wav'
        if lower.endswith('.ogg'):
            return 'audio/ogg'
        if lower.endswith('.pdf'):
            return 'application/pdf'
        if lower.endswith('.txt'):
            return 'text/plain'
        return 'image/png'

    def _convert_tools_format(self, data: Any) -> Any:
        """专门处理工具格式转换，统一转换为 camelCase"""
        if isinstance(data, dict):
            new_dict: dict[str, Any] = {}
            data_dict: dict[str, Any] = cast(dict[str, Any], data)
            for k, v in data_dict.items():
                # 转换 function_declarations 为 functionDeclarations
                if k in ['function_declarations', 'functionDeclarations']:
                    new_dict['functionDeclarations'] = self._convert_tools_format(v)
                elif k in ['google_search', 'googleSearch']:
                    new_dict['googleSearch'] = self._convert_tools_format(v) if isinstance(v, (dict, list)) else v
                elif k in ['google_search_retrieval', 'googleSearchRetrieval']:
                    new_dict['googleSearchRetrieval'] = self._convert_tools_format(v) if isinstance(v, (dict, list)) else v
                elif k in ['code_execution', 'codeExecution']:
                    new_dict['codeExecution'] = self._convert_tools_format(v) if isinstance(v, (dict, list)) else v
                elif k in ['url_context', 'urlContext']:
                    new_dict['urlContext'] = self._convert_tools_format(v) if isinstance(v, (dict, list)) else v
                elif k == "parametersJsonSchema" and isinstance(v, dict):
                    # parametersJsonSchema 需要特殊处理，确保 properties 和 required 字段一致
                    new_dict[k] = self._convert_parameters_schema(cast(dict[str, Any], v))
                elif k == "parameters" and isinstance(v, dict):
                    # Schema 对象需要特殊处理
                    converted_v = v.copy() if isinstance(v, dict) else v
                    new_dict[k] = self._to_native_schema(converted_v)
                elif k == "name" and not v:  # Vertex AI Function name cannot be empty
                    continue
                else:
                    # 对于其他字段，转换为 camelCase（除了特殊字段）
                    camel_key = snake_to_camel(k) if '_' in k else k
                    new_dict[camel_key] = self._convert_tools_format(v) if isinstance(v, (dict, list)) else v
            return new_dict
        elif isinstance(data, list):
            return [self._convert_tools_format(item) for item in cast(list[Any], data)]
        else:
            return data

    def _convert_parameters_schema(self, schema: dict[str, Any]) -> dict[str, Any]:
        """
        转换 parametersJsonSchema，确保 properties 和 required 字段中的参数名一致
        统一使用 snake_case 格式，避免 camelCase 和 snake_case 混用导致的不匹配
        """
        new_schema: dict[str, Any] = schema.copy()
        unsupported_keys = {
            '$schema', '$id', '$defs', '$ref', 'definitions', 'additionalProperties',
            'patternProperties', 'unevaluatedProperties', 'dependentSchemas',
            'if', 'then', 'else', 'allOf', 'anyOf', 'oneOf', 'not',
            'examples', 'default', 'nullable'
        }
        for key in unsupported_keys:
            new_schema.pop(key, None)

        if isinstance(new_schema.get('type'), list):
            non_null_types = [item for item in cast(list[Any], new_schema['type']) if item != 'null']
            new_schema['type'] = non_null_types[0] if non_null_types else 'string'
        
        # 处理 properties 字段：将 camelCase 转换为 snake_case
        if 'properties' in new_schema and isinstance(new_schema['properties'], dict):
            old_properties: dict[str, Any] = cast(dict[str, Any], new_schema['properties'])
            new_properties: dict[str, Any] = {}
            
            for prop_name, prop_def in old_properties.items():
                # 将 camelCase 转换为 snake_case
                snake_name = camel_to_snake(str(prop_name))
                new_properties[snake_name] = prop_def
                
                # 递归处理嵌套的 schema
                if isinstance(prop_def, dict):
                    new_properties[snake_name] = self._convert_parameters_schema(cast(dict[str, Any], prop_def))
            
            new_schema['properties'] = new_properties
        
        # 处理 required 字段：确保使用 snake_case（通常已经是正确的）
        if 'required' in new_schema and isinstance(new_schema['required'], list):
            # required 字段通常已经是 snake_case，但为了保险起见，也进行转换
            new_required: list[Any] = []
            for req_name in cast(list[Any], new_schema['required']):
                if isinstance(req_name, str):
                    snake_name = camel_to_snake(req_name)
                    new_required.append(snake_name)
                else:
                    new_required.append(req_name)
            new_schema['required'] = new_required
        
        # 处理其他可能包含 schema 的字段
        for key, value in list(new_schema.items()):
            if key not in ['properties', 'required'] and isinstance(value, dict):
                new_schema[key] = self._convert_parameters_schema(cast(dict[str, Any], value))
        
        return new_schema

    def _to_native_schema(self, standard_schema: dict[str, Any]) -> dict[str, Any]:
        """
        将标准 JSON Schema 转换为 Vertex AI 原生 Map-style Schema
        
        Args:
            standard_schema: 标准 JSON Schema 对象
            
        Returns:
            Vertex AI 原生 Schema
        """
        
        native_schema = standard_schema.copy()
        unsupported_keys = {
            '$schema', '$id', '$defs', '$ref', 'definitions', 'additionalProperties',
            'patternProperties', 'unevaluatedProperties', 'dependentSchemas',
            'if', 'then', 'else', 'allOf', 'anyOf', 'oneOf', 'not',
            'examples', 'default', 'nullable'
        }
        for key in unsupported_keys:
            native_schema.pop(key, None)
        
        # Vertex AI 要求类型必须是大写 (例如: STRING, OBJECT, INTEGER)
        if 'type' in native_schema and isinstance(native_schema['type'], list):
            non_null_types = [item for item in cast(list[Any], native_schema['type']) if item != 'null']
            native_schema['type'] = non_null_types[0] if non_null_types else 'string'

        if 'type' in native_schema and isinstance(native_schema['type'], str):
            native_schema['type'] = native_schema['type'].upper()
            
        if 'properties' in native_schema and isinstance(native_schema['properties'], dict):
            native_props: list[dict[str, str | dict[str, Any]]] = []
            props_dict = cast(dict[str, Any], native_schema['properties'])
            for key, value in props_dict.items():
                # 递归处理嵌套对象
                if isinstance(value, dict):
                    converted_value = self._to_native_schema(cast(dict[str, Any], value))
                else:
                    converted_value = {}
                
                native_props.append({
                    "key": str(key),
                    "value": converted_value
                })
            native_schema['properties'] = native_props
        
        # 处理数组项
        if 'items' in native_schema and isinstance(native_schema['items'], dict):
            items_dict = cast(dict[str, Any], native_schema['items'])
            native_schema['items'] = self._to_native_schema(items_dict)
            
        return native_schema
    
    def _handle_system_instruction(self, new_variables: dict[str, Any]) -> None:
        """处理 systemInstruction：如果没有 user content，则转换为 user message"""
        system_instruction_content = new_variables.get('systemInstruction')
        if not system_instruction_content:
            return
            
        contents = new_variables.get('contents', [])
        
        # 检查是否已有 user 角色
        contents_list: list[Any] = cast(list[Any], contents) if isinstance(contents, list) else []
        has_user_role = any(
            isinstance(content, dict) and cast(dict[str, Any], content).get('role') == 'user'
            for content in contents_list
        )
        
        if has_user_role:
            return
            
        # 提取文本内容
        text_from_system = self._extract_text_from_instruction(system_instruction_content)
        if not text_from_system:
            return
            
        # 转换为 user message
        user_message = {
            'role': 'user',
            'parts': [{'text': text_from_system}]
        }
        
        # 显式转换 contents 为 list[Any] 以修复 pylance 报错
        new_contents: list[Any] = list(contents_list)
        new_contents.insert(0, user_message)
        new_variables['contents'] = new_contents
        del new_variables['systemInstruction']
    
    def _extract_text_from_instruction(self, instruction: Any) -> str:
        """从 system instruction 中提取文本内容"""
        if isinstance(instruction, str):
            return instruction
        elif isinstance(instruction, dict):
            instruction_dict = cast(dict[str, Any], instruction)
            parts = instruction_dict.get('parts', [])
            if isinstance(parts, list):
                text_parts = []
                for part in parts:
                    if isinstance(part, dict) and 'text' in part:
                        text_parts.append(str(part['text']))
                return "".join(text_parts)
        return ""
    
    def _normalize_tools_format(self, tools: Any) -> list[dict[str, Any]]:
        """标准化 tools 格式为 Vertex AI 期望的格式 (List[Tool])"""
        converted_tools: Any = self._convert_tools_format(tools)
        tool_keys = {
            'functionDeclarations', 'googleSearch', 'googleSearchRetrieval',
            'codeExecution', 'retrieval', 'urlContext'
        }
        
        if isinstance(converted_tools, dict):
            # 如果是字典，且包含 functionDeclarations，将其包裹在列表中
            if any(key in converted_tools for key in tool_keys):
                return [cast(dict[str, Any], converted_tools)]
            # 如果是单个 FunctionDeclaration，包裹成 Tool 再包裹在列表中
            if 'name' in converted_tools:
                return [{"functionDeclarations": [cast(dict[str, Any], converted_tools)]}]
            return []
            
        if not isinstance(converted_tools, list) or len(cast(list[Any], converted_tools)) == 0:
            return []
            
        converted_tools_list: list[Any] = cast(list[Any], converted_tools)
        normalized_tools: list[dict[str, Any]] = []
        function_decls: list[dict[str, Any]] = []
        for item in converted_tools_list:
            if not isinstance(item, dict):
                continue
            item_dict = cast(dict[str, Any], item)
            if any(key in item_dict for key in tool_keys):
                normalized_tools.append(item_dict)
            elif item_dict.get('name'):
                function_decls.append(item_dict)

        if function_decls:
            normalized_tools.insert(0, {"functionDeclarations": function_decls})

        return normalized_tools

    def _handle_inline_data_case(self, contents: Any) -> Any:
        """
        递归处理 contents，确保 inlineData/mimeType 字段名正确 (适配各种客户端传参)
        """
        if isinstance(contents, list):
            return [self._handle_inline_data_case(item) for item in cast(list[Any], contents)]
        if isinstance(contents, dict):
            new_dict: dict[str, Any] = {}
            for k, v in cast(dict[str, Any], contents).items():
                camel_k = snake_to_camel(k)
                if camel_k == 'inlineData' and isinstance(v, dict):
                    v_dict = cast(dict[str, Any], v)
                    new_inline_data = {}
                    for ik, iv in v_dict.items():
                        camel_ik = snake_to_camel(ik)
                        new_inline_data[camel_ik] = iv
                    new_dict['inlineData'] = new_inline_data
                else:
                    new_dict[camel_k] = self._handle_inline_data_case(v)
            return new_dict
        return contents

    def _filter_empty_contents(self, contents: Any) -> Any:
        """
        过滤掉空的 contents（parts 为空数组的消息）
        Vertex AI 要求每个 content 至少包含一个 part
        """
        if not isinstance(contents, list):
            return contents
        
        filtered_contents: list[Any] = []
        contents_list: list[Any] = cast(list[Any], contents)
        
        # 收集所有 functionCall 的名称，用于修复 functionResponse
        function_call_names: list[str] = []
        for content in contents_list:
            if isinstance(content, dict):
                content_dict = cast(dict[str, Any], content)
                parts = content_dict.get('parts', [])
                if isinstance(parts, list):
                    for part in cast(list[Any], parts):
                        if isinstance(part, dict):
                            part_dict = cast(dict[str, Any], part)
                            func_call = part_dict.get('functionCall')
                            if isinstance(func_call, dict):
                                func_call_dict = cast(dict[str, Any], func_call)
                                name = func_call_dict.get('name')
                                if name and isinstance(name, str):
                                    function_call_names.append(name)
        
        # 追踪已使用的 functionCall 名称（按顺序），用于修复 functionResponse
        fc_tracker = _FcNameTracker(function_call_names)
        
        for content in contents_list:
            if isinstance(content, dict):
                content_dict: dict[str, Any] = cast(dict[str, Any], content)
                parts = content_dict.get('parts', [])
                # 只保留有 parts 且 parts 不为空的 content
                if isinstance(parts, list) and len(cast(list[Any], parts)) > 0:
                    parts_list: list[Any] = cast(list[Any], parts)
                    # 过滤并验证 parts 中的有效内容
                    valid_parts: list[Any] = []
                    for part in parts_list:
                        if isinstance(part, dict):
                            part_dict = cast(dict[str, Any], part)
                            
                            cleaned_part = clean_part(part_dict, function_call_names, fc_tracker)
                            if cleaned_part:
                                valid_parts.append(cleaned_part)
                    
                    if valid_parts:
                        # 更新 content 的 parts
                        filtered_content = content_dict.copy()
                        filtered_content['parts'] = valid_parts
                        filtered_contents.append(filtered_content)
                    else:
                        logger.debug(f"过滤掉空的 content: role={content_dict.get('role', 'unknown')}")
                else:
                    logger.debug(f"过滤掉空的 content: role={content_dict.get('role', 'unknown')}")
            else:
                # 非 Dict 类型的 content，保留
                filtered_contents.append(content)
        
        return filtered_contents

    @staticmethod
    def prepare_headers(creds: dict[str, Any]) -> dict[str, str]:
        """准备请求头"""
        # 提取原始头信息
        headers = RequestTransformer._extract_headers_from_creds(creds)
        
        # 设置必要的头信息
        headers['content-type'] = 'application/json'
        
        # 移除可能导致问题的头
        problematic_headers = [
            'content-length', 'Content-Length', 'host', 'Host',
            'connection', 'Connection', 'accept-encoding'
        ]
        for header in problematic_headers:
            headers.pop(header, None)
            
        return headers
    
    @staticmethod
    def _extract_headers_from_creds(creds: dict[str, Any]) -> dict[str, str]:
        """从凭证中提取头信息"""
        if hasattr(creds, 'model_dump') and hasattr(creds, 'headers'):
            headers_attr = getattr(creds, 'headers')
            if isinstance(headers_attr, dict):
                return cast(dict[str, str], headers_attr).copy()
        
        raw_headers = creds.get('headers')
        if isinstance(raw_headers, dict):
            return cast(dict[str, str], raw_headers).copy()
            
        return {}


def _is_safety_block(error: VertexError) -> bool:
    """判断一个 VertexError 是否由安全拦截引起"""
    msg = (error.message or "").lower()
    status = (error.status or "").lower()
    keywords = ("safety", "block_reason", "content_filter", "finish_reason_safety")
    return any(k in msg or k in status for k in keywords)


def _build_safety_result(error: VertexError) -> dict[str, Any]:
    """构造 Gemini 标准格式的安全拦截响应"""
    return {
        "candidates": [],
        "promptFeedback": {
            "blockReason": error.status or "SAFETY",
            "safetyRatings": [],
            "blockReasonMessage": error.message,
        },
    }


class ResponseAggregator:
    """响应聚合器"""

    @staticmethod
    async def aggregate_stream(stream_generator: Any, _raw_image_response: bool = False) -> dict[str, Any]:
        """
        聚合流式响应为非流式对象
        """
        all_parts: list[dict[str, Any]] = []
        finish_reason: str | None = None
        finish_message: str | None = None
        safety_ratings: list[dict[str, Any]] = []
        citation_metadata: dict[str, Any] = {}
        grounding_metadata: dict[str, Any] = {}
        token_count: int | None = None
        avg_logprobs: float | None = None
        logprobs_result: dict[str, Any] | None = None
        candidate_index = 0
        usage_metadata: dict[str, Any] = {}
        
        create_time: str | None = None
        model_version: str | None = None
        prompt_feedback: dict[str, Any] = {}
        response_id: str | None = None
        model_status: dict[str, Any] | None = None
        
        try:
            async for chunk_str in stream_generator:
                if isinstance(chunk_str, dict):
                    chunk = chunk_str
                else:
                    actual_json_str = chunk_str.strip()
                    if actual_json_str.startswith("data: "):
                        actual_json_str = actual_json_str[6:]
                    
                    if not actual_json_str:
                        continue
                    
                    try:
                        chunk = json.loads(actual_json_str)
                    except json.JSONDecodeError as e:
                        logger.debug(f"JSON 解析失败，跳过此块: {e}")
                        continue
                
                # 检查 chunk 是否包含错误 (使用统一解析逻辑)
                parsed_error = parse_error_response(chunk)
                if parsed_error:
                    raise parsed_error
                
                # 提取顶层元数据
                create_time = chunk.get('createTime') or create_time
                model_version = chunk.get('modelVersion') or model_version
                prompt_feedback = chunk.get('promptFeedback', {}) or prompt_feedback
                response_id = chunk.get('responseId') or response_id
                usage_metadata = chunk.get('usageMetadata', {}) or usage_metadata
                model_status = chunk.get('modelStatus') or model_status
                
                candidates = chunk.get('candidates', [])
                if candidates:
                    candidate = candidates[0]
                    
                    # 提取 parts
                    content_obj = candidate.get('content', {})
                    parts = content_obj.get('parts', [])
                    if parts:
                        all_parts.extend(parts)
                    
                    # 提取 candidate 元数据
                    finish_reason = candidate.get('finishReason') or finish_reason
                    finish_message = candidate.get('finishMessage') or finish_message
                    safety_ratings = candidate.get('safetyRatings') or safety_ratings
                    citation_metadata = candidate.get('citationMetadata') or citation_metadata
                    grounding_metadata = candidate.get('groundingMetadata') or grounding_metadata
                    if candidate.get('tokenCount') is not None:
                        token_count = candidate['tokenCount']
                    if candidate.get('avgLogprobs') is not None:
                        avg_logprobs = candidate['avgLogprobs']
                    if candidate.get('logprobsResult') is not None:
                        logprobs_result = candidate['logprobsResult']
                    if candidate.get('index') is not None:
                        candidate_index = candidate['index']
                    
        except VertexError as ve:
            # 检测安全拦截：上游返回 GraphQL 错误但实际上是安全过滤
            if _is_safety_block(ve):
                logger.warning(f"上游安全拦截: {ve.message}")
                result = _build_safety_result(ve)
                result["usageMetadata"] = usage_metadata if usage_metadata else {
                    "promptTokenCount": 0,
                    "candidatesTokenCount": 0,
                    "totalTokenCount": 0,
                }
                return result
            raise
        except Exception as e:
            raise InternalError(message=f"Non-streaming request error: {e}")
        
        # 处理图片响应特例
        inline_images = ResponseAggregator._extract_inline_images(all_parts)
        if _raw_image_response and inline_images:
            return {"created": int(time.time()), "data": [{"b64_json": data} for _, data in inline_images]}

        full_text_content = "".join(str(p['text']) for p in all_parts if 'text' in p)

        if full_text_content.startswith("![Generated Image](data:"):
            start_idx = full_text_content.find('(') + 1
            end_idx = full_text_content.rfind(')')
            data_url = full_text_content[start_idx:end_idx]
            if _raw_image_response:
                try:
                    _, encoded = data_url.split(',', 1)
                    return {"created": int(time.time()), "data": [{"b64_json": encoded}]}
                except Exception:
                    return {"created": int(time.time()), "data": []}
            else:
                return {"resultUrl": data_url}
        
        # 构建最终响应
        # 当 parts 为空时，检查是否有安全拦截
        blocked = bool(prompt_feedback.get('blockReason'))
        
        if not all_parts and blocked:
            # 上游安全拦截：返回空 candidates + promptFeedback.blockReason
            result: dict[str, Any] = {"candidates": []}
        else:
            if not all_parts:
                all_parts = []
            
            if not finish_reason:
                finish_reason = "STOP"
            
            result_candidate: dict[str, Any] = {
                "index": candidate_index
            }
            if finish_reason:
                result_candidate["finishReason"] = finish_reason.upper()
                
            result_candidate["content"] = {
                "parts": all_parts,
                "role": "model"
            }
            
            # safetyRatings 始终包含（官方 Gemini API 总是返回此字段）
            if not safety_ratings:
                safety_ratings = []
            result_candidate["safetyRatings"] = safety_ratings
            
            # groundingAttributions 始终包含（官方 Gemini API 总是返回此字段）
            result_candidate["groundingAttributions"] = []
            
            # 其他可选字段，只添加非空值
            optional_candidate_fields: dict[str, Any] = {
                "finishMessage": finish_message,
                "citationMetadata": citation_metadata,
                "groundingMetadata": grounding_metadata,
                "tokenCount": token_count,
                "avgLogprobs": avg_logprobs,
                "logprobsResult": logprobs_result
            }
            
            for key, value in optional_candidate_fields.items():
                if value is not None and value != [] and value != {}:
                    result_candidate[key] = value
            
            # 构建最终结果
            result: dict[str, Any] = {"candidates": [result_candidate]}
        
        # usageMetadata 始终包含（官方 Gemini API 总是返回此字段）
        if not usage_metadata or usage_metadata == {}:
            usage_metadata = {
                "promptTokenCount": 0,
                "candidatesTokenCount": 0,
                "totalTokenCount": 0,
                "promptTokensDetails": [{"modality": "TEXT", "tokenCount": 0}],
                "candidatesTokensDetails": [{"modality": "TEXT", "tokenCount": 0}],
            }
        else:
            # 补充详细 token 分解字段（如果上游没提供）
            if "promptTokensDetails" not in usage_metadata and "promptTokenCount" in usage_metadata:
                usage_metadata["promptTokensDetails"] = [{"modality": "TEXT", "tokenCount": usage_metadata["promptTokenCount"]}]
            if "candidatesTokensDetails" not in usage_metadata and "candidatesTokenCount" in usage_metadata:
                usage_metadata["candidatesTokensDetails"] = [{"modality": "TEXT", "tokenCount": usage_metadata["candidatesTokenCount"]}]
        result["usageMetadata"] = usage_metadata
        
        # promptFeedback 始终包含（官方 Gemini API 总是返回此字段）
        if not prompt_feedback or prompt_feedback == {}:
            prompt_feedback = {"safetyRatings": []}
        result["promptFeedback"] = prompt_feedback
        
        # 其他顶层可选字段
        optional_result_fields: dict[str, Any] = {
            "createTime": create_time,
            "modelVersion": model_version,
            "responseId": response_id,
            "modelStatus": model_status
        }
        
        for key, value in optional_result_fields.items():
            if value is not None and value != {} and value != "":
                result[key] = value
        
        return result

    @staticmethod
    def _extract_inline_images(parts: list[dict[str, Any]]) -> list[tuple[str, str]]:
        images: list[tuple[str, str]] = []
        for part in parts:
            inline_data = part.get('inlineData') or part.get('inline_data')
            if not isinstance(inline_data, dict):
                continue
            mime_type = inline_data.get('mimeType') or inline_data.get('mime_type') or 'image/png'
            data = inline_data.get('data')
            if isinstance(data, str) and data.strip():
                images.append((str(mime_type), data))
        return images

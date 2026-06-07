"""Unified part cleaning and content block merging.

Consolidates duplicated logic from parser.py, transform.py, and oai_adapter.py.
"""

import base64
import json
from typing import Any, cast

from src.utils.logger import get_logger

logger = get_logger(__name__)


class _FcNameTracker:
    """Track used functionCall names in order, for repairing functionResponse.name."""
    def __init__(self, names: list[str]) -> None:
        self._names = [n for n in names if n and str(n).strip()]
        self._idx = 0

    def next_name(self) -> str | None:
        if self._idx < len(self._names):
            name = self._names[self._idx]
            self._idx += 1
            if name and str(name).strip():
                return str(name).strip()
        return None


def normalize_base64(data: str) -> str:
    """Normalize base64 data: strip data URI prefix, replace URL-safe chars, add padding."""
    value = data.strip()
    if "," in value and value.startswith("data:"):
        _, value = value.split(",", 1)
    value = value.replace("-", "+").replace("_", "/")
    padding = len(value) % 4
    if padding:
        value += "=" * (4 - padding)
    return value


def clean_part(
    part: dict[str, Any],
    function_call_names: list[str] | None = None,
    fc_tracker: _FcNameTracker | None = None,
) -> dict[str, Any] | None:
    """Clean a single part dict: remove empty fields, fix edge cases.

    Returns None if the part has no valid content after cleaning.

    Handles:
    - text: remove empty strings
    - thought: convert bool to empty string (Vertex API requires string)
    - thoughtSignature: preserve, encode sentinel value later
    - functionCall: drop if name is empty
    - functionResponse: repair empty name from functionCall ordering
    - inlineData: drop if data or mimeType missing
    - fileData: drop if fileUri or mimeType missing
    - executableCode, codeExecutionResult, videoMetadata, mediaResolution: passthrough
    """
    has_valid_content = False
    cleaned_part: dict[str, Any] = {}

    if 'text' in part:
        text_value = part['text']
        if text_value is not None:
            text_str = str(text_value)
            if text_str == "":
                from src.core.errors import EmptyResponseError
                raise EmptyResponseError("Part text is empty")
            cleaned_part['text'] = text_value
            has_valid_content = True

    if 'thought' in part:
        thought_val = part['thought']
        if isinstance(thought_val, bool):
            cleaned_part['thought'] = thought_val
        else:
            cleaned_part['thought'] = thought_val

    if 'functionCall' in part:
        func_call = part['functionCall']
        if isinstance(func_call, dict):
            func_call_dict = cast(dict[str, Any], func_call)
            name = func_call_dict.get('name')
            if name and str(name).strip():
                fixed = func_call_dict.copy()
                if isinstance(fixed.get('args'), str):
                    try:
                        fixed['args'] = json.loads(cast(str, fixed['args']))
                    except json.JSONDecodeError:
                        fixed['args'] = {"raw": fixed['args']}
                cleaned_part['functionCall'] = fixed
                has_valid_content = True

    if 'functionResponse' in part:
        func_response = part['functionResponse']
        if isinstance(func_response, dict):
            func_response_dict = cast(dict[str, Any], func_response)
            current_name = func_response_dict.get('name')

            if not current_name:
                inferred_name: str | None = None
                if fc_tracker is not None:
                    inferred_name = fc_tracker.next_name()
                elif function_call_names:
                    inferred_name = function_call_names[-1]

                if inferred_name:
                    logger.warning(f"修复空的 functionResponse.name，推断为: {inferred_name}")
                    fixed = func_response_dict.copy()
                    fixed['name'] = inferred_name
                    cleaned_part['functionResponse'] = fixed
                    has_valid_content = True
                else:
                    logger.debug("没有未匹配的 functionCall，丢弃空的 functionResponse")
            else:
                fixed = func_response_dict.copy()
                if 'response' in fixed and not isinstance(fixed['response'], dict):
                    fixed['response'] = {"result": fixed['response']}
                cleaned_part['functionResponse'] = fixed
                has_valid_content = True

    if 'inlineData' in part:
        inline_data = part['inlineData']
        if isinstance(inline_data, dict):
            inline_data_dict = cast(dict[str, Any], inline_data)
            if (inline_data_dict.get('data') and
                str(inline_data_dict['data']).strip() and
                inline_data_dict.get('mimeType') and
                str(inline_data_dict['mimeType']).strip()):
                cleaned_part['inlineData'] = inline_data
                has_valid_content = True

    if 'fileData' in part:
        file_data = part['fileData']
        if isinstance(file_data, dict):
            file_data_dict = cast(dict[str, Any], file_data)
            if (file_data_dict.get('fileUri') and
                str(file_data_dict['fileUri']).strip() and
                file_data_dict.get('mimeType') and
                str(file_data_dict['mimeType']).strip()):
                cleaned_part['fileData'] = file_data
                has_valid_content = True

    for code_key in ('executableCode', 'codeExecutionResult'):
        if code_key in part and part[code_key]:
            cleaned_part[code_key] = part[code_key]
            has_valid_content = True

    if 'thoughtSignature' in part:
        cleaned_part['thoughtSignature'] = part['thoughtSignature']

    passthrough_part_keys = (
        'videoMetadata', 'mediaResolution',
    )
    for key in passthrough_part_keys:
        if key in part and part[key]:
            cleaned_part[key] = part[key]

    if 'thought' in cleaned_part and not isinstance(cleaned_part['thought'], (str, bool)):
        cleaned_part['thought'] = ''

    if 'functionResponse' in cleaned_part:
        cleaned_part.pop('thought', None)
        cleaned_part.pop('thoughtSignature', None)
    elif 'functionCall' in cleaned_part or 'thought' in cleaned_part or 'thoughtSignature' in cleaned_part:
        cleaned_part['thoughtSignature'] = 'skip_thought_signature_validator'

    if cleaned_part.get('text') and not cleaned_part.get('thought'):
        cleaned_part.pop('thought', None)
        cleaned_part.pop('thoughtSignature', None)

    if has_valid_content:
        return cleaned_part
    logger.debug("过滤掉没有有效内容的 part")
    return None


def encode_thought_signature(contents: Any, _depth: int = 0) -> Any:
    """Recursively encode thoughtSignature fields to base64 in contents."""
    MAX_DEPTH = 64
    if _depth > MAX_DEPTH:
        logger.warning(f"thoughtSignature 处理超过最大递归深度 ({MAX_DEPTH})，跳过")
        return contents

    if isinstance(contents, list):
        return [encode_thought_signature(item, _depth + 1) for item in cast(list[Any], contents)]
    if isinstance(contents, dict):
        new_dict: dict[str, Any] = {}
        contents_dict = cast(dict[str, Any], contents)
        for k, v in contents_dict.items():
            if k == 'parts' and isinstance(v, list):
                new_parts: list[Any] = []
                for part in cast(list[Any], v):
                    if isinstance(part, dict):
                        new_part = cast(dict[str, Any], part).copy()
                        if 'thoughtSignature' in new_part:
                            sig = new_part['thoughtSignature']
                            if isinstance(sig, str) and sig == "skip_thought_signature_validator":
                                new_part['thoughtSignature'] = base64.b64encode(
                                    sig.encode('utf-8')
                                ).decode('utf-8')
                        new_parts.append(new_part)
                    else:
                        new_parts.append(part)
                new_dict[k] = new_parts
            else:
                new_dict[k] = encode_thought_signature(v, _depth + 1) if isinstance(v, (dict, list)) else v
        return new_dict
    return contents


def handle_base64_in_contents(contents: Any) -> Any:
    """Recursively normalize base64 data in inlineData across contents."""
    try:
        if isinstance(contents, list):
            return [handle_base64_in_contents(item) for item in cast(list[Any], contents)]
        if isinstance(contents, dict):
            new_dict: dict[str, Any] = {}
            for k, v in cast(dict[str, Any], contents).items():
                if k == 'inlineData' and isinstance(v, dict):
                    v_dict = cast(dict[str, Any], v)
                    if 'data' in v_dict and isinstance(v_dict['data'], str):
                        try:
                            new_inline_data = v_dict.copy()
                            new_inline_data['data'] = normalize_base64(v_dict['data'])
                            new_dict[k] = new_inline_data
                            continue
                        except Exception:
                            pass
                new_dict[k] = handle_base64_in_contents(v)
            return new_dict
        return contents
    except Exception as e:
        logger.warning(f"Base64 内容处理失败: {e}")
        return contents


def merge_content_blocks(parts: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Merge consecutive same-type text blocks (thought+thought, text+text).

    Example: thought1 → text1 → thought2 → text2
    Becomes: thought1+thought2 → text1 → text2
    """
    cleaned_parts = [_clean_simple(part) for part in parts]
    cleaned_parts = [p for p in cleaned_parts if p]
    if not cleaned_parts:
        return []

    merged: list[dict[str, Any]] = []
    current_group: dict[str, Any] | None = None

    for part in cleaned_parts:
        is_text = 'text' in part and part['text'] is not None and str(part['text']) != ""

        if not is_text:
            merged.append(part)
            current_group = None
            continue

        is_thought = bool(part.get('thought', False))

        if current_group is not None and bool(current_group.get('thought', False)) == is_thought:
            current_group['text'] += str(part['text'])
            if 'thoughtSignature' in part and 'thoughtSignature' not in current_group:
                current_group['thoughtSignature'] = part['thoughtSignature']
        else:
            new_part: dict[str, Any] = {'text': str(part['text'])}
            if is_thought:
                new_part['thought'] = True
                if 'thoughtSignature' in part:
                    new_part['thoughtSignature'] = part['thoughtSignature']
            merged.append(new_part)
            current_group = new_part

    return merged


def _clean_simple(part: dict[str, Any]) -> dict[str, Any] | None:
    """Lightweight cleaning for content-block merging (no FcNameTracker needed)."""
    try:
        from src.core.types import ContentPart
        content_part = ContentPart.model_validate(part)
        cleaned = content_part.model_dump(exclude_none=True, by_alias=True)
        if 'text' in cleaned and str(cleaned['text']) == "":
            del cleaned['text']
        if 'functionCall' in cleaned:
            func_call = cast(dict[str, Any], cleaned['functionCall'])
            name = func_call.get('name')
            if not name or not str(name).strip():
                del cleaned['functionCall']
        return cleaned
    except Exception as e:
        logger.debug(f"Pydantic 验证 part 失败，回退到基础清洗: {e}")
        return clean_part(part)

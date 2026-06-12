"""OpenAI 兼容适配层

将 OpenAI Chat Completion 格式转换为 Gemini 格式（请求），
将 Gemini SSE 格式转换为 OpenAI 格式（响应）。
"""

import json
import math
import time
import uuid
import copy
import base64
import binascii
from typing import Any

from src.utils.logger import get_logger
from src.api.part_cleaner import normalize_base64
from src.api.model_config import ModelConfigBuilder
from src.utils.string_utils import snake_to_camel

logger = get_logger(__name__)

DEFAULT_IMAGE_MODEL = "gemini-2.5-flash-image"
OPENAI_IMAGE_MODEL_ALIASES = {
    "gpt-image-1",
    "dall-e-2",
    "dall-e-3",
}

# Default Gemini model for text-to-speech requests. OpenAI TTS model names
# (e.g. tts-1, gpt-4o-mini-tts) fall back to this.
DEFAULT_TTS_MODEL = "gemini-3.1-flash-tts-preview"
OPENAI_TTS_MODEL_ALIASES = {
    "tts-1",
    "tts-1-hd",
    "gpt-4o-mini-tts",
}

# OpenAI voice names -> Gemini prebuilt voice names.
OPENAI_VOICE_MAP = {
    "alloy": "Kore",
    "echo": "Puck",
    "fable": "Charon",
    "onyx": "Fenrir",
    "nova": "Aoede",
    "shimmer": "Leda",
    "ash": "Orus",
    "ballad": "Zephyr",
    "coral": "Aoede",
    "sage": "Charon",
    "verse": "Puck",
}
DEFAULT_TTS_VOICE = "Kore"
# Native Gemini prebuilt voice names: passed through verbatim if requested.
GEMINI_VOICE_NAMES = {
    "Kore", "Puck", "Charon", "Aoede", "Fenrir", "Leda", "Orus", "Zephyr",
    "Autonoe", "Enceladus", "Iapetus", "Umbriel", "Algieba", "Despina",
    "Erinome", "Algenib", "Rasalgethi", "Laomedeia", "Achernar", "Alnilam",
    "Schedar", "Gacrux", "Pulcherrima", "Achird", "Zubenelgenubi", "Vindemiatrix",
    "Sadachbia", "Sadaltager", "Sulafat",
}

FINISH_REASON_MAP = {
    "STOP": "stop",
    "MAX_TOKENS": "length",
    "SAFETY": "content_filter",
    "RECITATION": "content_filter",
    "TOOL_CALLS": "tool_calls",
    "MALFORMED_FUNCTION_CALL": "tool_calls",
    "BLOCKLIST": "content_filter",
    "PROHIBITED_CONTENT": "content_filter",
    "SPII": "content_filter",
    "OTHER": "content_filter",
}


class OAIRequestConverter:
    """OpenAI → Gemini 请求转换"""

    @staticmethod
    def convert(body: dict[str, Any]) -> tuple[str, dict[str, Any]]:
        """将 OAI ChatCompletion 请求转为 (model, gemini_payload)"""
        model = body["model"]
        # Phase 2: strip thinking suffix, extract thinking config from model name
        clean_model, thinking_from_suffix = ModelConfigBuilder.parse_thinking_suffix(model)
        model = clean_model
        messages = body.get("messages", [])

        contents: list[dict[str, Any]] = []
        system_parts: list[dict[str, str]] = []
        tool_call_name_by_id: dict[str, str] = {}

        for msg in messages:
            role = msg.get("role", "")
            content = msg.get("content")

            if role in {"system", "developer"}:
                system_text = _content_to_text(content)
                if system_text:
                    system_parts.append({"text": system_text})

            elif role == "user":
                parts = _convert_content_to_parts(content)
                if parts:
                    contents.append({"role": "user", "parts": parts})

            elif role == "assistant":
                parts: list[dict[str, Any]] = []
                assistant_text = _content_to_text(content)
                if assistant_text:
                    parts.append({"text": assistant_text})
                tool_calls = msg.get("tool_calls")
                if tool_calls:
                    for tc in tool_calls:
                        parsed = _extract_oai_tool_call(tc)
                        if not parsed:
                            continue
                        tc_id, func_name, args_obj = parsed
                        if tc_id:
                            tool_call_name_by_id[str(tc_id)] = func_name
                        parts.append({"functionCall": {"name": func_name, "args": args_obj}})
                if parts:
                    contents.append({"role": "model", "parts": parts})

            elif role == "tool":
                tool_call_id = msg.get("tool_call_id")
                name = msg.get("name") or tool_call_name_by_id.get(str(tool_call_id), "unknown")
                raw = msg.get("content", "")
                resp_obj = _parse_tool_response(raw)
                contents.append({
                    "role": "function",
                    "parts": [{"functionResponse": {"name": name, "response": resp_obj}}]
                })
            
            elif role == "function":
                name = msg.get("name") or "unknown"
                resp_obj = _parse_tool_response(msg.get("content", ""))
                contents.append({
                    "role": "function",
                    "parts": [{"functionResponse": {"name": name, "response": resp_obj}}]
                })

        gemini_payload: dict[str, Any] = {"contents": contents}

        if system_parts:
            gemini_payload["systemInstruction"] = {"parts": system_parts}

        # tools
        oai_tools = body.get("tools")
        if not oai_tools and body.get("functions"):
            oai_tools = [{"type": "function", "function": f} for f in body.get("functions", [])]
        declared_tool_names: set[str] = set()
        if oai_tools:
            func_decls = []
            for t in oai_tools:
                f = _extract_oai_function_tool(t)
                if f:
                    decl: dict[str, Any] = {"name": f["name"]}
                    declared_tool_names.add(str(f["name"]))
                    if f.get("description"):
                        decl["description"] = f["description"]
                    if f.get("parameters"):
                        decl["parameters"] = _clean_function_parameters(copy.deepcopy(f["parameters"]))
                    else:
                        decl["parameters"] = {"type": "object", "properties": {}}
                    func_decls.append(decl)
            if func_decls:
                gemini_payload["tools"] = [{"functionDeclarations": func_decls}]

        # tool_choice
        tc = body.get("tool_choice", body.get("function_call"))
        if tc:
            if tc == "none":
                gemini_payload["toolConfig"] = {"functionCallingConfig": {"mode": "NONE"}}
            elif tc == "auto":
                gemini_payload["toolConfig"] = {"functionCallingConfig": {"mode": "AUTO"}}
            elif tc == "required":
                if not declared_tool_names:
                    raise ValueError("tool_choice='required' requires at least one tool")
                gemini_payload["toolConfig"] = {"functionCallingConfig": {"mode": "ANY"}}
            elif isinstance(tc, dict) and (tc.get("type") == "function" or "name" in tc):
                fn_name = tc.get("function", {}).get("name") if tc.get("type") == "function" else tc.get("name")
                if fn_name:
                    if declared_tool_names and fn_name not in declared_tool_names:
                        raise ValueError(f"tool_choice references unknown function: {fn_name}")
                    gemini_payload["toolConfig"] = {
                        "functionCallingConfig": {"mode": "ANY", "allowedFunctionNames": [fn_name]}
                    }
            else:
                raise ValueError(f"Unsupported tool_choice/function_call: {tc}")

        # generationConfig
        gen_cfg: dict[str, Any] = {}
        for oai_key, gemini_key in [
            ("temperature", "temperature"),
            ("top_p", "topP"),
            ("top_k", "topK"),
            ("presence_penalty", "presencePenalty"),
            ("frequency_penalty", "frequencyPenalty"),
            ("seed", "seed"),
        ]:
            if oai_key in body and body[oai_key] is not None:
                gen_cfg[gemini_key] = body[oai_key]

        max_tokens = body.get("max_tokens") or body.get("max_completion_tokens")
        if max_tokens is not None:
            gen_cfg["maxOutputTokens"] = max_tokens

        if body.get("logprobs") is not None:
            gen_cfg["responseLogprobs"] = bool(body.get("logprobs"))
        if body.get("top_logprobs") is not None:
            gen_cfg["logprobs"] = body.get("top_logprobs")

        stop = body.get("stop")
        if stop is not None:
            gen_cfg["stopSequences"] = [stop] if isinstance(stop, str) else stop

        rf = body.get("response_format")
        if isinstance(rf, dict):
            rf_type = rf.get("type")
            if rf_type == "json_object":
                gen_cfg["responseMimeType"] = "application/json"
            elif rf_type == "json_schema":
                gen_cfg["responseMimeType"] = "application/json"
                schema = rf.get("json_schema", {}).get("schema")
                if schema:
                    gen_cfg["responseSchema"] = _clean_function_parameters(copy.deepcopy(schema))

        modalities = body.get("modalities") or body.get("response_modalities")
        if isinstance(modalities, list):
            converted_modalities = [_convert_modality(m) for m in modalities if _convert_modality(m)]
            if converted_modalities:
                gen_cfg["responseModalities"] = converted_modalities
        elif _looks_like_image_model(model):
            gen_cfg.setdefault("responseModalities", ["TEXT", "IMAGE"])

        # Image resolution control for chat-based image generation. Image models
        # honor generationConfig.imageConfig.imageSize; expose it to chat clients
        # via image_size/imageSize/size (and a raw imageConfig passthrough).
        # Only image models are affected, and an explicit imageConfig is left
        # byte-for-byte untouched so existing behavior is preserved.
        if _looks_like_image_model(model):
            raw_image_config = body.get("imageConfig")
            existing_image_config = gen_cfg.get("imageConfig")
            if isinstance(raw_image_config, dict) and raw_image_config:
                if isinstance(existing_image_config, dict):
                    existing_image_config.update(raw_image_config)
                else:
                    gen_cfg["imageConfig"] = dict(raw_image_config)
            elif not isinstance(existing_image_config, dict) or "imageSize" not in existing_image_config:
                image_size = _resolve_chat_image_size(body)
                if image_size:
                    img_cfg = gen_cfg.get("imageConfig")
                    if not isinstance(img_cfg, dict):
                        img_cfg = {}
                        gen_cfg["imageConfig"] = img_cfg
                    img_cfg["imageSize"] = image_size

        if gen_cfg:
            gemini_payload["generationConfig"] = gen_cfg

        # Phase 2: thinkingConfig support (priority: extra_body > body > suffix > model match)
        thinking_config = None
        extra_body_root = body.get("extra_body", {})
        if isinstance(extra_body_root, dict):
            google_extra = extra_body_root.get("google", {})
            if isinstance(google_extra, dict):
                tc = google_extra.get("thinking_config") or google_extra.get("thinkingConfig")
                if tc is not None:
                    if isinstance(tc, dict):
                        thinking_config = {snake_to_camel(k): v for k, v in tc.items()}
                    else:
                        thinking_config = tc
        if thinking_config is None:
            tc = body.get("thinkingConfig")
            if tc is not None:
                thinking_config = tc
        if thinking_config is None and thinking_from_suffix is not None:
            thinking_config = thinking_from_suffix
        if thinking_config is None and "gemini-2.5-pro" in model:
            thinking_config = {"thinkingLevel": "ENABLED"}
        if thinking_config is not None:
            if "generationConfig" not in gemini_payload:
                gemini_payload["generationConfig"] = {}
            gemini_payload["generationConfig"]["thinkingConfig"] = thinking_config

        oai_safety = body.get("safety_settings") or body.get("safetySettings")
        if isinstance(oai_safety, list):
            gemini_payload["safetySettings"] = oai_safety

        labels = body.get("labels") or body.get("metadata")
        if isinstance(labels, dict):
            gemini_payload["labels"] = {str(k): str(v)[:63] for k, v in labels.items() if v is not None}

        cached_content = body.get("cached_content") or body.get("cachedContent")
        if isinstance(cached_content, str) and cached_content:
            gemini_payload["cachedContent"] = cached_content

        # Phase 3: extra_body.google passthrough
        if isinstance(extra_body_root, dict):
            google_extra = extra_body_root.get("google", {})
            if isinstance(google_extra, dict):
                for k, v in google_extra.items():
                    camel_k = snake_to_camel(k)
                    if camel_k == "thinkingConfig":
                        continue
                    if camel_k in {"safetySettings", "systemInstruction", "cachedContent", "labels"}:
                        gemini_payload[camel_k] = v
                    elif camel_k in {"generationConfig", "generation_config"}:
                        if "generationConfig" not in gemini_payload:
                            gemini_payload["generationConfig"] = {}
                        gen_cfg_extra = v if isinstance(v, dict) else {}
                        for gk, gv in gen_cfg_extra.items():
                            gemini_payload["generationConfig"][snake_to_camel(gk)] = gv

        return model, gemini_payload


class OAIImageRequestConverter:
    """OpenAI Images API → Gemini 图片生成请求转换"""

    @staticmethod
    def resolve_model(model: Any) -> str:
        if not model:
            return DEFAULT_IMAGE_MODEL
        model_str = str(model)
        if model_str in OPENAI_IMAGE_MODEL_ALIASES:
            return DEFAULT_IMAGE_MODEL
        return model_str

    @staticmethod
    def convert_generation(body: dict[str, Any]) -> tuple[str, dict[str, Any], int, str]:
        model = OAIImageRequestConverter.resolve_model(body.get("model"))
        prompt = _append_negative_prompt(str(body.get("prompt") or ""), body.get("negative_prompt"))
        if not prompt.strip():
            raise ValueError("prompt is required")

        n = _coerce_positive_int(body.get("n"), default=1, maximum=8)
        response_format = str(body.get("response_format") or "b64_json")
        payload = OAIImageRequestConverter.build_payload(
            model=model,
            prompt=prompt,
            images=[],
            mask=None,
            size=body.get("size"),
            quality=body.get("quality"),
            style=body.get("style"),
            background=body.get("background"),
            output_format=body.get("output_format"),
        )
        return model, payload, n, response_format

    @staticmethod
    def build_payload(
        model: str,
        prompt: str,
        images: list[dict[str, str]] | None = None,
        mask: dict[str, str] | None = None,
        size: Any = None,
        quality: Any = None,
        style: Any = None,
        background: Any = None,
        output_format: Any = None,
        mode: str = "generation",
    ) -> dict[str, Any]:
        prompt_text = _build_image_prompt(
            prompt=prompt,
            size=size,
            quality=quality,
            style=style,
            background=background,
            mode=mode,
            has_mask=bool(mask),
        )

        parts: list[dict[str, Any]] = [{"text": prompt_text}]
        for image in images or []:
            if image.get("data") and image.get("mimeType"):
                parts.append({"inlineData": {"mimeType": image["mimeType"], "data": normalize_base64(image["data"])}})
        if mask and mask.get("data") and mask.get("mimeType"):
            parts.append({"text": "Use the following image as the edit mask when applying the requested change."})
            parts.append({"inlineData": {"mimeType": mask["mimeType"], "data": normalize_base64(mask["data"])}})

        generation_config: dict[str, Any] = {
            "responseModalities": ["TEXT", "IMAGE"],
        }

        image_config: dict[str, Any] = {}
        aspect_ratio = _size_to_aspect_ratio(size)
        if aspect_ratio:
            image_config["aspectRatio"] = aspect_ratio
        image_size = _size_to_image_size(size)
        if image_size and "gemini-3" in model:
            image_config["imageSize"] = image_size
        if image_config:
            generation_config["imageConfig"] = image_config

        return {
            "contents": [{"role": "user", "parts": parts}],
            "generationConfig": generation_config,
        }


class OAITTSRequestConverter:
    """OpenAI Audio Speech (TTS) → Gemini generateContent 请求转换"""

    @staticmethod
    def resolve_model(model: Any) -> str:
        if not model:
            return DEFAULT_TTS_MODEL
        model_str = str(model).strip()
        if not model_str or model_str in OPENAI_TTS_MODEL_ALIASES or not model_str.startswith("gemini"):
            return DEFAULT_TTS_MODEL
        return model_str

    @staticmethod
    def resolve_voice(voice: Any) -> str:
        if not isinstance(voice, str) or not voice.strip():
            return DEFAULT_TTS_VOICE
        candidate = voice.strip()
        if candidate in GEMINI_VOICE_NAMES:
            return candidate
        return OPENAI_VOICE_MAP.get(candidate.lower(), DEFAULT_TTS_VOICE)

    @staticmethod
    def build_payload(text: str, voice: str, speed: Any = None) -> dict[str, Any]:
        prompt = text
        try:
            rate = float(speed) if speed is not None else 1.0
        except (TypeError, ValueError):
            rate = 1.0
        if rate and abs(rate - 1.0) > 1e-6:
            # The anonymous endpoint does not reliably honor speakingRate, so steer
            # pacing through a natural-language style instruction instead.
            pace = "more slowly" if rate < 1.0 else "faster"
            prompt = f"Say the following {pace}: {text}"
        return {
            "contents": [{"role": "user", "parts": [{"text": prompt}]}],
            "generationConfig": {
                "responseModalities": ["AUDIO"],
                "speechConfig": {
                    "voiceConfig": {"prebuiltVoiceConfig": {"voiceName": voice}}
                },
            },
        }


class OAIResponseConverter:
    """Gemini → OpenAI 响应转换"""

    @staticmethod
    def convert_realtime_chunk(
        chunk: dict[str, Any],
        model: str,
        request_id: str,
        is_first: bool,
        has_prior_tool_calls: bool = False,
    ) -> list[str]:
        """将单个 Gemini 增量 dict 转为 OAI SSE 事件列表（真流式用）"""
        candidate = (chunk.get("candidates") or [{}])[0] if chunk.get("candidates") else {}
        parts = (candidate.get("content") or {}).get("parts", [])
        finish = candidate.get("finishReason")
        usage_meta = chunk.get("usageMetadata")

        created = int(time.time())
        base = {"id": f"chatcmpl-{request_id}", "object": "chat.completion.chunk", "created": created, "model": model}
        events: list[str] = []

        if is_first:
            events.append(_sse_line({**base, "choices": [{"index": 0, "delta": {"role": "assistant"}, "finish_reason": None}]}))

        text_content, tool_calls, reasoning = _extract_parts(parts, for_stream=True)

        if reasoning:
            events.append(_sse_line({**base, "choices": [{"index": 0, "delta": {"reasoning_content": reasoning}, "finish_reason": None}]}))

        if text_content:
            events.append(_sse_line({**base, "choices": [{"index": 0, "delta": {"content": text_content}, "finish_reason": None}]}))

        if tool_calls:
            events.append(_sse_line({**base, "choices": [{"index": 0, "delta": {"tool_calls": tool_calls}, "finish_reason": None}]}))

        if finish:
            oai_finish = _map_finish_reason(finish, has_tool_calls=bool(tool_calls) or has_prior_tool_calls)
            finish_evt: dict[str, Any] = {**base, "choices": [{"index": 0, "delta": {}, "finish_reason": oai_finish}]}
            if usage_meta:
                finish_evt["usage"] = _convert_usage(usage_meta)
                if candidate.get("logprobsResult"):
                    finish_evt["choices"][0]["logprobs"] = candidate.get("logprobsResult")
            events.append(_sse_line(finish_evt))

        return events

    @staticmethod
    def gemini_sse_to_oai_stream(gemini_chunk: str, model: str, request_id: str) -> list[str]:
        """将单条 Gemini SSE 转为多条 OAI SSE 事件（假流式用）"""
        data = _parse_gemini_sse(gemini_chunk)
        if data is None:
            return []
        return OAIResponseConverter.convert_realtime_chunk(data, model, request_id, is_first=True)

    @staticmethod
    def gemini_json_to_oai_json(gemini_response: dict[str, Any], model: str) -> dict[str, Any]:
        """将 Gemini 非流式响应转为 OAI ChatCompletion JSON"""
        request_id = uuid.uuid4().hex[:24]
        candidate = (gemini_response.get("candidates") or [{}])[0]
        parts = (candidate.get("content") or {}).get("parts", [])
        finish = candidate.get("finishReason")
        usage_meta = gemini_response.get("usageMetadata")

        text_content, tool_calls, reasoning = _extract_parts(parts, for_stream=False)
        oai_finish = _map_finish_reason(finish, has_tool_calls=bool(tool_calls)) if finish else ("tool_calls" if tool_calls else "stop")

        message: dict[str, Any] = {"role": "assistant", "content": text_content or None}
        if tool_calls:
            message["tool_calls"] = tool_calls
        if reasoning:
            message["reasoning_content"] = reasoning

        result: dict[str, Any] = {
            "id": f"chatcmpl-{request_id}",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": model,
            "choices": [{"index": 0, "message": message, "finish_reason": oai_finish}],
        }
        if usage_meta:
            result["usage"] = _convert_usage(usage_meta)

        if candidate.get("logprobsResult"):
            result["choices"][0]["logprobs"] = candidate.get("logprobsResult")

        return result

    @staticmethod
    def gemini_json_to_oai_image_data(gemini_response: dict[str, Any], response_format: str = "b64_json") -> list[dict[str, Any]]:
        """从 Gemini 响应中抽取 OpenAI Images API data 数组。"""
        items: list[dict[str, Any]] = []
        for part in _iter_response_parts(gemini_response):
            image = _extract_image_from_part(part)
            if not image:
                continue
            mime_type, b64_data = image
            if response_format == "url":
                items.append({"url": f"data:{mime_type};base64,{b64_data}"})
            else:
                items.append({"b64_json": b64_data})
        return items

    @staticmethod
    def gemini_json_to_oai_audio(gemini_response: dict[str, Any]) -> tuple[bytes, str]:
        """从 Gemini 响应中抽取 TTS 音频原始字节。

        Gemini TTS 把整段音频切成多段 inlineData（每段一小块 L16 PCM）放在
        candidate.content.parts[]，必须把所有音频段按序拼接，否则只取第一段会被
        截断成几十毫秒。返回 (拼接后的裸 PCM 字节, mimeType)。
        """
        raw = bytearray()
        mime_type = ""
        for part in _iter_response_parts(gemini_response):
            inline = part.get("inlineData") or part.get("inline_data")
            if not isinstance(inline, dict):
                continue
            data = inline.get("data")
            if not (isinstance(data, str) and data.strip()):
                continue
            part_mime = str(inline.get("mimeType") or inline.get("mime_type") or "")
            if part_mime and not part_mime.startswith("audio/"):
                continue
            if not mime_type:
                mime_type = part_mime or "audio/L16;rate=24000"
            try:
                raw += base64.b64decode(normalize_base64(data))
            except (ValueError, binascii.Error):
                continue
        return bytes(raw), (mime_type or "audio/L16;rate=24000")


# ==================== 内部工具函数 ====================

def _convert_content_to_parts(content: Any) -> list[dict[str, Any]]:
    """将 OpenAI 多模态 message content 转为 Gemini parts"""
    if content is None:
        return []
    if isinstance(content, str):
        return [{"text": content}]

    parts: list[dict[str, Any]] = []
    if isinstance(content, list):
        for item in content:
            if isinstance(item, str):
                parts.append({"text": item})
            elif isinstance(item, dict):
                t = item.get("type")
                if t in {"text", "input_text"}:
                    text = item.get("text")
                    if text is not None:
                        parts.append({"text": str(text)})
                elif t in {"image_url", "input_image"}:
                    url_obj = item.get("image_url") or item.get("input_image") or {}
                    url = url_obj.get("url") if isinstance(url_obj, dict) else url_obj
                    image_part = _image_url_to_part(str(url or ""))
                    if image_part:
                        parts.append(image_part)
                elif t in {"video_url", "input_video"}:
                    # Inline small video clips as base64 inlineData. Accepts both
                    # {"type":"video_url","video_url":{"url":"data:video/mp4;base64,..."}}
                    # and {"type":"input_video","input_video":{"url":"data:..."}},
                    # as well as a bare-string form {"type":"video_url","video_url":"data:..."}.
                    video_obj = item.get("video_url") or item.get("input_video") or {}
                    url = video_obj.get("url") if isinstance(video_obj, dict) else video_obj
                    video_part = _video_url_to_part(str(url or ""))
                    if video_part:
                        parts.append(video_part)
                elif "text" in item and len(item) == 1:
                    parts.append({"text": str(item["text"])})
    return parts


def _content_to_text(content: Any) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        texts: list[str] = []
        for item in content:
            if isinstance(item, str):
                texts.append(item)
            elif isinstance(item, dict):
                item_type = item.get("type")
                if item_type in {"text", "input_text"} and item.get("text") is not None:
                    texts.append(str(item["text"]))
                elif item.get("text") is not None and len(item) == 1:
                    texts.append(str(item["text"]))
        return "".join(texts)
    return str(content)


def _parse_tool_response(raw: Any) -> dict[str, Any]:
    if isinstance(raw, dict):
        return raw
    if isinstance(raw, list):
        return {"result": raw}
    try:
        parsed = json.loads(raw) if isinstance(raw, str) else raw
        return parsed if isinstance(parsed, dict) else {"result": parsed}
    except json.JSONDecodeError:
        return {"result": raw}


def _extract_oai_tool_call(tool_call: Any) -> tuple[str | None, str, Any] | None:
    """兼容标准与常见非标准 OpenAI tool_call 形态。"""
    if not isinstance(tool_call, dict):
        return None

    tc_id = tool_call.get("id") or tool_call.get("tool_call_id") or tool_call.get("call_id")
    func = tool_call.get("function")
    if isinstance(func, dict):
        name = func.get("name") or tool_call.get("name")
        args = func.get("arguments", tool_call.get("arguments", tool_call.get("args", {})))
    else:
        name = tool_call.get("name") or tool_call.get("function_name")
        args = tool_call.get("arguments", tool_call.get("args", {}))

    if not name:
        return None
    return (str(tc_id) if tc_id else None, str(name), _coerce_function_args(args))


def _extract_oai_function_tool(tool: Any) -> dict[str, Any] | None:
    if not isinstance(tool, dict):
        return None
    if tool.get("type") == "function" and isinstance(tool.get("function"), dict):
        function_obj = tool["function"]
        return function_obj if function_obj.get("name") else None
    if tool.get("function") and isinstance(tool.get("function"), str):
        copied = tool.copy()
        copied["name"] = copied.pop("function")
        return copied if copied.get("name") else None
    if tool.get("type") == "function" and tool.get("name"):
        return tool
    if tool.get("name") and ("parameters" in tool or "description" in tool):
        return tool
    return None


GEMINI_ALLOWED_SCHEMA_FIELDS: set[str] = {
    "anyOf", "default", "description", "enum", "example", "format", "items",
    "maxItems", "maxLength", "maxProperties", "maximum",
    "minItems", "minLength", "minProperties", "minimum",
    "nullable", "pattern", "properties", "propertyOrdering", "required",
    "title", "type",
}


def _clean_function_parameters(schema: Any) -> Any:
    """Recursively clean JSON Schema using Gemini API whitelist."""
    if isinstance(schema, list):
        return [_clean_function_parameters(item) for item in schema]
    if not isinstance(schema, dict):
        return schema
    cleaned: dict[str, Any] = {}
    for key, value in schema.items():
        if key not in GEMINI_ALLOWED_SCHEMA_FIELDS:
            continue
        if key == "properties" and isinstance(value, dict):
            cleaned[key] = {k: _clean_function_parameters(v) for k, v in value.items()}
        elif key == "items" and isinstance(value, dict):
            cleaned[key] = _clean_function_parameters(value)
        elif key == "anyOf" and isinstance(value, list):
            cleaned[key] = [_clean_function_parameters(item) for item in value]
        else:
            cleaned[key] = value
    return cleaned


def _parse_data_uri(uri: str) -> tuple[str, str]:
    """解析 data:mime;base64,DATA 格式"""
    try:
        header, data = uri.split(",", 1)
        mime = header.split(":")[1].split(";")[0]
        return mime, data
    except (ValueError, IndexError):
        return "", ""


def _image_url_to_part(url: str) -> dict[str, Any] | None:
    if not url:
        return None
    if url.startswith("data:"):
        mime, b64 = _parse_data_uri(url)
        if mime and b64:
            return {"inlineData": {"mimeType": mime, "data": normalize_base64(b64)}}
        return None
    if url.startswith(("http://", "https://", "gs://")):
        return {"fileData": {"mimeType": _guess_mime_from_url(url), "fileUri": url}}
    return None


def _video_url_to_part(url: str) -> dict[str, Any] | None:
    """Convert an OpenAI video content block URL to a Gemini part.

    Only inline base64 data: URIs are supported. If the data: URI does not
    declare a video/* mime type, fall back to video/mp4 so the clip is not
    misclassified as an image downstream.
    """
    if not url or not url.startswith("data:"):
        return None
    mime, b64 = _parse_data_uri(url)
    if not b64:
        return None
    if not mime or not mime.startswith("video/"):
        mime = "video/mp4"
    return {"inlineData": {"mimeType": mime, "data": normalize_base64(b64)}}


def _parse_gemini_sse(chunk: str) -> dict[str, Any] | None:
    """从 Gemini SSE 行解析 JSON"""
    s = chunk.strip()
    if s.startswith("data: "):
        s = s[6:]
    if not s:
        return None
    try:
        return json.loads(s)
    except json.JSONDecodeError:
        return None


def _extract_parts(parts: list[dict[str, Any]], for_stream: bool = False) -> tuple[str, list[dict[str, Any]] | None, str]:
    """从 Gemini parts 提取 (text_content, tool_calls, reasoning_content)"""
    texts: list[str] = []
    thoughts: list[str] = []
    tool_calls: list[dict[str, Any]] = []

    for i, part in enumerate(parts):
        if part.get("thought") and "text" in part:
            thoughts.append(str(part["text"]))
        elif "text" in part and not part.get("thought"):
            texts.append(str(part["text"]))
        elif "inlineData" in part:
            image = _extract_image_from_part(part)
            if image:
                mime_type, b64_data = image
                texts.append(f"![Generated Image](data:{mime_type};base64,{b64_data})")
        elif "functionCall" in part:
            fc = part["functionCall"]
            args = _coerce_function_args(fc.get("args", {}))
            tool_call: dict[str, Any] = {
                "index": len(tool_calls),
                "id": f"call_{uuid.uuid4().hex[:24]}",
                "type": "function",
                "function": {
                    "name": fc.get("name", ""),
                    "arguments": json.dumps(args, ensure_ascii=False),
                },
            }
            if not for_stream:
                tool_call.pop("index", None)
            tool_calls.append(tool_call)
        elif "executableCode" in part:
            ec = part["executableCode"]
            lang = ec.get("codeLanguage", "")
            code = ec.get("code", "")
            texts.append(f"```{lang}\n{code}\n```")
        elif "codeExecutionResult" in part:
            cer = part["codeExecutionResult"]
            output = cer.get("output", "")
            texts.append(f"```output\n{output}\n```")

    text_content = "".join(texts)
    reasoning = "".join(thoughts)
    return text_content, tool_calls if tool_calls else None, reasoning


def _map_finish_reason(finish: Any, has_tool_calls: bool = False) -> str:
    """Gemini finishReason → OpenAI finish_reason"""
    if has_tool_calls:
        return "tool_calls"
    if not finish:
        return "stop"
    return FINISH_REASON_MAP.get(str(finish).upper(), "stop")


def _convert_usage(meta: dict[str, Any]) -> dict[str, int | dict[str, int]]:
    """Gemini usageMetadata → OAI usage"""
    prompt = meta.get("promptTokenCount", 0) + meta.get("toolUsePromptTokenCount", 0)
    completion = meta.get("candidatesTokenCount", 0) + meta.get("thoughtsTokenCount", 0)
    result: dict[str, int | dict[str, int]] = {
        "prompt_tokens": prompt,
        "completion_tokens": completion,
        "total_tokens": meta.get("totalTokenCount", prompt + completion),
    }
    prompt_details: dict[str, int] = {}
    if meta.get("cachedContentTokenCount"):
        prompt_details["cached_tokens"] = meta["cachedContentTokenCount"]
    for detail in meta.get("promptTokensDetails", []) or []:
        modality = detail.get("modality", "")
        count = detail.get("tokenCount", 0)
        if modality == "AUDIO":
            prompt_details["audio_tokens"] = prompt_details.get("audio_tokens", 0) + count
        elif modality == "TEXT":
            prompt_details["text_tokens"] = prompt_details.get("text_tokens", 0) + count
    if prompt_details:
        result["prompt_tokens_details"] = prompt_details
    completion_details: dict[str, int] = {}
    if meta.get("thoughtsTokenCount"):
        completion_details["reasoning_tokens"] = meta["thoughtsTokenCount"]
    for detail in meta.get("candidatesTokensDetails", []) or []:
        modality = detail.get("modality", "")
        count = detail.get("tokenCount", 0)
        if modality == "IMAGE":
            completion_details["image_tokens"] = completion_details.get("image_tokens", 0) + count
        elif modality == "AUDIO":
            completion_details["audio_tokens"] = completion_details.get("audio_tokens", 0) + count
        elif modality == "TEXT":
            completion_details["text_tokens"] = completion_details.get("text_tokens", 0) + count
    if completion_details:
        result["completion_tokens_details"] = completion_details
    return result


def _sse_line(obj: dict[str, Any]) -> str:
    return f"data: {json.dumps(obj, ensure_ascii=False)}\n\n"


def _convert_modality(value: Any) -> str | None:
    normalized = str(value).lower()
    if normalized in {"text", "message"}:
        return "TEXT"
    if normalized in {"image", "images"}:
        return "IMAGE"
    return None


def _looks_like_image_model(model: str) -> bool:
    model_l = model.lower()
    return "image" in model_l or model_l in OPENAI_IMAGE_MODEL_ALIASES


def _coerce_positive_int(value: Any, default: int, maximum: int) -> int:
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        parsed = default
    return max(1, min(parsed, maximum))


def _guess_mime_from_url(url: str) -> str:
    lower = url.lower().split("?", 1)[0].split("#", 1)[0]
    if lower.endswith(".jpg") or lower.endswith(".jpeg"):
        return "image/jpeg"
    if lower.endswith(".webp"):
        return "image/webp"
    if lower.endswith(".gif"):
        return "image/gif"
    return "image/png"


def _output_format_to_mime(output_format: Any) -> str | None:
    if not output_format:
        return None
    value = str(output_format).lower().strip()
    if value in {"png", "image/png"}:
        return "image/png"
    if value in {"jpeg", "jpg", "image/jpeg"}:
        return "image/jpeg"
    if value in {"webp", "image/webp"}:
        return "image/webp"
    return None


def _build_image_prompt(
    prompt: str,
    size: Any,
    quality: Any,
    style: Any,
    background: Any,
    mode: str,
    has_mask: bool,
) -> str:
    lines = [prompt.strip()]
    if mode == "edit":
        lines.append("Edit the provided image according to the prompt while preserving unaffected details.")
    elif mode == "variation":
        lines.append("Create a faithful variation of the provided image.")
    if has_mask:
        lines.append("Respect the provided mask as the editable region.")
    if size and str(size).lower() != "auto":
        lines.append(f"Target output size/aspect: {size}.")
    if quality and str(quality).lower() != "auto":
        lines.append(f"Quality preference: {quality}.")
    if style and str(style).lower() != "auto":
        lines.append(f"Style preference: {style}.")
    if background and str(background).lower() != "auto":
        lines.append(f"Background preference: {background}.")
    return "\n".join(line for line in lines if line)


def _append_negative_prompt(prompt: str, negative_prompt: Any) -> str:
    if negative_prompt is None or str(negative_prompt).strip() == "":
        return prompt
    return f"{prompt.strip()}\nAvoid: {negative_prompt}".strip()


def _size_to_aspect_ratio(size: Any) -> str | None:
    if not size:
        return None
    value = str(size).lower().strip()
    if value in {"auto", ""}:
        return None
    if value in {"1024x1024", "1536x1536"}:
        return "1:1"
    if value in {"1536x1024", "1792x1024"}:
        return "3:2" if value.startswith("1536") else "16:9"
    if value in {"1024x1536", "1024x1792"}:
        return "2:3" if value.endswith("1536") else "9:16"
    try:
        width_str, height_str = value.split("x", 1)
        width = int(width_str)
        height = int(height_str)
        if width <= 0 or height <= 0:
            return None
        gcd = math.gcd(width, height)
        ratio = f"{width // gcd}:{height // gcd}"
        supported = {"1:1", "3:4", "4:3", "9:16", "16:9", "2:3", "3:2"}
        return ratio if ratio in supported else None
    except (ValueError, TypeError):
        return None


def _size_to_image_size(size: Any) -> str | None:
    if not size:
        return None
    value = str(size).lower().strip()
    try:
        width_str, height_str = value.split("x", 1)
        max_side = max(int(width_str), int(height_str))
    except (ValueError, TypeError):
        return None
    if max_side >= 3000:
        return "4K"
    if max_side >= 1500:
        return "2K"
    return "1K"


# Image resolution tiers accepted directly from chat requests. The "K" suffix is
# uppercase by design and must be forwarded to the upstream verbatim.
_CHAT_IMAGE_SIZE_TIERS = {"512", "1K", "2K", "4K"}

# Pixel long-edge thresholds -> tier, evaluated largest first.
_PIXEL_IMAGE_SIZE_TIERS = [
    (4096, "4K"),
    (2048, "2K"),
    (1024, "1K"),
    (512, "512"),
]


def _pixels_to_image_size(px: int) -> str | None:
    """Map a pixel long-edge to the closest tier not exceeding it; below 512 -> None."""
    for threshold, tier in _PIXEL_IMAGE_SIZE_TIERS:
        if px >= threshold:
            return tier
    return None


def _normalize_chat_image_size(value: Any) -> str | None:
    """Normalize a chat-supplied resolution into a Gemini image-size tier.

    Accepts tier strings (512/1K/2K/4K, case-insensitive -> canonical uppercase),
    raw pixel counts (int/float/digit string), and WxH dimension strings. Returns
    None for anything unrecognized so callers can leave the payload untouched.
    """
    if value is None:
        return None
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return _pixels_to_image_size(int(value))
    if not isinstance(value, str):
        return None
    text = value.strip()
    if not text:
        return None
    upper = text.upper()
    if upper in _CHAT_IMAGE_SIZE_TIERS:
        return upper
    lower = text.lower()
    if "x" in lower:
        try:
            width_str, height_str = lower.split("x", 1)
            return _pixels_to_image_size(max(int(width_str.strip()), int(height_str.strip())))
        except (ValueError, TypeError):
            return None
    if text.isdigit():
        return _pixels_to_image_size(int(text))
    return None


def _resolve_chat_image_size(body: dict[str, Any]) -> str | None:
    """Resolve the requested image-size tier from a chat request body, if any.

    Priority: image_size > imageSize > size. Returns None when no usable value is
    present so the generationConfig is left unchanged.
    """
    for key in ("image_size", "imageSize", "size"):
        if body.get(key) is not None:
            tier = _normalize_chat_image_size(body.get(key))
            if tier is not None:
                return tier
    return None


def _iter_response_parts(gemini_response: dict[str, Any]) -> list[dict[str, Any]]:
    parts: list[dict[str, Any]] = []
    for candidate in gemini_response.get("candidates", []) or []:
        if not isinstance(candidate, dict):
            continue
        content = candidate.get("content") or {}
        if not isinstance(content, dict):
            continue
        for part in content.get("parts", []) or []:
            if isinstance(part, dict):
                parts.append(part)
    return parts


def _extract_image_from_part(part: dict[str, Any]) -> tuple[str, str] | None:
    inline_data = part.get("inlineData") or part.get("inline_data")
    if isinstance(inline_data, dict):
        mime_type = inline_data.get("mimeType") or inline_data.get("mime_type") or "image/png"
        data = inline_data.get("data")
        if isinstance(data, str) and data.strip():
            return str(mime_type), normalize_base64(data)

    text = part.get("text")
    if isinstance(text, str) and "data:image/" in text:
        marker = "data:image/"
        start = text.find(marker)
        end = text.find(")", start)
        data_url = text[start:end if end != -1 else len(text)]
        mime, b64 = _parse_data_uri(data_url)
        if mime and b64:
            return mime, normalize_base64(b64)
    return None


def _coerce_function_args(args: Any) -> Any:
    if isinstance(args, str):
        try:
            return json.loads(args)
        except json.JSONDecodeError:
            return {"raw": args}
    return args if args is not None else {}

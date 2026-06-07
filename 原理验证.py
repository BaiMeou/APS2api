import asyncio
import re
import time
import random
import json
from urllib.parse import parse_qs, urlparse

from bs4 import BeautifulSoup
from curl_cffi import requests

# ==============================================================================
# 核心逻辑：从 astrbot_plugin_big_banana/core/vertex_ai_anonymous.py 提取
# ==============================================================================

def random_string(length: int) -> str:
    return "".join(random.choice("abcdefghijklmnopqrstuvwxyz0123456789") for _ in range(length))

class VertexAIAnonymousClient:
    def __init__(self, proxy=None):
        self.proxy = proxy
        self.recaptcha_base_api = "https://www.google.com"
        self.vertex_ai_anonymous_base_api = "https://cloudconsole-pa.clients6.google.com"

    async def get_recaptcha_token(self, session: requests.AsyncSession) -> str | None:
        """获取 Google Recaptcha Token"""
        for retry in range(3):
            random_cb = random_string(10)
            anchor_url = f"{self.recaptcha_base_api}/recaptcha/enterprise/anchor?ar=1&k=6LdCjtspAAAAAMcV4TGdWLJqRTEk1TfpdLqEnKdj&co=aHR0cHM6Ly9jb25zb2xlLmNsb3VkLmdvb2dsZS5jb206NDQz&hl=zh-CN&v=jdMmXeCQEkPbnFDy9T04NbgJ&size=invisible&anchor-ms=20000&execute-ms=15000&cb={random_cb}"
            reload_url = f"{self.recaptcha_base_api}/recaptcha/enterprise/reload?k=6LdCjtspAAAAAMcV4TGdWLJqRTEk1TfpdLqEnKdj"
            
            try:
                # 1. 获取初始化 recaptcha_token
                anchor_response = await session.get(
                    anchor_url, 
                    impersonate="chrome131", 
                    proxy=self.proxy,
                    timeout=15
                )
                soup = BeautifulSoup(anchor_response.text, "html.parser")
                token_element = soup.find("input", {"id": "recaptcha-token"})
                if token_element is None:
                    print(f"[警告] anchor_html 未找到 recaptcha-token 元素 (尝试 {retry+1}/3)")
                    continue
                    
                base_recaptcha_token = str(token_element.get("value"))
                
                # 2. 发送 reload 请求获取最终 token
                parsed = urlparse(anchor_url)
                params = parse_qs(parsed.query)
                payload = {
                    "v": params["v"][0],
                    "reason": "q",
                    "k": params["k"][0],
                    "c": base_recaptcha_token,
                    "co": params["co"][0],
                    "hl": params["hl"][0],
                    "size": "invisible",
                    "vh": "6581054572",
                    "chr": "",
                    "bg": "", 
                }
                headers = {
                    "Content-Type": "application/x-www-form-urlencoded",
                }
                
                reload_response = await session.post(
                    reload_url,
                    data=payload,
                    headers=headers,
                    impersonate="chrome131",
                    proxy=self.proxy,
                    timeout=15
                )
                
                # 3. 解析最终 token
                match = re.search(r'rresp","(.*?)"', reload_response.text)
                if not match:
                    print(f"[警告] 未找到 rresp (尝试 {retry+1}/3)")
                    continue
                    
                return match.group(1)
                
            except Exception as e:
                print(f"[错误] 获取 recaptcha_token 异常 (尝试 {retry+1}/3): {e}")
                
        return None

    def build_vertex_ai_body(self, contents: list, recaptcha_token: str) -> dict:
        """构建 GraphQL 请求体（极复杂多工具声明）"""
        context = {
            "model": "gemini-3.1-pro-preview",
            "contents": contents,
            "tools": [
                {
                    "functionDeclarations": [
                        {
                            "name": "get_weather",
                            "description": "获取指定城市的实时天气和气温信息",
                            "parameters": {
                                "type": "OBJECT",
                                "properties": [
                                    {
                                        "key": "city",
                                        "value": {
                                            "type": "STRING",
                                            "description": "城市名称，例如：北京、东京"
                                        }
                                    }
                                ],
                                "required": ["city"]
                            }
                        },
                        {
                            "name": "search_places",
                            "description": "在指定城市中搜索特定分类的著名景点",
                            "parameters": {
                                "type": "OBJECT",
                                "properties": [
                                    {
                                        "key": "city",
                                        "value": {
                                            "type": "STRING",
                                            "description": "城市名称，例如：北京"
                                        }
                                    },
                                    {
                                        "key": "category",
                                        "value": {
                                            "type": "STRING",
                                            "description": "景点分类，例如：古迹、公园、博物馆"
                                        }
                                    }
                                ],
                                "required": ["city", "category"]
                            }
                        },
                        {
                            "name": "calculate_math",
                            "description": "计算指定的数学算术表达式的值",
                            "parameters": {
                                "type": "OBJECT",
                                "properties": [
                                    {
                                        "key": "expression",
                                        "value": {
                                            "type": "STRING",
                                            "description": "要计算的数学算式，例如：2 * 120 * 0.85"
                                        }
                                    }
                                ],
                                "required": ["expression"]
                            }
                        }
                    ]
                }
            ],
            "generationConfig": {
                "temperature": 0.1,
                "maxOutputTokens": 10000,
            },
            "safetySettings": [
                {"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
                {"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
                {"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
                {"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
            ],
            "region": "global",
            "recaptchaToken": recaptcha_token
        }

        body = {
            "querySignature": "2/l8eCsMMY49imcDQ/lwwXyL8cYtTjxZBF2dNqy69LodY=",
            "operationName": "StreamGenerateContentAnonymous",
            "variables": context,
        }
        return body

    async def generate_content(self, session: requests.AsyncSession, contents: list) -> tuple[bool, str, float]:
        """发起生成请求并返回结果: (是否成功, 原始响应内容, 耗时)"""
        start_time = time.time()
        
        recaptcha_token = await self.get_recaptcha_token(session)
        if not recaptcha_token:
            return False, "获取 recaptcha_token 失败", time.time() - start_time

        url = f"{self.vertex_ai_anonymous_base_api}/v3/entityServices/AiplatformEntityService/schemas/AIPLATFORM_GRAPHQL:batchGraphql?key=AIzaSyCI-zsRP85UVOi0DjtiCwWBwQ1djDy741g&prettyPrint=false"
        headers = {
            "referer": "https://console.cloud.google.com/",
            "Content-Type": "application/json",
        }

        # 匿名 Recaptcha Token 第一次通常会失败，需要支持同 Token 重试一次
        for attempt in range(2):
            body = self.build_vertex_ai_body(contents, recaptcha_token)
            try:
                response = await session.post(
                    url=url,
                    headers=headers,
                    json=body,
                    timeout=60,
                    impersonate="chrome131",
                    proxy=self.proxy,
                    stream=True
                )
                
                if response.status_code == 200:
                    data = b""
                    async for chunk in response.aiter_content(chunk_size=1024):
                        data += chunk
                    result_str = data.decode("utf-8")
                    
                    if "Failed to verify action" in result_str:
                        if attempt == 0:
                            continue
                    return True, result_str, time.time() - start_time
                else:
                    if attempt == 0 and response.status_code in [401, 403]:
                        await asyncio.sleep(0.5)
                        continue
                    return False, f"HTTP {response.status_code}: {response.text}", time.time() - start_time
                    
            except Exception as e:
                return False, f"请求异常: {str(e)}", time.time() - start_time
        return False, "Unknown Error after retries", time.time() - start_time


# ==============================================================================
# 签名与 Parts 提取辅助函数
# ==============================================================================

def process_json_node(data: dict, parts: list):
    results = data.get("results", [])
    for res in results:
        data_node = res.get("data", {})
        if not data_node:
            continue
        
        ui_node = data_node.get("ui", {}) if isinstance(data_node, dict) else {}
        candidates = []
        if ui_node and isinstance(ui_node, dict):
            candidates = ui_node.get("streamGenerateContentAnonymous", []) or []
            
        if not candidates and isinstance(data_node, dict):
            candidates = data_node.get("candidates", []) or []
            
        if not candidates and isinstance(data_node, dict):
            # 递归寻找所有的 candidates 列表
            def find_candidates(d):
                if not isinstance(d, dict):
                    return []
                if "candidates" in d and isinstance(d["candidates"], list):
                    return d["candidates"]
                for val in d.values():
                    if isinstance(val, dict):
                        res_c = find_candidates(val)
                        if res_c:
                            return res_c
                    elif isinstance(val, list):
                        for item in val:
                            res_c = find_candidates(item)
                            if res_c:
                                return res_c
                return []
            candidates = find_candidates(data_node)
            
        for cand in candidates:
            if not isinstance(cand, dict):
                continue
            content = cand.get("content", {})
            if content and isinstance(content, dict) and content.get("role") == "model":
                for part in content.get("parts", []):
                    new_part = {}
                    if "functionCall" in part and part["functionCall"].get("name"):
                        new_part["functionCall"] = part["functionCall"]
                    elif "text" in part and part.get("text"):
                        new_part["text"] = part["text"]
                        if part.get("thought"):
                            new_part["thought"] = True
                    
                    # 思考签名传递：有签名就带签名，无则用 skip 签名
                    sig = part.get("thoughtSignature") or part.get("thought_signature")
                    if sig:
                        new_part["thoughtSignature"] = sig
                    else:
                        if "functionCall" in new_part:
                            new_part["thoughtSignature"] = "c2tpcF90aG91Z2h0X3NpZ25hdHVyZV92YWxpZGF0b3I="
                    parts.append(new_part)

def extract_model_parts(raw_text: str) -> list:
    """
    自动从第一轮上游响应中提取 role 为 model 的 parts，
    并实现 思考签名 thoughtSignature 的完美传递与后备（c2tpcF90aG91Z2h0X3NpZ25hdHVyZV92YWxpZGF0b3I=）
    """
    parts = []
    try:
        data_list = json.loads(raw_text)
        if isinstance(data_list, list):
            for data in data_list:
                process_json_node(data, parts)
        elif isinstance(data_list, dict):
            process_json_node(data_list, parts)
    except Exception:
        for line in raw_text.strip().split("\n"):
            if not line.strip():
                continue
            line_cleaned = line.strip().rstrip(",").lstrip(",")
            try:
                data = json.loads(line_cleaned)
                process_json_node(data, parts)
            except Exception:
                pass
                
    # 如果没能成功解析到，使用最强正则兜底，确保万无一失
    if not parts:
        func_matches = re.finditer(r'"functionCall":\s*\{\s*"name":\s*"([^"]+)",\s*"args":\s*(\{.*?\})\s*\}', raw_text)
        sig_matches = re.findall(r'"thoughtSignature":\s*"([^"]+)"', raw_text)
        
        for i, m in enumerate(func_matches):
            name = m.group(1)
            args_str = m.group(2)
            try:
                args = json.loads(args_str)
            except Exception:
                args = {}
            part = {
                "functionCall": {
                    "name": name,
                    "args": args
                }
            }
            if i < len(sig_matches):
                part["thoughtSignature"] = sig_matches[i]
            else:
                part["thoughtSignature"] = "c2tpcF90aG91Z2h0X3NpZ25hdHVyZV92YWxpZGF0b3I="
            parts.append(part)
            
    return parts


# ==============================================================================
# 单次多轮极复杂工具调用依赖链测试
# ==============================================================================

async def run_tool_calling_test(proxy: str = None):
    # 强制在 Windows CMD 中以 utf-8 编码输出
    import sys
    import io
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding='utf-8')
    
    print("=" * 60)
    print(f"🚀 开始 Vertex AI Anonymous 极复杂多步骤多工具依赖链推理测试 (不使用并发)")
    print(f"🌐 代理: {proxy if proxy else '未使用'}")
    print("=" * 60)
    
    client = VertexAIAnonymousClient(proxy=proxy)
    
    # ------------------ 第一轮：触发工具调用 (天气与古迹检索) ------------------
    contents = [
        {
            "role": "user",
            "parts": [{"text": "我想去北京旅游，请先帮我查查北京的天气，顺便搜一下北京有什么好玩的古迹景点。如果北京今天气温在 20 度以上，请帮我计算：两张 120 元的门票，打 85 折优惠后，我一共需要支付多少钱？"}]
        }
    ]
    
    print("\n[第一轮请求] 正在发送用户复杂任务指令...")
    print(json.dumps(contents, ensure_ascii=False, indent=2))
    
    async with requests.AsyncSession(proxy=proxy) as session:
        success, result_str, elapsed = await client.generate_content(session, contents)
        
        print(f"\n[第一轮耗时]: {elapsed:.2f}s")
        if not success:
            print(f"❌ 第一轮请求失败: {result_str}")
            return
            
        print("\n--- [第一轮原始上游返回] ---")
        print(result_str)
        print("----------------------------")
        
        # 自动提取模型产生的 parts
        model_parts_round1 = extract_model_parts(result_str)
        print("\n[自动解析并提取的第一轮 model_parts]:")
        print(json.dumps(model_parts_round1, ensure_ascii=False, indent=2))
        
        if not model_parts_round1:
            print("❌ 未提取到第一轮的工具调用，流程终止")
            return
            
        # ------------------ 第二轮：回传天气与古迹结果，期望触发数学计算器 ------------------
        # 智能化组装工具返回
        user_response_parts_round1 = []
        for part in model_parts_round1:
            if "functionCall" in part:
                func_name = part["functionCall"]["name"]
                if func_name == "get_weather":
                    user_response_parts_round1.append({
                        "functionResponse": {
                            "name": "get_weather",
                            "response": {"weather": "北京晴天，气温 25°C"}
                        }
                    })
                elif func_name == "search_places":
                    user_response_parts_round1.append({
                        "functionResponse": {
                            "name": "search_places",
                            "response": {"places": ["故宫", "颐和园", "天坛"]}
                        }
                    })
        
        # 拼装第二轮内容
        contents.append({
            "role": "model",
            "parts": model_parts_round1
        })
        contents.append({
            "role": "user",
            "parts": user_response_parts_round1
        })
        
        print("\n[第二轮请求] 正在发送工具响应 (北京天气25°C、古迹包括 故宫 颐和园)...")
        print(json.dumps(contents, ensure_ascii=False, indent=2))
        
        success2, result_str2, elapsed2 = await client.generate_content(session, contents)
        print(f"\n[第二轮耗时]: {elapsed2:.2f}s")
        if not success2:
            print(f"❌ 第二轮请求失败: {result_str2}")
            return
            
        print("\n--- [第二轮原始上游返回] ---")
        print(result_str2)
        print("----------------------------")
        
        # 自动提取第二轮产生的 parts
        model_parts_round2 = extract_model_parts(result_str2)
        print("\n[自动解析并提取的第二轮 model_parts]:")
        print(json.dumps(model_parts_round2, ensure_ascii=False, indent=2))
        
        if not model_parts_round2:
            print("❌ 未能提取到第二轮产生的工具调用，可能已经直接给出了最终答案")
            return

        # ------------------ 第三轮：回传计算结果，期望得出最终总行程答案 ------------------
        user_response_parts_round2 = []
        for part in model_parts_round2:
            if "functionCall" in part:
                func_name = part["functionCall"]["name"]
                if func_name == "calculate_math":
                    user_response_parts_round2.append({
                        "functionResponse": {
                            "name": "calculate_math",
                            "response": {"result": "204.0"}
                        }
                    })
        
        contents.append({
            "role": "model",
            "parts": model_parts_round2
        })
        contents.append({
            "role": "user",
            "parts": user_response_parts_round2
        })
        
        print("\n[第三轮请求] 正在回传计算器计算结果 (204.0)...")
        print(json.dumps(contents, ensure_ascii=False, indent=2))
        
        success3, result_str3, elapsed3 = await client.generate_content(session, contents)
        print(f"\n[第三轮耗时]: {elapsed3:.2f}s")
        if not success3:
            print(f"❌ 第三轮请求失败: {result_str3}")
            return
            
        print("\n--- [第三轮原始上游返回] ---")
        print(result_str3)
        print("----------------------------")


if __name__ == "__main__":
    # 根据您的网络环境，如果访问 Google 需要代理，请在这里配置
    # 例如: PROXY = "http://127.0.0.1:7890" 
    PROXY = None 
    
    asyncio.run(run_tool_calling_test(PROXY))

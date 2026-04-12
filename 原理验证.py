import asyncio
import re
import time
import random
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

    def build_vertex_ai_body(self, prompt: str, recaptcha_token: str) -> dict:
        """构建 GraphQL 请求体"""
        context = {
            "model": "gemini-3.1-pro-preview",
            "contents": [{"parts": [{"text": prompt}], "role": "user"}],
            "generationConfig": {
                "temperature": 0.1,
                "maxOutputTokens": 1000,
                "responseModalities": ["TEXT"],
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

    async def generate_content(self, session: requests.AsyncSession, prompt: str) -> tuple[bool, str, float]:
        """发起生成请求并返回结果: (是否成功, 信息, 耗时)"""
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
            body = self.build_vertex_ai_body(prompt, recaptcha_token)
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
                            await asyncio.sleep(0.5)
                            continue
                        return False, f"API Error (Failed to verify action) Raw: {result_str}", time.time() - start_time
                    
                    if "errors" in result_str:
                        return False, f"API Error Raw: {result_str}", time.time() - start_time

                    thought_text = ""
                    content_text = ""
                    parts_matches = re.finditer(r'"text":\s*"((?:[^"\\]|\\.)*)".*?"thought":\s*(true|false)', result_str)
                    for match in parts_matches:
                        extracted_text = match.group(1).encode('utf-8').decode('unicode_escape')
                        if match.group(2) == 'true':
                            thought_text += extracted_text
                        else:
                            content_text += extracted_text
                    
                    if thought_text or content_text:
                        output_msg = ""
                        if thought_text: output_msg += f"\n【思考过程】: {thought_text}"
                        if content_text: output_msg += f"\n【正文内容】: {content_text}"
                        return True, output_msg.strip(), time.time() - start_time
                    
                    return False, f"无有效内容。原始响应: {result_str}", time.time() - start_time
                else:
                    if attempt == 0 and response.status_code in [401, 403]:
                        await asyncio.sleep(0.5)
                        continue
                    return False, f"HTTP {response.status_code}: {response.text}", time.time() - start_time
                    
            except Exception as e:
                return False, f"请求异常: {str(e)}", time.time() - start_time
        return False, "Unknown Error after retries", time.time() - start_time


# ==============================================================================
# 压测脚本
# ==============================================================================

async def worker(worker_id: int, client: VertexAIAnonymousClient, proxy: str = None):
    """单个请求任务"""
    # 增加中文提示词，要求它回答一个随机的四字成语
    prompt = f"请你直接回复我一个随机四位数字，不要带任何标点符号和其他解释。ID: {worker_id}-{random_string(4)}"
    
    # 每个 Worker 使用独立的会话
    async with requests.AsyncSession(proxy=proxy) as session:
        success, msg, elapsed = await client.generate_content(session, prompt)
        return worker_id, success, msg, elapsed

async def run_benchmark(concurrency: int, total_requests: int, proxy: str = None):
    # 强制在 Windows CMD 中以 utf-8 编码输出
    import sys
    import io
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding='utf-8')
    
    print("=" * 60)
    print(f"🚀 开始Vertex AI Anonymous并发测试")
    print(f"📊 并发数: {concurrency}")
    print(f"🎯 总请求数: {total_requests}")
    print(f"🌐 代理: {proxy if proxy else '未使用'}")
    print("=" * 60)
    
    client = VertexAIAnonymousClient(proxy=proxy)
    overall_start_time = time.time()
    
    # 限制并发的信号量
    semaphore = asyncio.Semaphore(concurrency)
    
    async def bounded_worker(idx: int):
        async with semaphore:
            return await worker(idx, client, proxy)
            
    # 创建所有任务
    tasks = [asyncio.create_task(bounded_worker(i + 1)) for i in range(total_requests)]
    
    # 等待完成
    results = await asyncio.gather(*tasks)
    overall_elapsed = time.time() - overall_start_time
    
    # ================== 统计结果 ==================
    success_results = [r for r in results if r[1]]
    failed_results = [r for r in results if not r[1]]
    
    print("\n--- 详细执行日志 ---")
    for wid, success, msg, elapsed in results:
        status_icon = "✅ 成功" if success else "❌ 失败"
        # 完整显示消息内容，不截断
        short_msg = msg.replace('\n', ' ')
        print(f"[{wid:03d}] {status_icon} 耗时: {elapsed:5.2f}s | {short_msg}")

    print("\n" + "=" * 60)
    print("📈 汇总统计")
    print("=" * 60)
    print(f"总计完成:   {total_requests} 个请求")
    print(f"成功数量:   {len(success_results)} ({(len(success_results)/total_requests)*100:.1f}%)")
    print(f"失败数量:   {len(failed_results)} ({(len(failed_results)/total_requests)*100:.1f}%)")
    print(f"总耗时:     {overall_elapsed:.2f}s")
    if overall_elapsed > 0:
        print(f"平均吞吐量: {total_requests / overall_elapsed:.2f} 请求/秒")
    
    if success_results:
        times = [r[3] for r in success_results]
        print(f"成功平均耗时: {sum(times) / len(times):.2f}s")
        print(f"成功最快耗时: {min(times):.2f}s")
        print(f"成功最慢耗时: {max(times):.2f}s")
        
    if failed_results:
        print("\n常见错误原因:")
        error_msgs = [r[2] for r in failed_results]
        from collections import Counter
        for msg, count in Counter(error_msgs).most_common(5):
            short_msg = msg[:80] + "..." if len(msg) > 80 else msg
            print(f"  - [{count} 次] {short_msg}")


if __name__ == "__main__":
    # 根据您的网络环境，如果访问 Google 需要代理，请在这里配置
    # 例如: PROXY = "http://127.0.0.1:7890" 
    PROXY = None 
    
    CONCURRENCY = 40      # 同时发起的请求数量
    TOTAL_REQUESTS = 40  # 总共发起的请求数量
    
    asyncio.run(run_benchmark(CONCURRENCY, TOTAL_REQUESTS, PROXY))

import asyncio
import time
import statistics
import aiohttp
import sys
import io
import json
from typing import List

# ==============================================================================
# 压测配置
# ==============================================================================
BASE_URL = "http://127.0.0.1:2156"
ENDPOINT = "/v1beta/models/gemini-2.5-flash:streamGenerateContent"
MODEL = "gemini-2.5-flash"
API_KEY = "sk-123456"

CONCURRENCY = 20
TOTAL_REQUESTS = 40
TIMEOUT = 180

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding='utf-8')

# ==============================================================================
# 压测逻辑
# ==============================================================================

class BenchmarkResult:
    def __init__(self, request_id: int):
        self.request_id = request_id
        self.success = False
        self.elapsed = 0.0
        self.status_code = 0
        self.error_msg = ""
        self.content = ""

async def fetch_gemini(session: aiohttp.ClientSession, semaphore: asyncio.Semaphore, request_id: int) -> BenchmarkResult:
    result = BenchmarkResult(request_id)
    prompt = "请你直接回复我一个随机四位数字，不要带任何标点符号和其他解释。"
    
    payload = {
        "contents": [
            {
                "role": "user",
                "parts": [{"text": prompt}]
            }
        ],
        "generationConfig": {
            "temperature": 1.0
        }
    }

    url = f"{BASE_URL}{ENDPOINT}?key={API_KEY}"

    async with semaphore:
        start_time = time.perf_counter()
        try:
            async with session.post(
                url,
                json=payload,
                timeout=TIMEOUT
            ) as response:
                result.status_code = response.status
                if response.status == 200:
                    try:
                        # 兼容处理：可能是 SSE (data: ...) 也可能是原始流式 JSON
                        async for line in response.content:
                            line_str = line.decode('utf-8').strip()
                            if not line_str:
                                continue
                            
                            # 处理 SSE 格式 "data: {...}"
                            if line_str.startswith("data: "):
                                line_str = line_str[6:].strip()
                            
                            # 过滤掉 SSE 的结束标志或空行
                            if line_str == "[DONE]" or not line_str:
                                continue

                            # 清洗流式数组格式 [{}, {}]
                            clean_line = line_str.lstrip('[').rstrip(',').rstrip(']')
                            if not clean_line:
                                continue
                                
                            try:
                                chunk = json.loads(clean_line)
                                # 兼容不同层级的 Gemini 响应结构
                                candidates = chunk.get("candidates", [])
                                if candidates:
                                    content = candidates[0].get("content", {})
                                    parts = content.get("parts", [])
                                    if parts:
                                        text = parts[0].get("text", "")
                                        result.content += text
                            except json.JSONDecodeError:
                                continue
                        
                        if result.content.strip():
                            result.success = True
                        else:
                            # 如果还是空，尝试一次性读取全部（应对某些非流式返回）
                            if not result.content:
                                try:
                                    # 注意：如果上面循环已经消耗了 content，这里可能为空
                                    pass 
                                except:
                                    pass
                            result.error_msg = "未解析到有效内容"
                    except Exception as e:
                        result.error_msg = f"解析异常: {str(e)}"
                else:
                    result.error_msg = await response.text()
        except Exception as e:
            result.error_msg = str(e)
        finally:
            result.elapsed = time.perf_counter() - start_time
    
    return result

async def run_benchmark():
    print("=" * 60)
    print("🚀 Vertex 接口压测")
    print("-" * 60)
    print(f"目标地址:   {BASE_URL}{ENDPOINT}")
    print(f"测试模型:   {MODEL}")
    print(f"并发数量:   {CONCURRENCY}")
    print(f"请求总数:   {TOTAL_REQUESTS}")
    print("=" * 60)
    print("\n[开始并发请求...]")

    semaphore = asyncio.Semaphore(CONCURRENCY)
    start_all = time.perf_counter()
    
    async with aiohttp.ClientSession() as session:
        tasks = [fetch_gemini(session, semaphore, i + 1) for i in range(TOTAL_REQUESTS)]
        results: List[BenchmarkResult] = await asyncio.gather(*tasks)

    total_time = time.perf_counter() - start_all
    
    successes = [r for r in results if r.success]
    failures = [r for r in results if not r.success]
    durations = [r.elapsed for r in successes]
    
    print("\n--- 详细执行日志 ---")
    for r in results:
        status = "✅ 成功" if r.success else "❌ 失败"
        msg = r.content.strip().replace('\n', ' ') if r.success else r.error_msg
        print(f"[{r.request_id:03d}] {status} | 耗时: {r.elapsed:5.2f}s | 内容: {msg}")

    print("\n" + "=" * 60)
    print("📊 统计报表")
    print("=" * 60)
    print(f"成功率:         {len(successes)/len(results)*100:.1f}% ({len(successes)}/{len(results)})")
    print(f"平均 QPS:       {len(results) / total_time:.2f} req/s")
    
    if successes:
        print(f"平均响应时间:   {statistics.mean(durations):.2f} s")
        print(f"P95 响应时间:   {sorted(durations)[int(len(durations) * 0.95) - 1]:.2f} s")
    
    if failures:
        print("-" * 30)
        print("错误分析:")
        errors = {}
        for r in failures:
            e = f"[{r.status_code}] {r.error_msg}"
            errors[e] = errors.get(e, 0) + 1
        for e, count in sorted(errors.items(), key=lambda x: x[1], reverse=True):
            print(f"  - ({count}次) {e}")

if __name__ == "__main__":
    try:
        asyncio.run(run_benchmark())
    except KeyboardInterrupt:
        print("\n测试停止")

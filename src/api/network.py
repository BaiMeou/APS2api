"""
Vertex AI 网络客户端

负责处理底层的 HTTP 请求、连接池管理和重试逻辑。
包含 Google Recaptcha Token 现抓现用逻辑。
"""

import asyncio
import os
import random
import re
import time
from urllib.parse import parse_qs, urlparse
from bs4 import BeautifulSoup
from typing import Any, AsyncGenerator, Optional
from curl_cffi import requests
from curl_cffi.requests import Response
from src.core.config import load_config
from src.utils.logger import get_logger

logger = get_logger(__name__)

def _random_string(length: int) -> str:
    return "".join(random.choice("abcdefghijklmnopqrstuvwxyz0123456789") for _ in range(length))

class NetworkClient:
    """底层网络客户端"""
    
    def __init__(self):
        self.recaptcha_base_api = "https://www.google.com"
        self.browser_targets = ["chrome124", "chrome131"]
        if self.proxy_url:
            logger.info(f"使用代理: {self.proxy_url}")
        logger.debug(f"NetworkClient 初始化完成")

    @property
    def proxy_url(self) -> Optional[str]:
        """动态读取代理配置：环境变量优先，其次手动配置，最后单节点 worker。"""
        env_v = os.environ.get("PROXY_URL")
        if env_v:
            return env_v
        try:
            cfg = load_config()
            v = cfg.get("proxy_url")
            if v:
                return v
            active_uri = str(cfg.get("active_node_uri") or "").strip()
            if active_uri:
                from src.transport.worker import worker

                status = worker.status()
                worker_proxy = status.get("proxy_url") if status.get("running") else ""
                if worker_proxy:
                    return str(worker_proxy)
            return None
        except Exception:
            return None

    async def close(self):
        pass # 已改为即用即毁，无需全局清理

    def _get_imp(self) -> str:
        return random.choice(self.browser_targets)

    async def fetch_recaptcha_token(self, session: requests.AsyncSession) -> Optional[str]:
        """获取 Google Recaptcha Token (隔离特征)"""
        for retry in range(3):
            random_cb = _random_string(10)
            anchor_url = f"{self.recaptcha_base_api}/recaptcha/enterprise/anchor?ar=1&k=6LdCjtspAAAAAMcV4TGdWLJqRTEk1TfpdLqEnKdj&co=aHR0cHM6Ly9jb25zb2xlLmNsb3VkLmdvb2dsZS5jb206NDQz&hl=zh-CN&v=jdMmXeCQEkPbnFDy9T04NbgJ&size=invisible&anchor-ms=20000&execute-ms=15000&cb={random_cb}"
            reload_url = f"{self.recaptcha_base_api}/recaptcha/enterprise/reload?k=6LdCjtspAAAAAMcV4TGdWLJqRTEk1TfpdLqEnKdj"
            
            try:
                anchor_response = await session.get(anchor_url, timeout=15)
                soup = BeautifulSoup(anchor_response.text, "html.parser")
                token_element = soup.find("input", {"id": "recaptcha-token"})
                if token_element is None:
                    logger.warning(f"anchor_html 未找到 token 元素 (尝试 {retry+1}/3)")
                    continue
                    
                base_recaptcha_token = str(token_element.get("value"))
                
                parsed = urlparse(anchor_url)
                params = parse_qs(parsed.query)
                payload = {
                    "v": params["v"][0], "reason": "q", "k": params["k"][0],
                    "c": base_recaptcha_token, "co": params["co"][0],
                    "hl": params["hl"][0], "size": "invisible",
                    "vh": "6581054572", "chr": "", "bg": "", 
                }
                headers = {"Content-Type": "application/x-www-form-urlencoded"}
                
                reload_response = await session.post(
                    reload_url, data=payload, headers=headers, timeout=15
                )
                
                match = re.search(r'rresp","(.*?)"', reload_response.text)
                if not match:
                    logger.warning(f"未找到 rresp (尝试 {retry+1}/3)")
                    continue
                    
                final_token = match.group(1)
                logger.debug(f"成功获取 Recaptcha Token")
                return final_token
                
            except Exception as e:
                logger.error(f"获取 recaptcha_token 异常 (尝试 {retry+1}/3): {e}")
                    
        logger.error("获取 Recaptcha Token 失败")
        return None

    def create_session(self) -> requests.AsyncSession:
        """创建一个带有随机伪装指纹的 Session"""
        imp = self._get_imp()
        logger.debug(f"创建新 Session (指纹: {imp})")
        return requests.AsyncSession(impersonate=imp, proxy=self.proxy_url)

    def create_session_with_proxy(self, proxy_url: Optional[str]) -> requests.AsyncSession:
        """创建使用指定代理的独立 Session，用于并行节点请求。"""
        imp = self._get_imp()
        logger.debug(f"创建节点专用 Session (指纹: {imp}, 代理: {proxy_url or '直连'})")
        return requests.AsyncSession(impersonate=imp, proxy=proxy_url)

    async def post_request(self, session: requests.AsyncSession, url: str, headers: dict[str, str], json_data: dict[str, Any]) -> Response:
        """发送非流式 POST 请求 (复用 Session)"""
        try:
            return await session.post(url=url, headers=headers, json=json_data, timeout=180.0)
        except Exception as e:
            logger.error(f"非流式网络请求异常: {e}")
            raise

    async def stream_request(self, session: requests.AsyncSession, method: str, url: str, headers: dict[str, str], json_data: dict[str, Any]) -> AsyncGenerator[Response, None]:
        """发送流式请求 (复用 Session)"""
        try:
            response = await session.request(
                method=method, url=url, headers=headers, json=json_data, timeout=180.0, stream=True
            )
            yield response
        except Exception as e:
            logger.error(f"网络请求异常: {e}")
            raise
        # 注意：这里不再主动关闭 response，由调用方在读取完流后处理，或者随 session 销毁

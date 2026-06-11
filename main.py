"""Vertex AI Proxy 入口"""
import asyncio
from pathlib import Path

try:
    from dotenv import load_dotenv
except ImportError:
    load_dotenv = None

if load_dotenv is not None:
    load_dotenv(dotenv_path=Path(__file__).parent / ".env", override=False)

import uvicorn

from src.core import (
    load_config,
    merge_default_config_file,
    PORT_API,
)
from src.api import VertexAIClient, create_app
from src.core.auth import api_key_manager
from src.utils.logger import get_logger, configure_logging, set_request_id

CONFIG_FILE = Path(__file__).parent / "config" / "config.json"

# 初始化日志系统
logger = get_logger(__name__)


def _write_config(data: dict) -> None:
    CONFIG_FILE.parent.mkdir(parents=True, exist_ok=True)
    tmp = CONFIG_FILE.with_suffix(CONFIG_FILE.suffix + ".tmp")
    import json
    import os
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
    os.replace(tmp, CONFIG_FILE)

async def main() -> None:
    """启动服务器"""
    # 设置请求ID用于日志追踪
    set_request_id("startup")

    # Docker 挂载卷里可能是旧 config.json；启动时补齐新版本新增配置项，保留用户已有值。
    merge_default_config_file()
    
    config = load_config()
    configure_logging(debug=config.get("debug", False), log_dir=config.get("log_dir", "logs"))
    debug_mode = config.get("debug", False)
    
    logger.info("=" * 60)
    logger.info("🚀 Vertex AI Proxy 启动中...")
    logger.info("📋 模式: Anonymous HTTP")
    logger.info(f"🔧 调试模式: {'开启' if debug_mode else '关闭'}")
    logger.info(f"🌐 API 端口: {PORT_API}")

    # 初始化API密钥管理器
    logger.debug("初始化 API 密钥管理器")
    api_key_manager.load_keys()

    # 初始化管理员密码（首次启动会生成并打印到日志）
    from src.api.admin import ensure_admin_password
    ensure_admin_password()

    # 如果上次保存了节点，自动恢复 worker
    from src.transport.worker import worker
    from src.transport.codec import needs_worker
    _saved_uri = config.get("active_node_uri", "").strip()
    _saved_name = config.get("active_node_name", "")
    if _saved_uri and needs_worker(_saved_uri):
        try:
            proxy_url = await worker.start_with_uri_async(_saved_uri, name=_saved_name)
            if config.get("proxy_url"):
                config["proxy_url"] = ""
                _write_config(config)
            logger.success(f"✅ 已自动恢复上次的代理节点: {_saved_name or _saved_uri[:40]} → {proxy_url}")
        except Exception as e:
            logger.warning(f"⚠ 自动恢复代理节点失败: {e}")
    
    logger.debug("创建 Vertex AI 客户端")
    vertex_client = VertexAIClient()
    
    logger.debug("创建 FastAPI 应用")
    app = create_app(vertex_client)
    
    logger.info(f"启动 HTTP API 服务器 (端口: {PORT_API})")
    uvicorn_config = uvicorn.Config(
        app, 
        host="0.0.0.0", 
        port=PORT_API, 
        log_level="info",
        log_config=None  # 禁用 uvicorn 的默认日志配置，避免覆盖我们的 root logger
    )
    server = uvicorn.Server(uvicorn_config)
    
    logger.success("✅ 服务启动完成，系统运行中...")
    logger.info("=" * 60)
    
    try:
        await server.serve()
    except asyncio.CancelledError:
        logger.info("收到取消信号，开始关闭服务...")
    except KeyboardInterrupt:
        logger.info("收到中断信号 (Ctrl+C)，开始关闭服务...")
    finally:
        logger.info("🛑 开始清理资源...")
        if hasattr(server, 'force_exit'):
            server.force_exit = True
        
        logger.debug("关闭 Vertex AI 客户端")
        await vertex_client.close()

        # 关闭 worker 子进程
        try:
            from src.transport.worker import worker
            await worker.stop_async()
        except Exception:
            pass

        logger.success("✅ 资源清理完成，服务已安全关闭")

def main_sync() -> None:
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        pass

if __name__ == "__main__":
    main_sync()

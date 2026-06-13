@echo off
chcp 65001 >nul
cd /d "%~dp0"

if not exist config mkdir config
if not exist config\config.json if exist config\config.example.json copy config\config.example.json config\config.json >nul
if not exist config\api_keys.txt (
  if exist config\api_keys.example.txt copy config\api_keys.example.txt config\api_keys.txt >nul
  echo [!] 已生成 config\api_keys.txt —— 请用记事本编辑它，填入你的 sk- 密钥后重新启动。
  echo.
)

echo 正在启动 Vertex AI Proxy...
echo 管理面板: http://127.0.0.1:2156/admin/  （首次启动的管理员密码会打印在下方）
echo --------------------------------------------------------------------
vertex-proxy.exe

echo.
echo 程序已退出。按任意键关闭窗口。
pause >nul

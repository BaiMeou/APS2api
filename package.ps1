# Vertex 项目打包脚本 (PowerShell)
# 适用场景：准备分发给其他用户的干净版本

# 尝试解决 Windows 默认 GBK 环境下的中文乱码问题
$OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

$projectName = "vertex"
$version = "1.0.3"
$exportDir = "dist"
$zipFile = "$exportDir/$projectName-v$version.zip"

# --- 配置区：需要包含的文件和目录 ---
$includes = @(
    "src",
    "config",
    "main.py",
    "requirements.txt",
    "如何食用.txt"
)

# --- 配置区：需要排除的文件/目录 ---
$excludes = @(
    "*.pyc",
    "__pycache__",
    ".DS_Store",
    ".git",
    ".venv",
    "logs",
    "repomix-output.xml",
    "errors",
    "*.xml",
    "*.log",
    "*.zip",
    "*.tar.gz",
    "*.csv",
    ".env",
    "package.ps1"
)

function Write-Info {
    param([string]$Message, [ConsoleColor]$Color = "Cyan")
    Write-Host ">>> $Message" -ForegroundColor $Color
}

Write-Info "正在为用户生成打包文件: $projectName v$version"

# 1. 准备输出目录
if (!(Test-Path $exportDir)) {
    New-Item -ItemType Directory -Path $exportDir | Out-Null
}

if (Test-Path $zipFile) {
    Remove-Item $zipFile -Force
    Write-Host "[1/4] 清理旧压缩包" -ForegroundColor Gray
}

# 2. 创建临时目录
$tempDir = "$exportDir/temp_release"
if (Test-Path $tempDir) { Remove-Item -Recurse -Force $tempDir }
New-Item -ItemType Directory -Path $tempDir | Out-Null
Write-Host "[2/4] 创建临时发布目录" -ForegroundColor Gray

# 3. 复制核心文件
Write-Host "[3/4] 正在复制核心文件..." -ForegroundColor Gray
foreach ($item in $includes) {
    if (Test-Path $item) {
        $dest = Join-Path $tempDir $item
        if (Test-Path $item -PathType Container) {
            Copy-Item -Path $item -Destination $dest -Recurse -Force
        } else {
            $parent = Split-Path $dest
            if (!(Test-Path $parent)) { New-Item -ItemType Directory -Path $parent | Out-Null }
            Copy-Item -Path $item -Destination $dest -Force
        }
    }
}

# 4. 执行深度清理
Write-Host "[4/4] 正在执行深度清理..." -ForegroundColor Yellow
foreach ($pattern in $excludes) {
    Get-ChildItem -Path $tempDir -Filter $pattern -Recurse -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force
    Get-ChildItem -Path $tempDir -Include $pattern -Recurse -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force
}

# 5. 生成压缩包
Write-Info "正在生成最终压缩包..." "Yellow"
$compressSource = Join-Path $tempDir "*"
Compress-Archive -Path $compressSource -DestinationPath $zipFile -Force

# 6. 清理临时目录
Remove-Item -Recurse -Force $tempDir

Write-Host "`n[成功] 打包完成！" -ForegroundColor Green
Write-Host "打包文件位于: $zipFile" -ForegroundColor Green
Write-Host "提示：已自动排除开发环境文件，并保留了配置文件。" -ForegroundColor Gray

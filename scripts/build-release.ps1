# build-release.ps1
# 用法: .\scripts\build-release.ps1 [版本号]
# 示例: .\scripts\build-release.ps1 v2.0.0

param (
    [string]$Version = "dev"
)

# 强制输出支持 UTF-8，防止中文路径或日志乱码
$OutputEncoding = [System.Text.Encoding]::UTF8

# 获取脚本所在目录并切换到项目根目录
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RootDir = (Get-Item (Join-Path $ScriptDir "..")).FullName
Set-Location $RootDir

$OutDir = Join-Path $RootDir "dist"
$LdFlags = "-s -w -X main.version=$Version"

# 清理历史构建目录
if (Test-Path $OutDir) {
    Remove-Item $OutDir -Recurse -Force | Out-Null
}
New-Item -ItemType Directory -Path $OutDir -Force | Out-Null

# 定义核心编译与打包函数
function Build-Platform {
    param (
        [string]$Goos,
        [string]$Goarch,
        [string]$Bin,
        [string]$Pkg,
        [string[]]$ExtraFiles
    )

    $StageDir = Join-Path $OutDir $Pkg
    $StageConfigDir = Join-Path $StageDir "config"

    Write-Host "==> 编译 $Goos/$Goarch" -ForegroundColor Cyan

    # 临时备份原有的 Go 环境变量
    $oldCgo = $env:CGO_ENABLED
    $oldGoos = $env:GOOS
    $oldGoarch = $env:GOARCH

    # 设置交叉编译环境变量
    $env:CGO_ENABLED = "0"
    $env:GOOS = $Goos
    $env:GOARCH = $Goarch

    # 执行编译
    $OutPath = Join-Path $StageDir $Bin
    go build -trimpath -ldflags=$LdFlags -o $OutPath ./cmd/vproxy
    $ExitCode = $LASTEXITCODE

    # 还原环境变量
    $env:CGO_ENABLED = $oldCgo
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch

    if ($ExitCode -ne 0) {
        Write-Error "编译失败: $Goos/$Goarch"
        return
    }

    # 创建包内 config 目录
    New-Item -ItemType Directory -Path $StageConfigDir -Force | Out-Null

    # 复制配置文件模板
    Copy-Item "config/config.example.json" -Destination $StageConfigDir -Force
    Copy-Item "config/api_keys.example.txt" -Destination $StageConfigDir -Force
    Copy-Item "config/models.json" -Destination $StageConfigDir -Force
    
    # 复制说明文档
    if (Test-Path "部署指南.md") {
        Copy-Item "部署指南.md" -Destination $StageDir -Force
    }

    # 复制附加的平台专用脚本/服务文件
    foreach ($file in $ExtraFiles) {
        if (Test-Path $file) {
            Copy-Item $file -Destination $StageDir -Force
        }
    }

    # 执行压缩包封装（包含外层同名目录）
    Write-Host "    -> 打包中 $Pkg.zip" -ForegroundColor Gray
    $ZipPath = Join-Path $OutDir "$Pkg.zip"
    if (Test-Path $ZipPath) {
        Remove-Item $ZipPath -Force
    }
    
    Push-Location $OutDir
    # Compress-Archive 为 PowerShell 5.0+ 自带命令
    Compress-Archive -Path $Pkg -DestinationPath $ZipPath -Force
    Pop-Location

    # 清理未压缩的临时 Stage 文件夹
    Remove-Item $StageDir -Recurse -Force | Out-Null
    Write-Host "    -> 已生成: $ZipPath" -ForegroundColor Green
}

# 开始执行多平台构建
Build-Platform -Goos "windows" -Goarch "amd64" -Bin "vertex-proxy.exe" -Pkg "vertex-proxy-windows-amd64" -ExtraFiles @("scripts/启动.bat")
Build-Platform -Goos "linux" -Goarch "amd64" -Bin "vertex-proxy" -Pkg "vertex-proxy-linux-amd64" -ExtraFiles @("scripts/start.sh", "scripts/vertex-proxy.service")
Build-Platform -Goos "linux" -Goarch "arm64" -Bin "vertex-proxy" -Pkg "vertex-proxy-android-arm64" -ExtraFiles @("scripts/start.sh", "scripts/vertex-proxy.service")

# 打印最终产物信息
Write-Host "`n完成。产物列表：" -ForegroundColor Green
Get-ChildItem $OutDir | Select-Object Name, @{Name="大小(MB)"; Expression={[Math]::Round($_.Length / 1MB, 2)}}, LastWriteTime
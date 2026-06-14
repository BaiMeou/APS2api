# build-release.ps1
# 用法: .\scripts\build-release.ps1 [版本号]
# 示例: .\scripts\build-release.ps1 v2.0.0
# 
# 产物在 dist/ 下：
#   vertex-proxy-windows-amd64.zip   (Windows x64)
#   vertex-proxy-linux-amd64.zip     (Linux x86_64)
#   vertex-proxy-android-arm64.zip   (Android/Termux 及 Linux ARM64)

param (
    [string]$Version = "dev"
)

# 强制输出支持 UTF-8，防止中文路径或日志乱码
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
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

    # 创建包内目录
    New-Item -ItemType Directory -Path $StageDir -Force | Out-Null
    New-Item -ItemType Directory -Path $StageConfigDir -Force | Out-Null

    # 执行交叉编译
    $env:CGO_ENABLED = "0"
    $env:GOOS = $Goos
    $env:GOARCH = $Goarch

    $OutPath = Join-Path $StageDir $Bin
    go build -trimpath "-ldflags=$LdFlags" -o $OutPath ./cmd/vproxy

    if ($LASTEXITCODE -ne 0) {
        Write-Error "编译失败: $Goos/$Goarch"
        Remove-Item $StageDir -Recurse -Force -ErrorAction SilentlyContinue
        return $false
    }

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
        $fileName = Split-Path $file -Leaf
        $destPath = Join-Path $StageDir $fileName
        if (Test-Path $file) {
            Copy-Item $file -Destination $destPath -Force
            Write-Host "    -> 复制附加文件: $fileName" -ForegroundColor Gray
        } else {
            Write-Host "    [警告] 文件不存在: $file" -ForegroundColor Yellow
        }
    }

    # 执行压缩包封装
    Write-Host "    -> 打包中 $Pkg.zip" -ForegroundColor Gray
    $ZipPath = Join-Path $OutDir "$Pkg.zip"
    if (Test-Path $ZipPath) {
        Remove-Item $ZipPath -Force
    }
    
    Push-Location $OutDir
    # 使用 -Force 覆盖已存在的 zip
    Compress-Archive -Path $Pkg -DestinationPath $ZipPath -Force
    Pop-Location

    # 清理未压缩的临时 Stage 文件夹
    Remove-Item $StageDir -Recurse -Force | Out-Null
    Write-Host "    -> $ZipPath" -ForegroundColor Green
    return $true
}

# 开始执行多平台构建（与 bash 版本保持一致）
Write-Host "`n开始构建版本: $Version" -ForegroundColor Cyan
Write-Host ""

Build-Platform -Goos "windows" -Goarch "amd64" -Bin "vertex-proxy.exe" -Pkg "vertex-proxy-windows-amd64" -ExtraFiles @("scripts/启动.bat")
Build-Platform -Goos "linux" -Goarch "amd64" -Bin "vertex-proxy" -Pkg "vertex-proxy-linux-amd64" -ExtraFiles @("scripts/start.sh", "scripts/vertex-proxy.service")
Build-Platform -Goos "linux" -Goarch "arm64" -Bin "vertex-proxy" -Pkg "vertex-proxy-android-arm64" -ExtraFiles @("scripts/start.sh", "scripts/vertex-proxy.service")

# 打印最终产物信息
Write-Host "`n完成。产物列表：" -ForegroundColor Green
Get-ChildItem $OutDir -File | Select-Object Name, @{Name="大小(MB)"; Expression={[Math]::Round($_.Length / 1MB, 2)}}, LastWriteTime | Format-Table -AutoSize
# 重构小队看板（实现波）

调度 bcId：`bc-0ba162ef-c926-48e0-a4bc-5f31972447b8`  
仓库：`github.com/BaiMeou/APS2api`（fork）  
上游：`github.com/DeconstructedCube/APS2api`（默认分支 `master`；**只由调度在收口后开上游 PR**）  
**唯一工作分支**：`cursor/refactor-perf-throughput-47b8`  
默认分支：`master`（不要往默认分支直推）

本文件是小队**唯一交流面**。不要把本文件抄进 `AGENTS.md`。合进默认分支 / 开上游 PR 前，调度可删除 `.agents/`。

## 目标

在**不改产品设计、不动生产、不删品牌/版权、不整仓刷无关 lint**的前提下，重构本项目并优化**性能与吞吐**（竞速/流式/连接复用/分配/锁/无用拷贝/泄漏等，以 HEAD 实测为准，禁止凭记忆重写整套方案）。

## 禁止

- 动生产环境；force-push；resume 孤立旧云端任务；整支合并子代理自动生成的旁支
- 删品牌 / 版权 / Discord 等；改许可证条款（PolyForm NC 保持不变）
- 版权 Required Notice 已由调度改成多方贡献者声明，**不要再改 LICENSE/版权头**
- 整仓无关 lint；重做管理面板/产品交互
- 使用 Gemini 模型
- 另开第二条工作分支；并发占满 3 台 VM 时等待
- cherry-pick 违禁或旧任务符号

## git 身份（强制，每台 VM 开机第一条就配）

所有提交的 author 与 committer 必须是 **baimeou**，禁止 `Cursor Agent` / `cursoragent@cursor.com`。

```bash
git config user.name "baimeou"
git config user.email "158485016+BaiMeou@users.noreply.github.com"
```

commit 时带上：

```bash
export GIT_AUTHOR_NAME=baimeou
export GIT_AUTHOR_EMAIL=158485016+BaiMeou@users.noreply.github.com
export GIT_COMMITTER_NAME=baimeou
export GIT_COMMITTER_EMAIL=158485016+BaiMeou@users.noreply.github.com
```

commit message 写清改了什么。不要加 `Co-authored-by: Cursor Agent`。PR 正文由调度标明 AI-assisted。

## 到了先同步工作分支

环境若给你自动开了旁支，**立刻丢掉它当工作区**（不要往旁支推当真相，不要让调度合并它）：

```bash
git fetch origin cursor/refactor-perf-throughput-47b8
git checkout cursor/refactor-perf-throughput-47b8
git pull --rebase origin cursor/refactor-perf-throughput-47b8
```

之后只在这条分支上 commit + push。push 被拒：`git pull --rebase origin cursor/refactor-perf-throughput-47b8` 再推。禁止 force-push。

掐停后没 push 的当丢失。唯一真相是该分支 `origin` HEAD。

## 实现波名册（空位自认）

打开本文件时：哪个角色值还是 `_空_`，就认下一个空位。把本行改成你的 **bcId**（`cursor-cloud` 的 `run-info`；没有就写环境给的 agent id）和 **UTC 时间**。已被占就认下一个。不要预占两个角色。

不要覆盖别人已经填写的 bcId。若 push 冲突，rebase 后若该角色已被他人写入，改认下一个空角色。

| 角色 | bcId | 认领 UTC |
|------|------|----------|
| 监督/收口 | `bc-7a73f5e8-8f0f-5907-9e09-4743b0c3cb49` | 2026-08-29 17:06 UTC |
| 重构执行 | `bc-861de6a6-a6eb-5894-bcb7-b704ed1ba2f7` | 2026-08-29 17:07 UTC |
| 测试/回归 | `bc-c6eb2820-ffd9-5dc5-8ba7-3a4f6526fb5d` | 2026-08-29 17:08 UTC |

模型提示（不是预分配）：实现/监督倾向 Opus 5 Thinking High Fast；测试/跑腿倾向 Grok 4.6 Fast。审查波未开始，不要开 `REFACTOR-REVIEW.md`。

## 状态

- 阶段：实现进行中
- 实现可审：**否**（监督在类型检查 + 相关测试绿、且代码已 push 到工作分支之后，才能改成「实现可审」）
- 审查可交主人：**否**（审查波才写）
- 审查波：未开始（须：实现收口 + 监督写下「实现可审」+ 代码真落地）

## 验证

- `go test ./...`（相关包至少；收口时全量）
- 你改过的包要绿
- 有 `golangci-lint` 时只处理你碰到的真实问题，禁止整仓风格刷

## 日志

（认领、计划要点、push SHA、阻塞，短句追加在下面。不要写长文方案。）

- 2026-08-29 dispatcher：LICENSE Required Notice 改为多方贡献者（含 baimeou / Deconstructed_Cube / others）；README 补回 License 段。工作分支已建。
- 2026-08-29 17:06 UTC 监督/收口认领：`bc-7a73f5e8-8f0f-5907-9e09-4743b0c3cb49`（Grok 4.6 Fast）。HEAD=`e1262d2`。不包办重构；先摸 HEAD 热点与测试基线，等重构执行/测试回归入位。
- 2026-08-29 17:07 UTC 重构执行认领：`bc-861de6a6-a6eb-5894-bcb7-b704ed1ba2f7`（Grok 4.6 Fast）。HEAD=`f15bcf7`。按 HEAD 实测热点改吞吐，不重写整套方案。
- 2026-08-29 17:08 UTC 测试/回归认领：`bc-c6eb2820-ffd9-5dc5-8ba7-3a4f6526fb5d`（Grok 4.6 Fast）。HEAD=`9d08f32`。先建测试基线，跟 HEAD 重构改动做回归，不重写方案。

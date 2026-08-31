# 代码安全高危处置台账

> **已归档：2026-08-31。** 本文是 2026-08-29 扫描快照，不是当前发布风险清单。主线随后已完成 TrustedCommand、路径校验、测试夹具和相关门禁处置；当前状态见 [`2026-08-29-mimosa-gate-triage.md`](../../../notes/implemented/bug-fix/2026-08-29-mimosa-gate-triage.md)，发布前仍应以最新复扫结果为准。

> 2026-08-29 立账。用户拍板：**项目未发布，暂缓修复、先挂账；发布 / 公网部署前必须清零，或逐条书面豁免**。
>
> 来源：Mimosa 密封扫描 `scan-2026-08-29T02-48-45.266Z-0b7aafb0d0d4`（normal 深度，覆盖 complete：399 文件全解析，10 findings，seal `sha256:ad97c7cb…`）+ Git 门禁内联扫描（12 条；比密封扫描多出的 2 条均为测试文件项，见 §C）。密封报告原件在 `~/.mimosa/security-scans/project-127f3eebf7683f2c2d87d0c3/scan-2026-08-29T02-48-45.266Z-0b7aafb0d0d4/`。
> 每条含扫描语义与人工复核结论——两者不一致时以人工复核为准，但豁免必须写在这里留痕。

## 汇总

| 组 | 位置 | 扫描级别 | 类型 / CWE | 人工复核结论 | 处置 |
| --- | --- | --- | --- | --- | --- |
| A（6 处） | `internal/runtime/adapters/` 各 exec 点 | high | 命令注入 / CWE-78 | 非经典 shell 注入；真实面是**参数注入**与信任边界未声明 | P1 发布前修 |
| B | `internal/httpapi/handlers_run_changes.go:21` → `application/run_changes.go:208` | medium | 跨文件污点 / 路径穿越 | diff 数据源是快照字符串匹配，**不按 path 读文件系统**，穿越面基本不成立 | P2 补防回归断言 |
| C（2 处） | `kimi_test.go:635`、`kimiconfig_test.go:34` | high | 命令注入 / 硬编码凭据 | 均为测试夹具，**误报** | 豁免挂账 |
| D（3 条） | `tools/wan_video.py:62,96`（主树未入库） | high | SSRF / CWE-918×2、路径穿越 / CWE-22 | 个人 CLI，操作者=本人；不适用威胁模型 | 仓外挂账（转正则修） |
| E | Go / npm 依赖 | — | 已知 advisory | 4 个依赖包命中 35 条，**尚未枚举** | P1 枚举后回填 |

## A. adapter exec 面（6 处，合并一组处置）

扫描语义均为「外部数据进入进程执行接口」：

| 点位 | 调用 |
| --- | --- |
| `adapters/claudecode/claudecode.go:121` | `exec.Command(m.cfg.BinPath, args...)` |
| `adapters/codexapp/codexapp.go:125` | `exec.Command(m.cfg.BinPath, m.commandArgs()...)` |
| `adapters/codexapp/session.go:38` | `exec.CommandContext(ctx, m.cfg.BinPath, m.commandArgs()...)` |
| `adapters/dsh/supervisor.go:102` | `exec.Command(s.cfg.nodeBin(), args...)` |
| `adapters/kimi/kimi.go:149` | `exec.Command(a.cfg.BinPath, args...)` |
| `adapters/kimiapp/supervisor.go:123` | `exec.Command(bin, args...)` |

**人工复核**：Go `exec.Command` 的 args 切片不经 shell，`;`/反引号类注入不成立。真实面有二：① **参数注入**——args 中的 model 等值来自 run 输入（`ModelSnapshot`，控制面 API 可写），若以 `--` 开头会被目标 CLI 当 flag 消费；② `BinPath`/`BinArgs` 来自操作者配置（`ATW_*_BIN` > 仓库 `runtimes/` > PATH），属**可信配置面**，当前无书面声明。攻击前提是已持有控制面写权限。

**修复方向（发布前，一把刀做掉）**：
1. 集中一个参数校验 helper（六处共用）：对来自 run 输入进 args 的字符串拒绝空串 / 前导 `-` / 控制字符，或按 model registry 白名单校验；违规落 `config`/`failed` 终态，不进 args。
2. 在 `docs/architecture/` 或 adapter 契约文档书面声明信任边界：BinPath/BinArgs = 操作者可信配置；run 输入 = 不可信。
3. 每个适配器补一条防回归断言（如 model 传 `--evil-flag` 应被拒绝，断言其不出现在 args）。

## B. run_changes diff 路径校验（medium）

- 污点流：HTTP `?path=`（`handlers_run_changes.go:21`）→ `RunChangeDiff`（`application/run_changes.go:208`）。
- 现状 guard：拒绝空 / 绝对路径 / Clean 后 `..` 前缀。已人工核实：diff 的 Before/After 内容来自 `runSnapshots`（DB 快照）里 `f.Path == path` 的**字符串匹配**，全程不做文件系统按 path 读——穿越面基本不成立，扫描器 proof gap 也注明「需人工确认真实数据流」。
- 修复方向：补防回归断言（`../x`、绝对路径、`a/../../b` 均落 `invalid_change_path`）；顺手核实 `RevertRunChanges` 同面无按 path 写文件。关联：`notes/implemented/architecture/2026-08-29-run-file-changes-review.md`。

## C. 测试夹具误报（豁免挂账，不改代码）

- `adapters/kimi/kimi_test.go:635`：测试向临时目录写假 kimi 二进制再 spawn——夹具本体行为，非注入。
- `internal/agentwork/kimiconfig/kimiconfig_test.go:34`：断言生成的配置含 `api_key = "sk-test-kimi"`——假 key。
- 豁免理由成立的前提约定：测试凭据一律用 `sk-test-` 前缀、测试自建二进制一律落 `t.TempDir()`；后续新增测试遵守，避免豁免面扩大。

## D. tools/wan_video.py（主树未入库个人脚本）

- `:62` 请求 helper 对 CLI 传入的中转 API URL 直接 `urlopen`；`:96` `download(url, out)` 按 CLI 参数落盘。
- 人工复核：个人命令行工具，操作者=使用者本人，SSRF/路径穿越威胁模型基本不适用；该文件**不在 git 内**（未跟踪），不构成仓库风险面。
- 处置：维持仓外则标注「不适用」挂账；若日后转正入库，需先做 scheme 白名单（https）、目标 host 白名单（中转域名）、输出路径约束在固定下载目录内。

## E. 依赖组件已知 advisory（待枚举后回填）

- 扫描附带 offlineAdvisory：**4 个依赖包命中 35 条**已知 advisory（normal 深度未展开明细）。
- 动作：`govulncheck ./...`（Go 模块）+ `pnpm audit`（`agent-team-workbench/web`）枚举，把结果回填本表；预期多数为 dev 链 npm 包，发布前按「可升级则升级、不可升级写豁免」收口。

## 复扫与门禁说明

- 修复后重跑 Mimosa 扫描（建议 deep）刷新 seal；门禁按新基线放行。
- 门禁现状备注（2026-08-29）：Git 门禁按**项目级**内联扫描硬拦 commit（本次文档重组支线的两刀纯文档提交也被拦，与本台账列出的文件零交集）；其内联扫描自报「覆盖不完整」（120s 预算内扫不完仓库），与密封扫描（coverage complete）相互独立。清零前若需提交，需在门禁侧放行（用户关停/调参）或手工提交。

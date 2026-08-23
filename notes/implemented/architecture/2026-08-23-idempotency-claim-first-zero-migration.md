# 幂等键改 claim-first，零迁移表达占位状态

Status: implemented

## 决策与理由

Idempotency-Key 从「Check→exec→Record」（并发同 key 可双执行）改为 claim-first：请求先进来就 INSERT 占位行（`ON CONFLICT DO NOTHING`），用现有列表达状态机——行存在即占位、`status_code IS NULL` 即执行中；exec 完成后回写响应，≥500 失败则删除占位行允许重试。经 httpapi 侧可选接口类型断言实现（`idempotencyClaimer`），`application/` 层零改动。

依据：「行存在+status_code 可空」已足够区分 重放/执行中/冲突 三态，无需新列；类型断言保住了非 sqlstore 测试替身的兼容回退。

## 放弃了什么

- **加迁移列（status/expires_at）**：表达力更强（可枚举状态、可 TTL 回收），但为此动双目录迁移不抵收益；占位行回收只能等后续 janitor（崩溃残行会让同 key 永远 409 in_progress）。
- **把幂等记录并入命令的权威事务**：语义最强（与副作用同生共死），但要把事务边界穿透到 application 每个命令，重构面太大。claim-first 保证「至多执行一次」，代价是崩溃窗口下「执行了但响应未落盘」会回 409 而非重放。

## 复活条件

占位行堆积或 409 in_progress 投诉出现 → 返工点：`internal/persistence/sqlstore/events.go` 加 expires_at 列 + janitor；或需要「执行结果与副作用严格同事务」的保证时 → 重构 application 命令的事务边界并把幂等记录并入。

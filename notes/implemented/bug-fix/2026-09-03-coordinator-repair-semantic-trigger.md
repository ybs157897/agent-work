# 0043 迁移放宽 coordinator repair 触发器以放行 semantic 类

Status: implemented

## 决策与理由

F3 契约要求把 `plan_semantic_validation` 纳入有界自动修复，写库时
`repair_error_class='semantic'` + `repair_status IN ('pending','exhausted')`。
`migrations/0025_coordinator_plan_repair.sql` 的
`task_coordinator_repair_checkpoint_insert/update` 两个 BEFORE 触发器把该
白名单硬编码为 `('syntax','schema')`，semantic 写入必然
`RAISE(ABORT, 'invalid coordinator repair checkpoint')`——domain 层
`ValidateRepair` 放行在 SQL 边缘无效。

决策：遵循「migrations/ 是唯一 SQLite 迁移真相源，不留第二套 DDL」纪律，
不动 0025，新增 `0043_coordinator_repair_semantic.sql` DROP + 重建两个触发器，
白名单扩为 `('syntax','schema','semantic')`；authority/quota 仍拒。
触发器只是 domain 合同在 SQL 边缘的重申面，扩值与 `ValidateRepair` 的
`validPlanRepairErrorClass` 白名单一一对应。

## 放弃了什么

- **只改 Go 层、绕开 DB 断言**：集成测试库经 `migtest.ApplyAll` 全量应用迁移，
  semantic 检查点的任何真实写入都会撞触发器，F3 无法验收；绕开等于假修复。
- **改写 0025 历史迁移**：破坏既有部署的已应用版本记录，违反迁移不可变纪律。

## 复活条件（妥协项）

- 若未来要求 authority/quota 也进自动修复（不现实的策略裁决变化）→ 需再次
  迁移扩触发器白名单与 domain 白名单，两处必须同刀修改。
- 若触发器与 domain 白名单漂移（例如新增 repair 类）→ 以
  `internal/domain/task_coordinator.go` 的 `validPlanRepairErrorClass` 为权威，
  同步补迁移；`go test ./internal/application/ -run TestCoordinatorPlanSemantic`
  会在漂移时于 SQL 边缘失败。

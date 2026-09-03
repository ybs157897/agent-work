# Dispatch 结算只保留一个可恢复续点

Status: implemented

## 决策与理由

`disp_01M1KFJJXDFXDFQDB9PB7TT2BT` 的实测链路同时暴露了四个同源缺口：
settlement Coordinator 从一个 `collecting` 批次再派 Worker 时复用了旧批；wakeup
刚被调度器标成 `consumed`、summary Run 尚未提交的窗口被恢复循环复制；当前 waiting
Plan 只有 `join/defer` 时，Worker 终态又额外生成 `children_quiet`；评估通过还能在
保险批仍为 `collecting` 时进入 `waiting_user`，使其 settlement wake 被停止态门禁吞掉。
结果是同一个 Worker 终态既可能没有可见结算，也可能产生两条 Coordinator 续点，
最终任务还可能带着未终态批次交付。

本次把 dispatch 的成员冻结点定在 `running -> collecting`：Plan 只能向 `running`
批次追加成员，来自 collecting/终态批的后续派发必须新建 `lead_plan` 批。对已抢占的
settlement wake 设置短暂 in-flight 宽限期；宽限期内不重建，超时且仍无 summary Run
才按崩溃窗口恢复。只要终态 Worker 仍属于未终态 dispatch，coordinated waiting Plan
就只等待 settlement，不再并发生成 `children_quiet`。
评估通过后再以根任务全部 dispatch 为完成栅栏：存在任何未终态批次时保持
`running/settling`，让唯一 settlement wake 继续拥有控制线，禁止提前 `waiting_user`。

## 放弃了什么

- 不允许 collecting 批重新回到 running，也不让迟到普通成员触发第二次汇总；这会破坏
  dispatch 状态机单向性，并让一次批次的成员集合与首份 settlement 摘要发生漂移。
- 不把 wakeup 消费与 Run 创建强塞进一个跨层大事务；调度器与应用层现有补偿边界会被
  重新耦合。当前用有界宽限期区分“正在创建”和“消费后崩溃”，保留既有恢复协议。
- 不禁用 settlement、只依赖 `children_quiet`；后者没有批次结算摘要，也不能可靠表达
  retry 后的最新成员结果。
- 不因 `sympy` 缺包或 AgentSwarm 参数校验失败向控制面镜像增加隐式依赖；两类失败
  均由 Agent 在同一 Run 内纠正，和 dispatch 结算丢失没有因果关系。

## 复活条件

如果 wakeup 表引入带 lease/owner token 的 `claiming` 状态，并能在 Run 创建事务里原子
完成 claim 结算，可删除时间宽限期，改用持久 owner fence 精确恢复；届时需重写
`ensureCollectingDispatchWakeups` 及调度器 queued/consumed 补偿测试。

## 验证

- 精确回归（6 条）：`go test -race -count=1 ./internal/application -run '<本修复测试集>'`
  通过，覆盖 collecting 批再派发、join-only 观察轮、fresh/stale consumed wake 与
  pause/resume 崩溃恢复、未收口批阻止 waiting_user（23.252s）。
- application 全包非 race：`go test -count=1 -timeout=10m ./internal/application`
  通过（100.282s）。
- 静态与构建：`go vet ./...`、`go build ./...` 通过。
- implementation 中间态的 application 全包 race 在 20 分钟上限耗尽；超时时仍在未改动的
  `TestRunChangesDistinguishesExistingEmptyFromNewEmpty` 创建迁移测试库，未出现断言失败
  或 race detector 报告。最终差异以 6 条 focused race + 全包非 race 收口；全包 race
  不记作通过，留给 CI 全量窗口。

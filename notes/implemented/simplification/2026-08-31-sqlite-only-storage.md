# SQLite 单一存储后端

Status: implemented

Decision date: 2026-08-31

## 决策与理由

Agent Team Workbench 只支持 SQLite。`migrations/` 是唯一迁移真相源；Control
Plane、Migrate CLI 与 atw-mcp 只接受 `sqlite://` DSN，并共用同一套连接参数：
foreign keys、busy timeout 与 WAL。`sqlstore` 删除 Dialect、PostgreSQL 占位符
翻译、advisory lock、PG 全文检索与 pgx 依赖，直接表达 SQLite 的事务和 FTS5
语义。

当前产品是单机工作台，SQLite 已覆盖真实运行、测试、迁移和浏览器验收。继续维护
两套 DDL 与两套并发/检索实现只会制造漂移面，不带来当前用户价值。

## 放弃了什么

- 放弃 PostgreSQL 运行时、pgx 驱动、PostgreSQL CI service 与 live migration job。
- 放弃 `migrations/`/`migrations/sqlite/` 双目录 parity；SQLite DDL 上移为唯一历史。
- 放弃传入非 `sqlite://` DSN 时静默选择 PostgreSQL；错误配置必须启动失败。
- 不保留 Dialect shim、空的 PostgreSQL 分支或“以后可能恢复”的死代码。

现有 SQLite 数据库的 `schema_migrations.version` 使用文件 basename；迁移文件上移
不改变版本名，因此已有数据库继续幂等升级，不需要数据搬迁。

## 复活条件

出现任一可量化事实后另立架构任务：需要多个 Control Plane 实例同时写入；需要
数据库级 HA/远程托管；或真实负载在 WAL+busy timeout 下持续出现不可接受的写锁
等待。复活时从新的存储端口与数据迁移方案开始，不恢复本次删除的方言垫片。

## 验证

- `go build ./...`、`go vet ./...`、`go test -race -count=1 ./...` 全绿。
- 唯一迁移目录 fresh apply 23 条、重复 apply 幂等；上一版已有 23 个 version 与
  2 个 Workspace 的 SQLite 库重跑后版本/数据数量不变。
- Control Plane、Migrate CLI、atw-mcp 对非 `sqlite://` DSN 明确失败。
- 受信 SQLite 连接强制 foreign keys、5 秒 busy timeout、WAL 与单连接；调用方
  传入冲突 `_pragma` 不能覆盖。
- 真实 Control Plane 在迁移后的临时库启动成功，`/health` healthy、Workspace 可读，
  退出时完成优雅关闭。

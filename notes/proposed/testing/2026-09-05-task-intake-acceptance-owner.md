# 发布前任务澄清验收 owner 方案

Status: proposed

## 决策与范围

验收分成两条边界：先用隔离 SQLite 控制面验证 task-intake/analyze 的真实 provider 请求与无副作用，再由浏览器验证对话草稿、刷新/切换恢复和用户确认后的唯一发布动作。分析请求始终把完整对话由客户端传入；它不得创建 WorkItem、Goal、Run、Coordinator、Dispatch、Plan、TaskComment、Wakeup 或幂等写记录。只有用户明确点击确认发布后，前端才调用现有 POST /api/v1/workspaces/{workspace_id}/work-items。

当前验收实例使用 127.0.0.1:18080；该端口已在 2026-09-05 12:07（Asia/Shanghai）用 lsof 确认空闲。原用户服务 127.0.0.1:8080 保持运行，禁止停止、重启、修改其数据库或向其发送发布/运行命令。前端已有隔离 Vite 实例 127.0.0.1:15173，其 /api 代理指向 18080。

## 源码证据

- agent-team-workbench/internal/persistence/sqlstore/store.go:24-79：默认 DSN 是 sqlite://workbench.db，Open 只接受 sqlite://，并固定启用外键、5 秒 busy timeout 和 WAL。
- agent-team-workbench/cmd/migrate/main.go:18-57：迁移 CLI 从当前目录的 migrations/*.sql 按序应用；迁移和控制面都必须在 agent-team-workbench 目录执行。
- agent-team-workbench/cmd/control-plane/main.go:95-105：DATABASE_URL 和 LISTEN_ADDR 是控制面入口；不设置时分别落到主库和 :8080，隔离验收必须显式覆盖。
- agent-team-workbench/cmd/control-plane/main.go:119-191,382-401：运行时 home、模型/Agent 配置、host registry 和 wakeup tick 均可由环境覆盖；隔离验收设为 3600 秒。
- agent-team-workbench/internal/application/service.go:676-699,741-765,811-855：公开根 Task 创建会进入 Coordinator，且要求验收标准；分析阶段不能调用发布 API。
- agent-team-workbench/internal/httpapi/server.go:75-89,129-141：模型、凭据和分析路由由同一 HTTP Server 装配；分析路由使用 owner 演示权限，但不经过写命令幂等包装。
- agent-team-workbench/internal/httpapi/handlers_task_intake.go:37-74,76-180：分析输入先校验，再按显式 model_ref 解析 registry 与凭据；省略模型时只读已有 Coordinator 配置，不调用 EnsureConfig。
- agent-team-workbench/internal/taskintake/client.go:1-4,186-273,309-398：provider client 只发非流式直接 HTTP 文本请求，工具、文件、审批、Provider session 和 Runtime control 均不暴露；响应必须是严格 JSON。
- agent-team-workbench/models/registry.yaml:3-30：Kimi kimi-2-7 使用 openai-responses、https://api.kimi.com/coding/v1、模型 kimi-k2.7-code、凭据环境名 MOONSHOT_API_KEY；DeepSeek 和 OpenRouter 也有 registry 条目。
- agent-team-workbench/web/vite.config.ts:7-24：Vite 通过 BACKEND_URL 配置 /api 代理，默认才是 8080；隔离前端必须传 18080，不能依赖仓库 .env.development。

## 隔离 bootstrap

以下命令只在临时目录创建数据库和 Runtime home。ATW_TMP 是本轮已创建的 /tmp/atw-task-intake-ALRK8I；重新验收时应先用空闲端口检查替换端口值，不得因占用而停止 8080。

~~~bash
WB=/Users/yin/Documents/ybs/code/agent-work-task-intake-chat/agent-team-workbench
ATW_TMP=/tmp/atw-task-intake-ALRK8I
DB=$ATW_TMP/control.db
RUNTIME=$ATW_TMP/runtime

test ! -e $DB || test -f $DB
if lsof -nP -iTCP:18080 -sTCP:LISTEN >/dev/null 2>&1; then
  echo "18080 is occupied; inspect the exact owner and choose another verified free port" >&2
  exit 1
fi

cd $WB
DATABASE_URL=sqlite://$DB go run ./cmd/migrate
~~~

控制面启动使用以下完整环境块；WEB_DIST 指向不存在的临时目录，避免把旧静态产物误当成验收页面：

~~~bash
cd $WB
DATABASE_URL=sqlite://$DB \
LISTEN_ADDR=127.0.0.1:18080 \
WEB_DIST=$ATW_TMP/no-web \
MODEL_CONFIG_DIR=$WB/models \
AGENT_CONFIG_DIR=$WB/agents \
ATW_AGENT_WORK_DIR=$RUNTIME \
ATW_CODEX_HOME=$RUNTIME/codex \
ATW_KIMI_HOME=$RUNTIME/kimi \
ATW_DSH_HOME=$RUNTIME/dsh \
ATW_HOST_REGISTRY=$ATW_TMP/host-registry.yaml \
ATW_WAKEUP_TICK_SEC=3600 \
go run ./cmd/control-plane
~~~

本机已有项目凭据文件时，使用结构化 Ruby bootstrap 读取指定 provider 并直接 exec；它不会把 key 写入命令参数、日志或仓库。helper 只向隔离控制面进程注入环境变量；若凭据文件不存在，停止并改用用户已配置的环境变量，不要打印或复制凭据内容：

~~~bash
ruby -ryaml -e '
  path = ARGV.shift
  provider_id = ARGV.shift
  data = YAML.load_file(path) || {}
  item = Array(data["items"]).find { |entry| entry["provider_id"] == provider_id }
  abort "provider credential missing" unless item && !item["api_key"].to_s.empty?
  exec({"MOONSHOT_API_KEY" => item["api_key"]}, *ARGV)
' \
  /Users/yin/Documents/ybs/code/agent-work/agent-team-workbench/.agent-work/credentials.local.yaml \
  prov-kimi \
  env DATABASE_URL=sqlite://$DB \
      LISTEN_ADDR=127.0.0.1:18080 \
      WEB_DIST=$ATW_TMP/no-web \
      MODEL_CONFIG_DIR=$WB/models \
      AGENT_CONFIG_DIR=$WB/agents \
      ATW_AGENT_WORK_DIR=$RUNTIME \
      ATW_CODEX_HOME=$RUNTIME/codex \
      ATW_KIMI_HOME=$RUNTIME/kimi \
      ATW_DSH_HOME=$RUNTIME/dsh \
      ATW_HOST_REGISTRY=$ATW_TMP/host-registry.yaml \
      ATW_WAKEUP_TICK_SEC=3600 \
      go run ./cmd/control-plane
~~~

上面凭据路径的正确值是 /Users/yin/Documents/ybs/code/agent-work/agent-team-workbench/.agent-work/credentials.local.yaml。常规启动不需要凭据，缺凭据时分析 API 应返回可见的 analysis_credentials_missing。本轮 root 已终止旧 PID 38630，并用新 binary 接管同一临时 DB 和 18080；后续加载新代码仍必须先停止当前精确隔离 PID，再复用同一端口，不能新开第二个 18080 监听。

启动后先做只读 bootstrap，不创建任务：

~~~bash
BASE=http://127.0.0.1:18080/api/v1
WS=$(curl -fsS $BASE/workspaces | python3 -c 'import json,sys; print(json.load(sys.stdin)["items"][0]["id"])')
BOOT=$(curl -fsS $BASE/workspaces/$WS/bootstrap)
python3 -c 'import json,sys; d=json.loads(sys.argv[1]); assert d["health"]["control_plane"]=="healthy"; assert d["dashboard"]["running_tasks"]==0; assert "event_cursor" in d; print("bootstrap=healthy workspace=%s agents=%d tasks=%d running=%d" % (sys.argv[2], len(d["agents"]["items"]), len(d["work_items"]["items"]), d["dashboard"]["running_tasks"]))' "$BOOT" "$WS"
sqlite3 "$DB" 'SELECT "execution_runs", count(*) FROM execution_runs UNION ALL SELECT "task_coordinator_states", count(*) FROM task_coordinator_states UNION ALL SELECT "agent_wakeup_requests", count(*) FROM agent_wakeup_requests;'
~~~

本次实测 bootstrap 结果：1 个 workspace、5 个 Agent、3 个 seed Task、running_tasks=0；SQLite 中 execution_runs=0、task_coordinator_states=0、agent_wakeup_requests=0。seed Task 没有自动创建执行 Run。

## 真实 provider 分析 smoke

分析 API 的路径是：

~~~text
POST /api/v1/workspaces/{workspace_id}/task-intake/analyze
~~~

请求不带 Idempotency-Key，因为该端点声明为无状态分析；请求体使用完整对话，显式指定 model_ref 以避免读取或创建 Coordinator 配置：

~~~bash
BEFORE=$(sqlite3 "$DB" 'SELECT "work_items",count(*) FROM work_items UNION ALL SELECT "execution_runs",count(*) FROM execution_runs UNION ALL SELECT "task_coordinator_states",count(*) FROM task_coordinator_states UNION ALL SELECT "task_coordinator_configs",count(*) FROM task_coordinator_configs UNION ALL SELECT "dispatches",count(*) FROM dispatches UNION ALL SELECT "plans",count(*) FROM plans UNION ALL SELECT "task_comments",count(*) FROM task_comments UNION ALL SELECT "agent_wakeup_requests",count(*) FROM agent_wakeup_requests UNION ALL SELECT "outbox_messages",count(*) FROM outbox_messages UNION ALL SELECT "idempotency_keys",count(*) FROM idempotency_keys;')
curl -fsS --max-time 120 \
  -X POST "$BASE/workspaces/$WS/task-intake/analyze" \
  -H 'Content-Type: application/json' \
  -d '{"model_ref":"kimi-2-7","messages":[{"role":"user","content":"我想新增一个发布前任务澄清对话。请先整理目标、范围、约束和可验证验收标准，不要发布或执行任务。目标是让用户确认草案后才创建任务。"}]}' \
  | tee "$ATW_TMP/analysis-response.json" \
  | python3 -c 'import json,sys; d=json.load(sys.stdin); assert isinstance(d.get("reply"),str) and d["reply"].strip(); print("analysis=ok reply_chars=%d draft_present=%s" % (len(d["reply"]), bool(d.get("draft"))))'
AFTER=$(sqlite3 "$DB" 'SELECT "work_items",count(*) FROM work_items UNION ALL SELECT "execution_runs",count(*) FROM execution_runs UNION ALL SELECT "task_coordinator_states",count(*) FROM task_coordinator_states UNION ALL SELECT "task_coordinator_configs",count(*) FROM task_coordinator_configs UNION ALL SELECT "dispatches",count(*) FROM dispatches UNION ALL SELECT "plans",count(*) FROM plans UNION ALL SELECT "task_comments",count(*) FROM task_comments UNION ALL SELECT "agent_wakeup_requests",count(*) FROM agent_wakeup_requests UNION ALL SELECT "outbox_messages",count(*) FROM outbox_messages UNION ALL SELECT "idempotency_keys",count(*) FROM idempotency_keys;')
test "$BEFORE" = "$AFTER"
~~~

tee 的响应文件只含 API 返回，不含 provider key；若本地日志/脚本启用了 shell tracing，应先关闭。该 smoke 的通过条件是 HTTP 200、reply 非空、响应 JSON 可解析，并且上述控制面表计数完全不变。不要在这个 smoke 中调用 POST /workspaces/$WS/work-items、POST /work-items/$ID/runs、POST /plans 或 wakeup 命令。

错误边界至少逐项验证以下输入，且每次请求后重复检查上述计数：

| 场景 | 期望 HTTP/code | 不能发生的副作用 |
| --- | --- | --- |
| 空 messages、system/tool role、最后一条 assistant、超 40 条或超 40000 字符 | 400 / analysis_invalid_input | 不读 provider，不写控制面 |
| 不存在 model_ref、completion-only 以外协议、缺 base URL | 422 / analysis_model_unsupported | 不创建 Coordinator 或 Runtime binding |
| 缺凭据 | 422 / analysis_credentials_missing | 不把 key/上游 body 放进响应或日志 |
| provider 429/5xx/网络断开 | 502 / analysis_upstream_error，按 retryable 分类 | 不创建任务、Run 或重试风暴 |
| provider 超时 | 504 / analysis_timeout | 请求有界结束，计数不变 |
| provider 返回空、畸形 JSON、未知字段、超大响应或不完整草案 | 502 / analysis_invalid_response | 不展示可发布草案，不写控制面 |

Provider smoke 使用 Kimi 的直接 Responses API；本机已用同一 registry/model 和本地项目凭据验证 HTTP 200、status=completed、精确输出 READY。该证据只证明 provider 连接可用，不替代新分析 API 的无副作用和响应契约验收。

## 浏览器验收清单

连接 http://127.0.0.1:15173/tasks，确认页面通过 Vite /api 代理访问 18080。只做以下顺序：

1. 打开新建任务入口，输入一条含糊需求并发送；确认消息正文沿用 Agent 对话样式，回复显示追问或草案。
2. 反复发送补充，确认请求始终是 task-intake/analyze；Network 中没有 work-items、runs、plans 或 wakeup 写请求。
3. 分析期间和收到草案后刷新页面、关闭再打开入口、切换 workspace；草稿按 workspace 恢复，不能串稿或触发发布。
4. 编辑标题、描述、验收标准、优先级和截止日期；缺标题或缺验收标准时确认发布按钮保持禁用。
5. 继续对话后，旧草案不能继续直接发布；必须以最新分析结果/编辑内容为准。
6. 最后一次只点击确认发布；仅此时观察一次 POST /api/v1/workspaces/{workspace_id}/work-items，响应应是根 record_kind=task、status=todo，然后才允许 Coordinator/调度链开始。
7. 断开或模拟发布响应丢失后重试，必须复用同一 client_key 与相同 payload；HTTP 层 Idempotency-Key 可以是每次请求新生成的，不能产生第二个 WorkItem。

分析 UI 完成而未点击确认时，隔离 DB 的 work_items、execution_runs、task_coordinator_states、task_coordinator_configs、dispatches、plans、task_comments、agent_wakeup_requests 计数必须保持 bootstrap 基线。发布后的执行验证属于另一条明确的集成验收，不能倒推分析阶段已经安全。

## 独立复审记录

2026-09-05 已在新 binary 上使用显式 model_ref=kimi-2-7 走真实 Kimi Responses provider：HTTP 200，reply 非空，完整草案含 6 条验收标准；请求前后上述控制面计数保持不变。空 messages、非法 role、末条 assistant、未知 model_ref、未知字段分别返回 400/422，且 invalid input 未触发 provider。

当前仍有一个集成 gate：backend 的 `TestAnalyzeTaskIntakeWithoutModelDoesNotEnsureCoordinator` 已证明，干净 workspace 没有既存 Coordinator 配置且请求省略 model_ref 时会返回 analysis_model_unsupported；frontend 的 `runAnalysis` 目前只发送 messages/draft，没有 model_ref。浏览器验收必须先解决这个选择路径（显式带可直接调用的 model_ref，或在分析前由设置流程建立配置），否则干净 bootstrap 无法走真实模型。

另有两个契约/错误分类差异需在合入前收口：OpenAPI 的 TaskIntakeAnalyzeResponse.reply 上限是 12000 字符，而 taskintake 客户端的 MaxReplyRunes 当前等于输入消息上限 8000；8001–12000 字符的合法契约响应会被判为 analysis_invalid_response。OpenAPI 还把响应根对象声明为 additionalProperties=false，但当前 parseResult 会投影并接受未知根 presentation 字段，现有测试也已按此行为通过。两者必须统一，不能让契约与实现各自定义成功条件。

错误边界仍有一个未覆盖的真实路径：Client.Analyze 在 httpClient.Do 返回 context deadline 时映射 analysis_timeout；如果 provider 已返回 headers、随后 response body 读取跨过 context deadline，io.ReadAll 的错误分支却映射为 analysis_upstream_error/502。应补充“headers 已到、body 阻塞”的 timeout 断言，确保所有超时都返回 504/analysis_timeout。

## Questions focused review

Questions UI 与 store 在本轮已补齐到可编译状态：`pnpm tsc -b` 通过，新增的 store questions focused 测试 13/13 通过。原生 radio/checkbox、fieldset/legend 与 textarea 提供了键盘和自由补充的基础交互；选项点击只更新本地 answer map，只有提交回答按钮才进入 analyze。

此前发现的零条目旧题时序 gate 已闭合：`markQuestionAnswersContinued` 现在遍历所有历史 assistant questions，为未提交且没有 answer map 条目的题写入空的 `continued`；`latestAnswerableQuestionMessage` 让 selection/supplement/submit 只接受最新可回答 message。新增测试已覆盖“零条目旧题经自由发送后锁定、旧题选择和提交均 no-op，已有新草案保持不变”。

Questions 的持久化结构已包含 message.questions 与 questionAnswers，刷新后可以恢复已选值；当前 focused tests 已覆盖基本解析、回答提交、自由 composer 跳过、未完成题不调用 analyze 和零条目旧题过期。仍建议在真实浏览器验收中确认刷新恢复选项/答案、模型回放带 questions，以及 radio/checkbox/补充说明的实际键盘行为。

## 当前运行证据与清理

- 隔离数据库：/tmp/atw-task-intake-ALRK8I/control.db。
- 隔离 Runtime home：/tmp/atw-task-intake-ALRK8I/runtime。
- 本轮 root 已将现有本地 credentials.local.yaml 复制到 /tmp/atw-task-intake-ALRK8I/runtime/credentials.local.yaml，并收紧为 0600；该副本仅供隔离进程读取，验收后按精确临时路径清理，主凭据文件不改。
- root 已用新 binary /tmp/atw-task-intake-control-plane 接管同一隔离 DB；当前 exec session：50331，实际监听 PID 43440（以 lsof 为准，若已再次重启按当前精确 PID 更新）。
- 原 8080 用户服务 PID：23374，父 PID：23356；不要操作。
- 前端 Vite 实际监听 PID：36791，端口：15173；不要操作。

完成验收后，先在 root 启动终端发送 Ctrl-C 让当前 lsof 显示的隔离 PID（当前为 43440）优雅退出，再确认 18080 已释放；临时目录可按精确路径清理。不得使用按名称匹配的 broad kill，也不得清理主 worktree、主库或共享 Runtime home。

## 放弃了什么

不使用主 workbench.db 或 8080 服务做 smoke，因为旧数据、调度器和用户当前任务会污染“分析无副作用”证据；不把临时数据库放在仓库 agent-team-workbench/ 下，因为 SQLite 会产生 WAL/SHM 和未跟踪文件；不通过现有 CreateRun/Agent Runtime 做澄清，因为那条路径天然进入 Run、Session、Dispatch 和调度语义；不使用 fixture provider 作为唯一证据，因为 fixture 不能证明真实 HTTP endpoint、凭据读取和 provider 错误分类。

## 复活条件

【分析 API 改为持久化会话、需要跨设备恢复或需要流式事件 → 重新评估独立 intake session/事件模型，并补充 SQLite 表、权限、清理与重放验收 → 在未完成设计前保持当前 stateless request 契约】


## 最终交付状态

实现与验收完成，等待用户决定合并；合并后清除此 owner 工作工件。最终决策与验收事实见 `notes/implemented/feature/2026-09-05-task-intake-chat.md`。root 已完成真实 UI 发送、草案编辑与刷新恢复、确认发布、丢失响应后同任务重放、超限输入与清除确认验收；1024×700 无横向溢出。临时 18080/15173 服务已停止、端口释放，临时凭据副本和数据库已清除；截图与非敏感验收 JSON 保存在本轮可视化工件目录。


## 正文问答收口

正文单选、多选、逐题补充、历史题失效、pending composer 保存及分析草案上下文已完成并验收。最终前端 104 文件/861 测试与 build/lint 通过，后端 build/vet/race 通过；真实浏览器 4 次分析请求均未发布任务。临时 questions 环境、服务和凭据副本均已清理，等用户明确合并指示。

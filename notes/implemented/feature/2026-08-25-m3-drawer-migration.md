# M3 表单容器迁移：九处 Modal 换 Drawer，五处按纪律留 Modal

Status: implemented

审计 P2 #8「弹窗偏多」落地。先升级 Drawer 为表单容器（cd5cb90），
再四路蜂群逐页换装（cdd0cb5/7107075/0d6b3da/afd7593）。

## 决策与理由

1. **Drawer 双形态契约**：带 title = 表单容器（头行 h3+关闭、可滚动 body、
   宽 480 起）；不带 title = 自由内容（task-detail 零改动兼容）。补
   role=dialog/aria-modal/Escape/createPortal——旧 Drawer 无头行无滚动无
   语义，直接派蜂群会逼四个执行者各自手搓头部，先立契约再并行。
2. **映射一刀切规则**：创建/编辑表单（≥3 字段或列表型）→ Drawer；快速
   过渡对话框（打回/阻塞/唤醒）与破坏性确认（删模型/删供应商）→ 留 Modal。
   理由：过渡对话框是「决策」不是「填写」，模态的打断感是特性；破坏性
   确认留模态是 DESIGN.md Don'ts。
3. **内边距归属**：Modal 自带 p-comfortable，Drawer body 不带——换装时
   children 外包 p-comfortable，视觉零回归。
4. **蜂群质量协议**：四路文件边界互斥；共享组件（drawer/modal/ui/DESIGN.md）
   只读，API 不够报偏差由主智能体集中修；执行者禁 git 写，提交权收口。
   四路零偏差返回。

## 负向保证

- 新表单容器一律 Drawer（带 title 形态）；新破坏性确认一律 Modal。
- Drawer 不再允许无滚动长内容（body 已内置 overflow-y-auto）。

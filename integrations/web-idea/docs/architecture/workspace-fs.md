# 工作区文件系统

Status: accepted

Date: 2026-08-30

## 1. 目标

在 **单一 Workspace Root** 下提供安全的目录列举、文件读取与 **UTF-8 文本写回**，供目录树与 Monaco 打开/保存。二进制与超大文件仍拒绝。

## 2. 路径模型

- 客户端只使用 **相对路径**：`src/main/java/com/example/App.java`
- 禁止：`..` 段、绝对路径、空段、`~`、Windows 盘符、含 `\` 的混用（统一拒绝反斜杠）
- Gateway 算法：

```text
clean = path.Clean("/" + relative)           # 得到以 / 开头的干净路径
rel   = strings.TrimPrefix(clean, "/")
abs   = filepath.Join(root, filepath.FromSlash(rel))
abs   = filepath.Abs(abs)
assert strings.HasPrefix(abs, rootWithSep)  # root 自身或子路径
```

符号链接：默认 **解析后** 仍须落在 root 内；出界则 403。可通过配置改为「禁止跟随 symlink」（更严）。
`fs/files` 使用更严格的只读策略：遍历时跳过所有符号链接，不读取文件内容。

## 3. API 语义

### `GET .../fs/tree?path=&depth=`

- `path` 默认 `""`（root）
- `depth` 默认 `1`（只列直接子项）；最大 `3`
- 返回：`name`、`type`（file|dir）、`size?`、`modifiedAt?`
- 跳过：`.git` 目录内容可列名但默认折叠策略由 UI 决定；**不** 跟随进 `node_modules` 等超大目录时可服务端 denylist（可配）

### `GET .../fs/file?path=`

- 仅文件；目录返回 400
- `Content-Type: text/plain; charset=utf-8` 或 `application/octet-stream` + 前端按扩展名处理
- 超过 `MaxFileBytes` → 413
- 二进制检测：含 NUL 或明显非文本 → 415 或返回「不支持预览」

### `PUT .../fs/file?path=`

- 仅已有文件或 jail 内新建文本文件（父目录须已存在）
- Body：UTF-8 文本；`Content-Type: text/plain; charset=utf-8`
- 超过 `MaxFileBytes` → 413；含 NUL / 非 UTF-8 → 415
- 目录路径 → 400；出界 → 403

### `GET .../fs/stat?path=`

- 元数据；供 UI 与缓存校验（ETag / mtime）

### `GET .../fs/files?q=&limit=`

- 用于 IDEA 风格的快速打开；只搜索相对 POSIX 路径和文件名，不打开或读取文件内容。
- `q` 必填，大小写不敏感；支持文件名/路径包含匹配，也支持按字符顺序的 subsequence 匹配。
- `limit` 默认 `60`，最大 `200`；结果按文件名精确/包含、路径包含、subsequence 的相关性排序，同分按路径稳定排序。
- 遍历最多访问 `10000` 个目录项、扫描 `5000` 个常规文件；默认跳过 `.git`、`node_modules`、`build`、`target`、`dist` 等生成或依赖目录，并跳过所有符号链接。
- 响应为 `{query, paths, truncated?}`。达到结果或遍历边界时，`truncated` 表示仍有结果未返回；HTTP 请求取消会停止继续遍历且不再写响应。

## 4. 忽略与性能

| 规则 | 默认 |
|------|------|
| 单次 tree 条目上限 | 2000；超出截断并 `truncated: true` |
| 单次 file-path 搜索结果 | 默认 60，最大 200；超出截断并 `truncated: true` |
| 单次 file-path 搜索遍历 | 最多扫描 5000 个常规文件 |
| 单次 file-path 搜索目录项 | 最多访问 10000 个目录项 |
| 隐藏文件 | 返回；UI 可过滤 |
| `.git` 对象 | 不递归 `objects/` |
| 并发 | 单 workspace 读限流（例如 32） |

## 5. 与 jdtls 的关系

- FS API **不** 依赖 jdtls；
- jdtls 使用同一 root 做索引；保存后客户端应 `didChange`/`didSave` 以刷新补全；
- Gateway 不通过 FS API 代理 class 文件读取（除 P3 虚拟文件）。

## 6. 写入边界（ADR-007）

- 允许：单文件 UTF-8 文本写回 jail；
- 禁止：二进制、超大文件、路径逃逸、重构类批量写盘；
- 审计日志 / feature flag 可后续加，本刀默认开启文本写。

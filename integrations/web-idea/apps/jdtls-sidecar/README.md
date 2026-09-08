# jdtls-sidecar

Eclipse JDT Language Server 的启动约定与可选容器定义。

## 要求

- JDK **21+** 运行 jdtls
- 预置或下载 [eclipse.jdt.ls](https://github.com/eclipse-jdtls/eclipse.jdt.ls) 发行物
- 每 workspace 独立 `-data` 目录

## 启动参数骨架（由 Gateway 组装）

```text
java \
  -Declipse.application=org.eclipse.jdt.ls.core.id1 \
  -Dosgi.bundles.defaultStartLevel=4 \
  -Declipse.product=org.eclipse.jdt.ls.core.product \
  -Dlog.level=ERROR \
  -Xmx${WEBIDEA_JDTLS_XMX:-1g} \
  --add-modules=ALL-SYSTEM \
  --add-opens java.base/java.util=ALL-UNNAMED \
  --add-opens java.base/java.lang=ALL-UNNAMED \
  -jar plugins/org.eclipse.equinox.launcher_*.jar \
  -configuration config_mac|config_linux|config_win \
  -data /path/to/var/jdtls-data/{workspaceId}
```

stdio 与 Gateway 相连；**不要** 默认 `-listen` 公网端口。

Gateway 的只读 workspace 会设置 `WEBIDEA_READ_ONLY=true`。启动脚本据此传入
`-Djava.import.generatesMetadataFilesAtProjectRoot=false`，让 `.project`、
`.classpath`、`.settings` 等 Eclipse metadata 留在 jdtls data 区，不写回 checkout。

## 状态

P2：`scripts/fetch-jdtls.sh` 下载发行物到 `dist/`；`bin/launch.sh <dataDir> <workspaceRoot>` 以 stdio 启动。

Gateway 环境变量：

```bash
export WEBIDEA_JDTLS_LAUNCH="$PWD/apps/jdtls-sidecar/bin/launch.sh"
export WEBIDEA_JDTLS_DATA="$PWD/var/jdtls-data"   # 可选，默认系统临时目录
# export WEBIDEA_JDTLS_HOME="$PWD/apps/jdtls-sidecar/dist"  # launch.sh 默认即此
```

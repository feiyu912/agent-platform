# ZenForge引擎

## 当前状态

ZenForge 是普通 agent 的可灰度执行引擎。默认请求仍走现有 `legacy` 引擎；只有配置全局启用或命中 agent、chat、run 级 override 时，才会选择 `zenforge`。

本仓库当前只支持 Go 1.26。`go.mod` 声明为 `go 1.26.0`，ZenForge bridge 与 selector 文档、构建和验证都应按 Go 1.26 环境执行；不要用 Go 1.25 或更早版本验证这条链路。

proxy agent 不进入 ZenForge selector，继续使用原有 HTTP/WS proxy 路由。

## 配置入口

YAML 配置位于 `configs/runtime.yml` 根级：

```yaml
zenforge:
  enabled: false
  fallback-on-init-error: false
  agent-overrides:
    default_agent: zenforge
  chat-overrides:
    chat-canary: legacy
  run-overrides:
    run-canary: zenforge
```

字段含义：

| 字段 | 说明 |
|---|---|
| `zenforge.enabled` | 全局默认引擎开关。`false` 时全局默认 `legacy`；`true` 时全局默认 `zenforge`。 |
| `zenforge.fallback-on-init-error` | 仅在 ZenForge 惰性单例初始化失败时回退到 `legacy`。 |
| `zenforge.agent-overrides` | agent key 到 `legacy` 或 `zenforge` 的映射。 |
| `zenforge.chat-overrides` | chat id 到 `legacy` 或 `zenforge` 的映射。 |
| `zenforge.run-overrides` | run id 到 `legacy` 或 `zenforge` 的映射。 |

等价环境变量为：

| 环境变量 | 格式 |
|---|---|
| `ZENFORGE_ENABLED` | boolean |
| `ZENFORGE_FALLBACK_ON_INIT_ERROR` | boolean |
| `ZENFORGE_AGENT_OVERRIDES` | `agentA=zenforge,agentB=legacy` |
| `ZENFORGE_CHAT_OVERRIDES` | `chatA=zenforge,chatB=legacy` |
| `ZENFORGE_RUN_OVERRIDES` | `runA=zenforge,runB=legacy` |

override 的 key 会去除首尾空白后匹配；值只接受 `legacy` 或 `zenforge`。YAML map key 归一化后不能为空；环境变量不允许空项、空 key、空 value 或重复 key。配置错误会在启动时直接失败。

## 选择优先级

selector 先根据 `zenforge.enabled` 得到全局默认值，再依次应用 override：

```text
global enabled flag -> agent-overrides -> chat-overrides -> run-overrides
```

因此最终优先级固定为：

```text
run > chat > agent > global
```

示例：

```yaml
zenforge:
  enabled: true
  agent-overrides:
    default_agent: legacy
  chat-overrides:
    chat-canary: zenforge
  run-overrides:
    run-force-legacy: legacy
```

在这个配置下：

- 未命中任何 override 的普通 agent 请求走 `zenforge`。
- `default_agent` 下的请求默认走 `legacy`。
- `chat-canary` 会覆盖 agent 级选择并走 `zenforge`。
- `run-force-legacy` 会覆盖 chat 和 agent 级选择并走 `legacy`。

## 固定选择边界

普通 agent query 在准备阶段只选择一次引擎，并把结果固定到本次请求或对应 run 的后续边界。

固定使用同一选择结果的入口：

| 入口 | 选择边界 |
|---|---|
| HTTP sync query | query 准备阶段选择一次。 |
| HTTP async query / SSE | query 准备阶段选择一次，后续 SSE 只输出该 run 的事件。 |
| WebSocket query | query 准备阶段选择一次，后续 stream frame 使用该选择。 |
| approval submit | 提交给当前 run 的 awaiting/control，不重新选择到另一引擎。 |
| attach | 只订阅已注册 run 的 event bus，不重新选择引擎。 |
| awaiting continuation | 按原 run/session 标识重新执行同一选择规则，保持该 run 的选择边界。 |

proxy agent 固定保持原路由，不参与上述 selector。

## 失败和回退语义

ZenForge 引擎是进程内惰性单例：首次被选择为 `zenforge` 时才初始化。

`fallback-on-init-error` 只覆盖 ZenForge 初始化错误，包括 factory 缺失、factory 返回错误或返回空 engine 等初始化阶段失败。启用该开关时，这类错误会回退到 `legacy`；关闭时，请求直接返回 ZenForge 初始化错误。

一旦请求已经选定引擎并调用该引擎的 `Stream`，后续模型、工具、审批、流式传输、事件映射或运行期错误都不会触发跨引擎 fallback，也不会把同一请求重放到另一引擎。Stream 后错误按当前协议正常上报。

## 状态目录

ZenForge checkpoint 与 event 状态固定写入：

```text
${CHATS_DIR}/.zenforge/
```

该目录没有单独配置开关。启用前应确认：

- `${CHATS_DIR}` 可写，服务进程可以创建 `.zenforge/`。
- `.zenforge/` 与 chat 数据使用一致的持久化、备份和恢复策略。
- 清理或迁移 chat 数据时同步考虑 `.zenforge/`，避免 checkpoint/event 与 chat run 记录不一致。

## 迁移和灰度建议

1. 确认运行环境使用 Go 1.26，并完成现有 `legacy` 链路的基线测试。
2. 保持 `zenforge.enabled: false`，先使用 `agent-overrides` 对单个低风险 agent 配置 `zenforge`。
3. 对单个 chat 或 run 做更小粒度 canary 时，使用 `chat-overrides` 或 `run-overrides`；需要强制回退某个 chat/run 时显式写 `legacy`。
4. 灰度早期建议开启 `fallback-on-init-error`，只兜底 ZenForge 初始化失败；不要把它理解为运行期自动容灾。
5. 观察 SSE/WS 输出、approval submit、attach、awaiting continuation 和 `.zenforge/` 状态文件后，再扩大到更多 agent。
6. 全量切换时再设置 `zenforge.enabled: true`，并保留必要的 `legacy` override 作为回退名单。

## 验证清单

- `go version` 为 Go 1.26。
- `configs/runtime.yml` 中 `zenforge` 字段可被启动配置加载，非法 override 会 fail closed。
- `zenforge.enabled: false` 且无 override 时，普通 agent 仍走 `legacy`。
- agent、chat、run 同时配置时，实际选择符合 `run > chat > agent > global`。
- proxy agent 的 HTTP/WS query 仍走原 proxy 路由。
- HTTP sync、HTTP async/SSE、WebSocket query 均在准备阶段固定选择一次。
- approval submit 继续当前 run，不切换引擎。
- attach 只接入已注册 run 的 event bus，不重新选择引擎。
- awaiting continuation 按原 run/session 标识恢复，符合相同选择规则。
- ZenForge 初始化失败时，`fallback-on-init-error: true` 回退到 `legacy`，`false` 返回初始化错误。
- ZenForge `Stream` 开始后的模型、工具、审批或流错误不会重放到 `legacy`。
- `${CHATS_DIR}/.zenforge/` 可写，checkpoint/event 文件与 chat 数据一起备份和恢复。

## 相关文档

- [配置化说明](配置化说明.md)
- [API与协议](API与协议.md)
- [真流式和H2A](真流式和H2A.md)

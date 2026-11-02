# 账号 routing alias 设计方案

基线：`upstream/main`，`96a703a591c9fbcdaeab915152081c5f92d59778`。状态：方案已由主 Agent 复核，包含独立 review 的 F1–F3 修正；实现与验证尚未完成。

配套执行安排见《账号 routing alias 实施计划》§3“按依赖推进的实施批次”（[实施计划](account-alias-plan.md#3-按依赖推进的实施批次)）。


### 1. 用户可以选择账号，已有普通路由继续工作

“routing alias”沿用用户需求中的 alias，中文界面称“路由别名”。“qualified model”指包含这个选择条件的完整 model reference，例如 `codex/gpt-5.5@work`；其中 `work` 选择账号，上游仍收到 `gpt-5.5`。这两个名称与已有 `AccountNames` 区分：后者只是显示名，当前 CLI 的 `accounts alias` 也只改显示名。

首版范围包括：

- Web UI 为各订阅账号、插件提供的订阅账号及各 API key 设置、修改、清除 alias，包含只有一个账号或第一把 key 的情况。
- Gateway 已支持的生成协议及使用账号/key 的其他服务路径：Chat、Responses、Messages、Gemini、Ollama、count_tokens、图片、视频、embedding、rerank、transcription、System One/decision，以及 Codex 后端、搜索和 compaction 的相关路径。
- 账号隔离、模型列表、agent 模型选择、group 成员、gateway key 权限、sync/backup、账号显示名变化、provider rename、key rotation。
- 现有 native model ID 中的 `@`、模型路径中的 `/`，以及现有 effort、Claude `[1m]`、Claude dashed 和 flat `~` 拼写的兼容。

alias 是账号引用，不创建合成 provider，不产生第二份账号、key、token、sign-in 文件或 credential blob。备份沿用已有“包含 keys”的机制；本功能只随现有配置传递 alias 元数据，不增加凭据复制路径。真正完全绕开 magpie、直接访问 vendor 的 agent 原生请求不在本次范围；经过 magpie 的直连优化属于范围，必须执行隔离规则。

例子：provider `p` 有账号 A、B。A 没有 alias，B 的 alias 是 `work`。请求 `p/m` 只能选择 A；即使旧 affinity 指向 B，也不能选择 B。请求 `p/m@work` 只能选择 B；B 返回 429，错误直接返回客户端，即使 A 正常、`p.Fallback` 有其他 provider，也不能改用它们。若用户建立 group，显式保存 `p/m@work` 与 `q/m@backup`，前者失败后可由后者回答。

### 2. 主 Agent 已批准的语义与探索报告的取舍

以下是本轮通过 supervisor 工具获得的主 Agent 决定，实施和测试以此为准。

| 事项 | 已批准行为 |
| --- | --- |
| 模型可发现性 | qualified model 必须出现在 `/v1/models` 和 agent 模型选择中；普通 model 仍展示。 |
| `@` 歧义 | 保留完整已知 native ID；未列出的、末尾为字母开头 `@suffix` 的输入进入 alias 解析，未知 alias 立即失败。数字窗口/日期仍保留。 |
| alias owner | 同一 provider 内已用 alias 不能转给另一 owner；原 owner 可以恢复。保存 retired 记录；key rotation 显式迁移绑定。 |
| 新 native ID 与历史 alias 冲突 | 保留记录优先保护既有引用，歧义拼写明确失败，不能重新解释成普通 native model。 |
| 普通 provider fallback | 保留现有行为，普通 target 只使用对应 provider 的普通池；不能因为没有普通账号就自动选择 alias 账号。 |
| direct qualified fallback | 禁止其他账号和 provider fallback。 |
| group fallback | 失效 qualified 成员退出本次候选，其他显式成员仍可回答；不引入隐式成员。 |
| Codex 优化路径 | 所有 magpie 提供的路由/服务路径均遵守隔离；不能因只有一个账号就 relay 绕过。 |
| gateway key | base `p/m` 与 `p/*` 可授权该基础模型的 qualified variants；精确 `p/m@a` 只授权 a。models 与 accounts 取交集，group 授权保留既有规则。 |
| 请求记录 | `Requested` 保留 qualified reference；`Model` 和价格使用 base。可选 OTLP 沿用发送 `Requested` 的既有配置行为。 |
| 文档落盘 | 本报告先完整输出；主 Agent 在进入测试前拆成项目下两份工作文档。 |

两份探索报告建议首版不在 catalog 展示 qualified model；这里不采纳，因为 session 需要从现有模型选择器直接选账号。通过独立派生 entries，避免把 alias 当成新的基础模型，成本限定在已有 catalog 和 agent 接口。

探索报告对普通 provider 的所有账号都有 alias 时是否禁止 fallback 存在不同建议。这里保留用户已配置的普通 fallback，因为普通请求没有指定账号，隔离要求是“不使用有 alias 的账号”；直接 qualified 请求才禁止 provider fallback。

探索报告建议的单张 `ref → alias` 表不能表达已删除 alias 的 owner，也不能可靠处理 keyless backup。这里采用账号、key 分开的 registry，并保留 retired bindings。历史记录只解决本次需求中的旧引用不能转绑，不增加管理命令或确认流程。

### 3. 语法明确区分 alias 与 native `@`

规范输入为 `<provider>/<model-id>@<alias>[:<effort>][1m]`。group 成员继续使用已有 effort 多值和 Fast 表达方式；alias 位于 effort 之前，Fast 不成为 direct model 的新语法。

alias 保存时 trim，再转换为 ASCII 小写；合法形式为 `[a-z][a-z0-9._-]{0,31}`，即 1–32 个 ASCII 字符，首字符必须是字母。请求中的 alias 也按这一规则规范化；provider 和 native model ID 保持现有大小写及精确匹配规则。空字符串仅表示设置 API 的“清除 alias”，不是合法的 qualified model。禁止空白、`/`、`@`、`:`、`~`、`[]`、`+` 和 glob 字符进入 alias。

同一 provider 内，所有当前和 retired alias 都受 owner 唯一性约束；不同 provider 可使用相同 alias。每个账号/key 同时最多有一个 active alias。设置 alias 时，若它会与该 provider 已知的完整 native ID 冲突，则拒绝保存；检查已知列表和用户 picks，而不只检查当前对 agent 展示的子集。

只有最后一个 `/` 之后出现的末尾 `@suffix` 可能是 alias。先取最后一个 `@`，这样 native base 本身可以包含 `@`。以数字开头且符合 `[0-9]+[kKmM]?` 的末尾 suffix 维持 native 数字标记含义，日期如 `20260801` 也落在这里。其他非法 suffix 不能退回普通请求；完整已知 native ID 则按照下述顺序保留。

统一解析顺序如下，不能把“literal-first”写成没有例外的规则：

1. 按现有输入契约处理 agent 的外层 spelling，保留原始请求 reference；对确实被识别的 effort 和 `[1m]` 拆分，完整 native ID 中原有的 `:high` 等仍先按模型本义识别。
2. 找出可能的末尾 alias。如果该 alias 有当前或 retired 保留记录，而完整输入现在又是一个已知 native ID，则返回冲突错误，不重新解释、不删除 owner 记录。
3. 完整输入是已知 native model ID 时，按 native literal 处理。这里的已知集合必须来自基础模型列表，不能包含由 alias 派生的 catalog entries，也不能把旧 `Resolve` 的任意 `provider/model` 接受分支当成“已知”。
4. 对末尾 alias 语法解析 provider、base model 和 alias。当前 active binding 才能形成选择条件；retired、未知或绑定账号缺失分别返回明确错误。alias 不存在时不能进入普通 `Resolve`、`GroupFor`、AutoStandIn、agent stand-in 或 bare-model 兜底。
5. 不属于 alias 输入的 native 数字标记、模型路径及不含 alias 的 reference，继续使用现有解析规则，包括原来支持的未列出 `provider/model`。

| 输入或操作 | 结果 |
| --- | --- |
| `p/m@work`，work 已绑定 | 选择该 binding；上游 model 为 `m`。 |
| `p/m@WORK:high[1m]` | alias 规范为 `work`；effort 和窗口标记保持既有含义。 |
| `p/m@unknown` | 404，不能发送 `m@unknown` 给普通账号。 |
| `p/m@`、`p/m@work!` | 400，不能改为普通路由。 |
| 设置 `500k`、`20260801`、`_work` 为 alias | 拒绝；首字符不是字母。 |
| `p/grok-4.7@500k` | native ID，保持 `@500k`；不选择账号 alias。 |
| `p/claude-model@20260801` | native 日期标记；上游保留日期。 |
| `p/grok-4.7@500k@work` | base 为 `grok-4.7@500k`，选择 work。 |
| `p/@cf/vendor/model` | `@` 位于最后一个 `/` 之前，不拆 alias。 |
| `p/@cf/vendor/model@work` | base 为 `@cf/vendor/model`，选择 work。 |
| `p/m@work/submodel` | `@work` 是 native 路径的一部分，不是末尾 alias。 |
| `p/m:high@work` | 不作为“m 的 high effort”支持；只有 `m:high` 本来就是合法 native base 时才按那个 base 处理。 |
| 已知 native `p/m@latest`，尚无 latest binding | native literal；禁止新增会产生歧义的 latest alias。 |
| 先有 work binding，之后模型列表新增 native `p/m@work` | 该拼写报冲突；不能借列表刷新把旧引用改成普通路由。 |
| `group/g@work`、`auto/m@work` | 不为 group 增加账号 alias；group 必须通过其成员显式选择账号。 |

这是一条明确的兼容边界：未列出的、字母开头末尾 suffix 的 native model，例如 `p/m@latest`，需要先加入 provider 模型列表，才能按 native 解释。任意未知 native `@` 与未知 alias 在同一个字符串中无法同时无歧义地兼容。主 Agent 已接受这一边界；用户文档和测试必须写清，不能宣称保留所有未经列出的 native `@` 输入。

Claude dashed 和 flat `~` 处理作用于 base reference，然后保留 alias 及 effort。已删除 alias 也不能因为从 catalog 消失，导致 `p~m@old` 或 dashed spelling 绕回普通路径；识别到 qualified 意图后必须使用同一错误路径。

### 4. 配置保存 owner、目标 ID 和 retired 状态

建议在 `Provider` 增加两个字段；以下是供测试夹具和实现共同使用的数据契约，名称由主 Agent 在首轮测试前冻结。

```go
AccountAliases map[string]AccountAliasBinding `json:"accountAliases,omitempty"`
KeyAliases     map[string]KeyAliasBinding     `json:"keyAliases,omitempty"`

type AccountAliasBinding struct {
    AccountID string `json:"accountId"`
    Active    bool   `json:"active"`
}

type KeyAliasBinding struct {
    OwnerID string `json:"ownerId"`
    KeyID   string `json:"keyId"`
    Active  bool   `json:"active"`
}
```

两张 map 的 key 都是规范 alias。`AccountID` 使用账号保存记录的稳定 ID，不使用显示名、email 小写、seat 或 plan。内置订阅采用其真实登录命名空间；插件采用 `plugin:<raw provider id>` 下、按 `savedLogin.Home == plugin.Account.Key` 匹配的 saved ID。当前 `AccountIDs()`/`AccountID()` 对插件使用 provider ID 查询 `LoginID`，会退化成名字 hash，不能直接作为 routing owner。新增 routing 身份读取必须贯通 API、UI、候选、dispatch 和备份；若调整 gateway key 所见 ID，保留已有 ID 的兼容解析，不能扩大权限。`KeyID` 是当前 key 的现有指纹。key 的 `OwnerID` 在首次绑定时取当时的 KeyID，后续始终保持；显式 `ReplaceKey` 只迁移该 owner 的 `KeyID`。这是为了让另一台机器辨认“同一个 owner 换了 key”，不误判成“把旧 alias 交给另一个 owner”；不需要再生成账号或复制凭据。

例如，订阅配置可以保存：

```json
{
  "accountAliases": {
    "work": { "accountId": "login-a", "active": true },
    "team": { "accountId": "login-a", "active": false }
  }
}
```

A 的 alias 从 `team` 改为 `work` 后，`team` 保持 retired。`p/m@team` 报已停用；将 `team` 绑定给 B 被拒绝；A 可恢复 `team`，同时使 `work` retired。清除 alias 将该 owner 的 active binding 改成 inactive，账号恢复普通路由；不删除历史 owner。

key 从指纹 `key-old` rotation 到 `key-new` 后，所有属于该 owner 的 active 和 retired bindings 均保存 `ownerId: key-old`、`keyId: key-new`。将同一 key 再设置为不同 alias，通过它已有的 bindings 找到原 OwnerID；只按当前 KeyID 创建另一个 owner 会破坏历史约束，不能这样实现。

配置不变量：

- 一个 alias 只有一个 owner；一个 owner 最多有一个 active alias。
- 一个 key owner 同时只能指向一个当前 KeyID；同一 KeyID 不能对应多个 owner。
- inactive、暂时未登录、off、lapsed、key 已删除的 binding 都不能被重新分配。
- 无法读懂 registry、重复 owner、重复 active alias 或跨两张表的同名冲突均须拒绝写入；运行时不能将有问题的隔离配置当成“没有 alias”。至少对受影响 provider 关闭候选并明确报告配置错误。
- 不在旧的 `normalAccountNames` 式 normalize 中悄悄丢弃非法 bindings。setter 校验错误，文件读入保留并报告错误，未知状态不能扩大普通路由。

整份 providers.json 的读取状态属于隔离判断的一部分。现有 `load()` 丢弃错误，`allProviders()` 仍发现订阅账号，甚至旧 `.bak` 可能不含后来设置的 alias；不能把这类结果当作可信的无 alias 配置。实现须提供当前配置健康状态，区分“文件合法且没有 alias（包括合法首次使用）”与“文件不可读、整文件 JSON/字段类型非法、仅从旧备份恢复”。后者在冷启动和已有缓存的运行过程中，都必须阻止隔离状态无法确认的账号选择，并返回配置错误、保持零上游调用。旧缓存/旧备份可以用于展示和诊断，不得证明当前账号属于普通池。不得新增通用配置恢复框架；复用当前 reader、memo invalidation 和错误呈现，且不改变合法旧配置的普通路由。

有限验收覆盖冷启动和热缓存各一组损坏/不可读配置；另测存在不含 alias 的旧备份时不得恢复普通消费。权限测试不得依赖 root 下无效的 chmod，应使用真实可复现的读取失败方式。设置 API 必须在读取/验证失败时拒绝写入并保留原文件。

alias 更新执行一次 read–validate–write，修改 active 和 retired 状态必须原子保存。并发两个设置请求不能都通过唯一性检查后覆盖彼此；复用已有保存锁或增加此处必要的串行修改，不在锁内刷新账号或访问网络。

必须覆盖现有 `Provider.clone`、`withPicks`、订阅 `Save` 重建 Provider 的字段列表、GUI 普通 save 保留字段等路径。普通 provider 编辑器不能用省略字段清空 alias。设置、修改、清除 alias 仅写 `providers.json`；`AccountNames`、`logins.json`、plugin auth 以及原 key 字段字节保持原值。保存成功后通过现有 `catalog.Touched` 使缓存失效，并安排 `agent.SyncCatalogSoon` 更新模型列表。

### 5. UI 和 JSON API 使用账号 ID，显示名保持独立

建议增加：

```http
POST /api/provider/accountalias
{"id":"codex","account":"<LoginID>","alias":"work"}

POST /api/keys/alias
{"id":"relay","ref":"<KeyID>","alias":"work"}
```

alias 为空则清除。两个 API 都返回现有 `providersState` 形状，继续复用 `accountAction` 的保存、重画和错误反馈。accountalias 只接受该 provider 的稳定账号 ID；keys/alias 只接受当前 KeyID。新的 endpoint 没有旧客户端兼容负担，不增加显示名解析或名称模糊匹配；现有 accountname/keys/rename 的请求体和行为保持原样。

`/api/providers` 在订阅当前账号和各 login 行补 `id`、`routingAlias`，在各 KeyInfo 行补 `routingAlias`。login 的 routing ID 必须使用真实保存记录的稳定 ID；插件使用 `plugin:<raw provider id>` 命名空间并按 sign-in slot 读取，不能照搬当前 `AccountIDs()` 的名字 hash。现有 gateway-key ID 若保持不变，须与 routing ID 分开命名并显式适配；同一 routing ID 必须贯通 UI、API 和 dispatch。页面只需要 active alias；retired owner registry 不作为新的用户管理功能暴露。API 错误指出 alias、原因和当前账号，不返回 key/token。

账号行和 key 行分别提供独立的“路由别名”输入，与原显示名重命名并列。复用现有行内输入交互，Enter 保存一次、Escape 取消，失焦不重复提交；失败保留输入并显示具体原因。控件旁解释：无 alias 时参加普通路由；设置后只能通过 `provider/model@alias` 选择；清除后恢复普通路由。提供从该账号可服务模型构造 qualified reference 的复制入口，并让 group 编辑器可直接选择 qualified member。

所有文字走 `t()`，包含 zh、en、zh-TW、ja、de 及相同 placeholders。使用应用现有菜单，不使用 native select；点击不滚动页面，360px 宽度仍能操作，不增加左侧色条。单账号布局和多账号布局都必须有入口。Routing 页的普通 pool 展示应排除 active alias 账号，qualified member 展示其单一绑定，避免页面仍声称普通请求会分到该账号。

当前核对的 `app.js` 大小为 1,306,198 bytes，`appScriptBudget` 为 1,311,735 bytes，余量约 5.5 KB。优先复用现有 helper；不为本功能直接抬高预算，也不借功能之名重构整个 Providers 页。

### 6. 解析结果携带选择条件，凭据仍来自原账号

建议在现有 `Provider` 的请求副本中增加不序列化的路由选择条件，保存 alias 和目标 AccountID/KeyID；不要把选择条件写入真实 provider 配置，也不要让它改变 `Key`、`Account` 的来源。实际 credentials 仍在生成单账号 candidate 时通过现有 `AlsoOn`、`WithKey` 等路径引用。

建议新增的最小核心接口为：

| 接口建议 | 责任 |
| --- | --- |
| `SetAccountRoutingAlias(providerID, accountID, alias) error` | 校验订阅 owner、唯一性、native 冲突及 active/retired 原子变化。 |
| `SetKeyRoutingAlias(providerID, keyID, alias) error` | 相同规则，按 OwnerID 保持 key rotation 的身份。 |
| `ResolveModel(ref) (Provider, string, error)` | 返回带选择条件的 provider 副本、plain upstream model，或可区分原因的错误。 |
| `Provider.RoutingAlias() string` / `RoutingAccountID() string` | 只读本次请求的选择条件。 |
| `Provider.ModelRef(model) string` | 生成含 alias 的 route identity；不把它当 upstream model。 |
| `Provider.RoutingAllows(accountID) bool` | 普通请求仅允许无 active alias 的账号；qualified 请求仅允许绑定目标。 |
| `qualifiedEntries(base []Entry) []Entry` | 由基础模型和 active bindings 派生可发现条目。 |

旧 `Resolve` 的签名保持为适配层，调用新 resolver 并把错误转为 `ok=false`，避免无关调用大范围改签名；gateway 对用户输入采用有错误信息的接口，并在 stand-in 和 permissive fallback 之前识别 qualified 意图。group 和 ResolveRoute 使用同一个 resolver。函数名可以在 review 后按仓库模式微调，行为契约、序列化字段和测试 surface 需要先冻结。

`Member` 可继续通过 `Member.Provider` 携带选择条件，不必再增加第二套容易失配的 `Member.Alias`。`Member.Spelled`、复制成员、group 去重、media 去重和权限 map 都使用完整 `ModelRef`，加上现有 effort/Fast 身份；同一 `p/m` 的 `@work` 与 `@personal` 是两个成员。普通 rest、cap、concurrency、RPM 和 usage 的账号身份继续使用原 ID，不能按 alias 新开计数来绕过配额。

#### 6.1. 原始请求的账号约束先于自动模型替换

在 titles、descriptions、predictions、classifier/subagent、resume、stand-in 或同步 middleware 改写模型之前，识别并保存 client 原始 qualified reference 及其 provider/owner。未知、retired、非法或冲突引用先返回明确错误，不能先替换成配置中的其他模型再成功消费。该约束通过 request context 显式传递，不能只保存在随后会被替换的 body.model 中。

原始 qualified 请求优先于自动标题、描述、预测、子模型和 resume 模型设置；这些处理不得更换其 provider/owner，默认继续使用原始 qualified 模型。同步 middleware 如果试图改变 provider/owner，明确拒绝并保持零上游调用；其余内容改写保持原契约。异步 context hook 继续禁止修改 model，不新增 hook 权限。

| 独立入口 | 约束检查位置与结果 |
| --- | --- |
| 普通 gateway handle / Codex backend 的 title、description、prediction | 在提前处理 side call 前保存原始约束，配置中的其他模型不得取代它。 |
| serve 中 classifier/subagent 和 Codex resume | 使用 request context 中的原始约束；不得将 qualified 输入替换为普通模型或其他账号。 |
| serveAgent 同步 middleware | 改写后核对原始 provider/owner；冲突明确失败，不能仅按改写后 model 重建无约束请求。 |
| 未知、retired alias 与任何上述入口组合 | 原始引用先失败，所有其他账号和 provider 调用数为零。 |
| group 请求 | 原始输入是 group 时保留其显式成员选择；叶子 qualified 成员的失败仅由该 group 的显式其他成员承担 fallback。 |

### 7. 账号资格必须在任何“可恢复候选”之前确定

`perKeyBarred` 是主选择点，但源码确认只改它不足以实现本需求。

普通请求先从原账号/key 集合去掉所有 active alias 账号；qualified 请求先只保留 binding 的目标。此过滤发生在 plan/list/cap 分类之前。被隔离排除的账号不能进入 `aside`、`left`、`barred`、`held`、`capped`、`unlisted` 等可以稍后恢复的 candidate 集合。Routing trace 可以保存排除原因，但那是纯展示信息，不能再次转成 candidate。

必须同时处理：

- `perKeyBarred` 的账号分支 `all`，以及 key 分支“没有 out 就返回整个 provider”的分支。alias 过滤后空集合是真正的空集合。
- `onKeys` 在 plan 为空时重新构造 `{p: p}` 的兜底。只有原来就无 key、无账号的本地 endpoint 才能使用该能力；有隔离配置或 qualified 选择条件时不能用它恢复账号。
- `allowedKey` 直接枚举 `KeysOn` 的路径，以及选择 key 的其他同类调用。
- `strictKeep` 从 cap 集合恢复账号、旧 affinity/sticky、`unlistedNext`、probe、laneMate、ready、reset、sealed writer 等延迟选择。它们只能在已经满足 alias 资格的集合中操作。
- `chatGPTSearchers`、`codexAccounts`、`compactOn`/sealed compaction、`cap_careful` 的 `AlsoOn` 枚举，以及原生 compaction/redaction 的 `FallbackModels` 预判。

候选选定后，到真实 upstream 发送之前，再按当前 alias binding 检查一次资格，涵盖候选创建后用户改名/清除 alias 的情况。已经发出的请求允许自然结束，不复制到新账号、不取消其已发出的流；保存成功后新进入的请求，以及尚未发送的重试，必须按新配置执行。检查不持有跨网络调用的配置锁。

无 alias 的旧配置仍使用当前多账号顺序、Smart、rotate、usage、pace、weight、free、fast、rest/cap 等逻辑。旧 strict affinity 指向后来有 alias 的账号时，该账号不能被恢复；剩余普通候选按现有“旧 responder 已不在计划中”的规则处理，不新增会话迁移机制。

### 8. fallback、header 和权限取交集

直接 qualified 请求的 plan 不展开其 provider 的 `Fallback`，也不触发 auto/found group、bare model、agent 默认模型等替换。模型不被目标账号允许、账号关停、rest、cap、上游失败，均不能解除选择条件。允许在同一账号上执行已有的协议协商或合法重试，但不能把失败转换为另一账号/provider。

普通请求保留已配置 fallback。p 的普通池为空时可以尝试已配置的 q；q 的普通 target 仍只使用无 alias 账号。所有允许的普通 candidates 都为空时，明确返回“普通路由无可用账号”，提示 qualified 写法。不要通过按 alias 枚举来补空计划。provider fallback 本来可指向 group；此时只使用该 group 现有语义允许的显式 members，不自动添加 alias variants。不会自动生成新的 qualified fallback 配置。

`X-Magpie-Account` 只能进一步收窄：普通 model 加 header 指向有 alias 的账号时拒绝，提示使用 qualified reference；qualified model 的 header 指向不同账号时拒绝；指向同一账号时允许。header 不能解除 models/accounts 限制，也不能传给 upstream。

模型权限沿用主 Agent 批准的包含关系：

- models 允许 `p/m` 或匹配它的 `p/*`，允许该 base 的普通和 qualified variants；accounts 列表继续限制实际可用账号。
- models 只允许精确 `p/m@work`，不能使用 `p/m`、`p/m@other` 或别的 provider。不能先去掉 alias 再用精确 qualified 条目授权 base。
- groupNamed/memberAllowed 的既有授权语义保留；`groupKeeps`、权限候选清理和 catalog 过滤都用完整 member identity，不能把两个 alias 合成一个键。
- `magpieChose` 对 models 的现有豁免不豁免账号隔离；magpie 自己发起的 search/decision/image 等普通 target 同样不能花 alias 账号，accounts 限制保持现状。

对于 qualified catalog entries，即使调用方只受 accounts 限制而未受 models 限制，也必须只展示其有权使用的那个目标；不能沿用 `entryAllowed` 的 unrestricted shortcut 跳过检查。普通 entries 沿用已有目录过滤和展示规则；它们仍可展示，实际请求再按普通池与 fallback 执行。

### 9. catalog 派生 qualified entries，自动 group 仍只看基础模型

保留 `providerEntries` / `Exposed` 的基础模型含义；不要把 alias 加进 vendor 的 `Available`、价格索引、retired model 列表或 `catalog.Model.ID`。在 catalog 的对外组合层为 active bindings 派生 entries，保留 `Entry.Model` 为 plain model，使用 `Entry.ID = p/m@alias`，并增加显式标记 `RoutingAlias` 和 `BaseID`。派生 entry 的 Provider 携带同一个选择条件。

`Catalog`、`CatalogFor`、对外 Served/picker 的相应组合结果包含 qualified variants；组自动发现、Match/regexp、templates 和 same-model 合并直接使用基础集合。GUI `groups.models` 可同时提供基础和派生选项，但 `matchedBy`、`patternCount` 等必须跳过带 `routingAlias` 标记的条目。这样 `p/*` 的 group Match 不会把所有 alias 自动加进组；只有成员选择器点击或手写的完整 qualified reference 才是显式成员。gateway key 的 models wildcard 与 group Match 是不同语义，不应共用“跳过 qualified”的规则。

派生范围按账号实际可服务的基础模型计算，遵守 provider on/off、用户 model picks、account models、key plan/protocol 的明确限制；不为了派生列表触发真实 vendor 请求。off/lapsed/缺失账号不新增可用 qualified entry，临时 rest/cap 不导致 model 一会儿从 picker 消失、一会儿回来。无 key、无账号的本地 endpoint 没有 alias 控件或 derived variants。普通 entries 即使只有 alias 账号仍按主 Agent 决定保留。

derived entry 从 base 继承 context、output、modalities、reasoning/effort、API、fast、price 等模型能力。`EntryPriceIn`/`EntryPricer`、settings 中的 base model preferences 和 wires 都以 `BaseID`/plain Model 读取；alias 不隐式创建一套价格或覆盖窗口设置。保留每条 qualified reference 本身的 agent 可见性、隐藏和排序功能。

agent wiring 沿用 catalog 原有写入格式，qualified entries 的 ID 必须完整进入各 agent 的模型配置，不能按 `e.Model` 丢掉 alias。尤其 `CodexOwn` 不能把 qualified Codex entry 当作原生 bare GPT slug；否则选择 `codex/gpt-5.5@work` 又会写回 `gpt-5.5`，重新使用普通账号。Claude dashed、Desktop alias、MiniMax flat 的映射必须可以 round-trip 到 qualified reference。

### 10. group 保留失效引用，其他显式成员仍可 fallback

`Group.Members` 保存完整 `p/m@alias[:effort]`。同模型两个 alias 不去重；Weights、Off、Fast、Pick、rules 以及 drawingTries 等路径均保留其身份。嵌套 group 沿用现有路由策略和权限，叶子成员携带自己的选择条件；成员 provider 自己的 fallback 不进入 group。

保存 group 验证语法和现有结构约束，不要求 alias 此刻在线或存在。alias 被删除、账号未登录、provider off 时，保存字符串保留，GUI 显示具体 unavailable 原因。请求 group 时仅排除该失效成员；若还有显式成员可用，按原 group routing fallback。全部成员失效时，错误指出 alias 或绑定账号问题，不能只显示泛化的“unknown model”。

例如 group 是 `[p/m@work, q/m@backup]`。删除 work 后，group 配置仍保留 work 引用，页面标为停用；请求该 group 可以由 backup 回答。直接请求 `p/m@work` 则始终失败。后续给第三个账号设置 work 会被 owner 历史拒绝，不会让旧 group member 重新指向第三个账号。

共享/导入 group 时，未知 qualified member 不能按“同一个基础模型在另一 provider”自动替换。沿用 ImportGroup 的 missing 报告/拒绝规则即可；不能剥 alias、映射到 found group 或普通账号。已存 group 和 sync 恢复的 group 保留完整引用。

### 11. 各协议和特殊路径都遵守同一选择条件

| 路径 | 必须保持的行为 |
| --- | --- |
| Chat、Responses、Messages、Gemini、Ollama | 统一解析及候选约束；upstream JSON model 或 Gemini URL 只含 plain model；native `@` 仍保留。 |
| count_tokens / Gemini count | 未知、retired alias 或权限错误先明确拒绝；已知有效目标可按现有规则本地估算，但不能换账号或 provider 来计数。 |
| image generation/edit、video start | 同一个 alias 只选绑定账号；group 失败只尝试显式成员；同 base 两个 alias 不被 drawingTries 合并。 |
| video poll/download | 使用视频创建时记录的原 key；这属于既有资源继续操作，不重新选择账号。该 key 不存在/off 则明确失败，不能切换 key。alias 改名不把现有视频资源转给另一账号。 |
| embedding、rerank、transcription、System One | 复用统一 resolver/账号资格；onKeys/decision 的空候选兜底不能复活原 provider。 |
| Codex bare turns / compact / search | 经过 magpie 的原生 relay 优化、单账号路径、sealed writer 也执行普通隔离；qualified 请求不能落回 bare GPT 路径。 |
| plugin subscription fetch | 插件仍负责 sign-in、refresh、models、usage、requests；fetch 收到 plain model 和正确的原账号 auth，不为 alias 复制 auth。 |
| middleware/context hook | 先保存原始 qualified 约束。同步 middleware 可看到客户端 reference；改写 provider/owner 时明确失败，不能解除原始绑定。异步 context hook 保持禁改 model。最终 fetch/translator 获得 plain model。 |
| remote magpie | 本地 alias 只选择本地持有的 remote key，剥掉本地 alias 后发送原 remote model ID；native base 中原本的 `@` 不删，未知本地 alias 不转交远端解释。 |
| compaction/redaction/careful 预判 | direct qualified 只考虑实际目标，不读不会跟随的 provider fallback 来决定压缩或脱敏；普通 side call 仍执行隔离。 |

订阅账号已有 off/paused 逻辑在“没有其他账号时又使用自身”这类分支，不能覆盖 alias 隔离。provider 自身 off 时，qualified 也不可用；开回后仍绑定原 owner。只有一个账号且有 alias，或者所有账号都有 alias，普通 provider 池均为空；绝不能用第一账号、默认 key、原生 relay 或 strict stick 补回。

### 12. alias 修改、删除、重命名和 sync 不转移 owner

本次不自动改写所有 group、agent、profile 或用户配置中的 alias 引用。理由是修改 alias 表示改变路由入口；批量重写会跨多种外部配置格式，也会掩盖旧引用已失效。保存成功后 UI 说明旧 reference 将失败，提供新的 reference；agent 模型目录更新新增/移除的条目，但不把已选旧 reference 替换为普通模型或另一个 alias。

要专门核对 `Reseat`/SyncCatalog 等行为：不得把此次 alias 消失当成 provider 被删除，并自动把旧 qualified pick 改为另一个 model。用户的旧 pick 可以保留并明确标为不可用，实际请求返回 alias 错误；base entry 仍在不代表旧 qualified pick 可以改写为 base。

显示名 rename、plan 变化和原有稳定登录 ID 保持时，binding 完全不动。插件账号已通过 `plugin:<raw provider id>` 命名空间和保存记录的 Home/sign-in slot 维持稳定身份：`pluginLogins` 改 User 时保留 savedLogin.ID。本功能必须利用该身份；同一个 plugin slot 改显示名后，普通请求仍不得使用它，qualified 请求仍选择原账号。不能因旧 `AccountIDs()` 的名字 hash 改变而解除隔离。对于确实没有稳定身份依据的新外部登录，不按相似名字猜测转绑；已知同一 slot 的重命名不属于这种情况。

provider rename 沿用现有 rename 的 provider prefix 改写和旧 ID 兼容；alias registry 随同一个 provider 记录移动，qualified suffix 和 owner 不变。这与“修改 alias 不批量改写引用”是两个不同操作。key Rename 只改显示名；ReplaceKey 显式迁移 owner 的当前 KeyID；RemoveKey/ForgetLogin 保留 alias owner 记录，使直接旧引用失败。重新加入同一身份可以恢复绑定，不允许另一身份认领历史 alias。

sync/backup 规则如下：

1. `AccountAliases` 属于 portable provider 设置，随 providers 传递；目标机器缺少相同 AccountID 时保持 unresolved，不复制订阅 credentials、不自动绑定别的账号。
2. 带 keys 的 backup/sync 携带完整 `KeyAliases`。key rotation 通过不变 OwnerID 和变化 KeyID 表示；历史和 active binding 与对应 key 一起恢复。
3. 不带 keys 的导出清除完整 `KeyAliases`，包含 active 和 retired 记录。恢复到已有 provider 时，由 `KeepSecrets` 把本机 keys 与本机完整 KeyAliases 一起保留；全新机器不导入别台机器的 key alias。不能只保留 alias 字符串，再绑定本机默认 key。
4. Restore/Mirror 在现有 provider 上应用输入前合并 owner 历史。相同 alias、不同 owner 时拒绝此次 providers 更新并说明冲突；不能靠“新版本胜出”改 owner。相同 owner 的 active 状态与当前 KeyID 按被选择的配置版本处理，未被选择的历史记录以 inactive 合并，防止旧名被遗忘后复用。
5. 来自旧版本、完全没有 alias 字段的 provider 不清掉本机已有隔离；保留本机对应 registry。新格式的“全部清除”通过保留 inactive bindings 表达，无需用字段缺失表示。restore 先验证整个 providers 部分，再写入，不能先覆盖一半后发现 owner 冲突。
6. providers 之外的 sync 冲突机制、Undo、KeepLogin 等本机字段维持现有职责；hash/collect/restore 和 Undo 都携带本次应携带的 registry。回退到较旧快照也不能把已知 owner 历史静默抹掉。

历史 owner 的保证以配置保留为边界。用户手工删除 registry、删除整个 provider 后复建、使用不认识该字段的旧 binary 重写配置，均可能丢掉历史；本次不增加全局永久注册服务、provider 删除历史或防降级框架。正常 UI alias 操作、账号/key 生命周期及支持的 sync/restore 路径必须满足 owner 约束。

### 13. 错误、响应和记录保持具体

| 触发 | 响应要求 |
| --- | --- |
| alias 语法非法 | 400，指出合法格式；零上游调用。 |
| 未知或 retired alias | 404，分别指出“不存在”或“已停用”；不能走 stand-in、普通账号或 fallback。 |
| active binding 的账号不在本机/已删除 | 404，指出绑定账号不可用；不列出未授权账号或凭据。 |
| provider off | 保留现有 off 语义，指出 provider 关闭；不尝试账号。 |
| reserved alias 与新 native ID 冲突 | 409，指出引用歧义；零上游调用。 |
| gateway key 的 model/account 不允许 | 403，不能借 alias 绕过；错误不泄露其他账号 alias 列表。 |
| account models 或明确 plan 限制 | 保留对应拒绝状态和原因，qualified 不尝试其他账号。 |
| rest、cap 或账号额度不足 | 保留适用的 429/额度错误及 Retry-After；qualified 只说明该绑定目标。 |
| direct qualified 的 vendor 失败 | 返回该目标最终错误；其他账号和 provider 的计数保持零。 |
| 普通路由及其允许 fallback 都无候选 | 404，说明普通路由无可用账号，可提示 qualified 的使用方式。 |
| group 全部显式成员不可用 | 保留现有 group 错误框架，补充失效 alias 原因；不回退到未知 base。 |

不要求每个 vendor 的细分错误改成统一状态码；核心可观察保证是指向具体目标、错误原因可辨认，而且没有未经允许的上游消费。避免为报错列出调用方无权看到的全部 alias。

upstream body/URL 只使用 plain model，已有 ModelWires 仍在其原阶段作用于 base。响应 body 的 model 保持 vendor 原值；`X-Magpie-Provider` 和 `X-Magpie-Model` 继续指示实际 provider 和 base model，既有 response-model:member 规则保持。Routing 页保存完整请求 reference 和实际账号，`usage.Record.Model`/price 使用 plain model，`Requested` 使用 qualified reference。可选 OTLP 目前会发送 `Requested`，因此用户配置了它时 alias 也可能被发送；本次不新增遥测字段或静默宣称 alias 不出本机。

### 14. 已核对的源码依据

下表列出决定本方案的源码事实，均在 `96a703a5` 上核对；未来实现文档应引用符号，不依赖这里的行号。

| 事实 | 源码 |
| --- | --- |
| `AccountNames` 仅用于展示，setter 只保存 provider | [account_names.go](/data/code/github/magpie-account-alias/internal/provider/account_names.go)，`SetAccountName`、`AccountShown`。 |
| 登录有稳定 ID；plugin saved ID 按真实命名空间及 sign-in slot 保持，旧 AccountIDs 查询不能直接作为 routing owner | [logins.go](/data/code/github/magpie-account-alias/internal/provider/logins.go)，`loginID`、`LoginID`；[account_models.go](/data/code/github/magpie-account-alias/internal/provider/account_models.go)，`AccountIDs`、`AccountID`。 |
| 新字段会在 clone、withPicks、订阅 Save 重建处丢失 | [provider.go](/data/code/github/magpie-account-alias/internal/provider/provider.go)，`clone`、`withPicks`、`Save`、`store`。 |
| 第一把 key 与附加 keys 的结构不同，rotation 有显式迁移点 | [keys.go](/data/code/github/magpie-account-alias/internal/provider/keys.go)，`KeyList`、`KeyID`、`RenameKey`、`ReplaceKey`。 |
| `Resolve` 接受任意 provider/model；不能先用它判 native | [models.go](/data/code/github/magpie-account-alias/internal/provider/models.go)，`resolveIn`；native 真实 `@` 处理见 `cursorFast`、`grokWindow`。 |
| 基础、catalog、group、served 集合共享来源，派生层需分开 | [models.go](/data/code/github/magpie-account-alias/internal/provider/models.go)，`providerEntries`、`catalogKeeping`、`Served`；[groupmatch.go](/data/code/github/magpie-account-alias/internal/provider/groupmatch.go)，`matchesIn`。 |
| group 去重只用 p.ID/model，需加入选择条件 | [group.go](/data/code/github/magpie-account-alias/internal/provider/group.go)，`membersOff`、`ResolveRoute`；[groupeffort.go](/data/code/github/magpie-account-alias/internal/provider/groupeffort.go)，`Member.Spelled`。 |
| key 无候选会重建 provider；fallback 无条件展开 | [fallback.go](/data/code/github/magpie-account-alias/internal/gateway/fallback.go)，`perKeyBarred`、`planPaused`。 |
| strict affinity 可从 cap 集合恢复旧账号 | [affinity.go](/data/code/github/magpie-account-alias/internal/gateway/affinity.go)，`strictKeep`、`strictOnly`、`affine`。 |
| `allowedKey` 直接枚举，`onKeys` 再次恢复空候选 | [key_models.go](/data/code/github/magpie-account-alias/internal/gateway/key_models.go)，同名函数及 `groupKeeps`、`entryAllowed`。 |
| Codex 只有一账号时可绕过 plan；搜索枚举 AlsoOn | [codex_backend.go](/data/code/github/magpie-account-alias/internal/gateway/codex_backend.go)，`codexAccounts`、`compactOn`、`sealedCompact`；[codex_search.go](/data/code/github/magpie-account-alias/internal/gateway/codex_search.go)，`chatGPTSearchers`。 |
| GUI 有独立显示名 API，普通 save 手工保留字段 | [providers.go](/data/code/github/magpie-account-alias/internal/gui/providers.go)，`providerInfoWith`、`provider` actions、`keys` actions。 |
| GUI group 模式匹配直接遍历 groups.models | [routing/client.js](/data/code/github/magpie-account-alias/internal/gui/assets/plugins/routing/client.js)，`matchedBy`、`patternCount`、`splitMember`。 |
| agent 模型列表沿用 Entry.ID，CodexOwn 目前不区分 alias | [route.go](/data/code/github/magpie-account-alias/internal/agent/route.go)，`magpieModels`；[visible.go](/data/code/github/magpie-account-alias/internal/provider/visible.go)，`CodexOwn`。 |
| SyncCatalog 可异步刷新，必须防止旧 pick 被错误改写 | [sync.go](/data/code/github/magpie-account-alias/internal/agent/sync.go)，`SyncCatalog`、`SyncCatalogSoon`。 |
| keyless 会只清 key，KeepSecrets 放回本机 key | [backup.go](/data/code/github/magpie-account-alias/internal/backup/backup.go)，`withoutKeys`；[keep_secrets.go](/data/code/github/magpie-account-alias/internal/provider/keep_secrets.go)，`KeepSecrets`；[provider/backup.go](/data/code/github/magpie-account-alias/internal/provider/backup.go)，`Restore`、`Mirror`。 |
| provider rename 已按前缀改写引用，group import 会找替代模型 | [rename.go](/data/code/github/magpie-account-alias/internal/provider/rename.go)，`Rename`、`renamedRef`；[group_share.go](/data/code/github/magpie-account-alias/internal/provider/group_share.go)，`ImportGroup`。 |
| 响应按 base 标识，OTLP 发送 Requested | [member.go](/data/code/github/magpie-account-alias/internal/gateway/member.go)，`noteMember`；[otel_wire.go](/data/code/github/magpie-account-alias/internal/otlp/otel_wire.go)。 |

已读取的 owning references 包括 providers-accounts、gateway-routing 的模型解析/候选/affinity/权限/验证部分、provider-plugins 的 ownership 和 sandbox 约束、sync-backup，以及 gui-shell、agent-wiring、Claude subscription bridge 中相关 runtime 和配置边界。实现完成后，主 Agent 应把已实现契约更新到这些 references 的相应归属处；本设计不把未实现方案写成当前事实。


## 方案 review 决策记录

- F1：接受。源码 `pluginLogins` 已按 slot 保持 saved ID；routing owner 若继续使用名字 hash，会在显示名变化后解除隔离。实现和测试必须读取真实稳定身份。
- F2：接受。title/subagent/resume 和同步 middleware 在 resolver 前能换 model；必须在这些入口之前保存原始 qualified 约束，不能仅复核最终 body.model。
- F3：接受。配置读取失败会丢失 picks 并继续发现账号，旧备份也不能证明当前隔离状态；冷启动、热缓存及旧备份分别纳入有限测试。
- 主 Agent 负责最终验收；接受方案 review 不代表功能或测试已完成。

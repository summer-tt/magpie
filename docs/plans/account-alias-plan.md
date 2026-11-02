# 账号 routing alias 实施计划

本轮再次同步上游，基线固定为 `upstream/main` 的 `2741b749f95c8548d1f9107c5cfe474ed1d05c8f`（`v0.1.2888`）。此前基于 `50265256` 的交付为 `ff179ac2`，历史结果保留在本计划 §3.1–§3.5，本轮记录见本计划 §3.6。现有六笔 baseline 测试修复均未被本轮上游替代，继续保留在 Bailian AK/SK quota、账号 alias 两笔功能 commit 之前。用户明确要求不额外修复未解决的 baseline 问题；所有失败保留原始记录，不改称 PASS。

行为契约见《账号 routing alias 设计方案》§1–§13（[设计方案](account-alias-design.md#1-用户可以选择账号已有普通路由继续工作)）；原始请求约束见该文档 §6.1，配置错误处理见该文档 §4。


### 1. 先冻结行为与 fixture 契约，再分离测试和生产作者

主 Agent 是范围、产品语义、接口冻结、调度、整合和最终验收的责任人。设计作者提供本报告并处理方案 review；本轮没有创建测试或生产代码。计划 reviewer 先核查语法优先级、owner/rotation/sync、catalog 与普通路由隔离是否自洽，主 Agent 逐条记录接受或拒绝理由。

测试作者和生产作者必须是不同的实际作者，所有后续修复也一样：测试作者先补回归，生产作者再修改实现；生产作者不能改 fixture、断言或测试 helper 让实现通过。可以让熟悉同一批测试的测试作者继续补测试，不必为每个文件增加新角色。主 Agent 分配单一写入线程或独立 worktree，避免共享 index、日志和同一文件并发改写；本设计作者不启动其他 agent。

先冻结 HTTP endpoint、JSON 字段、alias grammar、状态/错误分类、catalog 标记和 acceptance IDs，再进入 RED。内部 Go helper 签名不应成为首轮测试能否编译的前提。

### 2. RED 必须是可运行的行为失败

首轮测试只依赖基线已有的入口和类型：`provider.Path`/现有 Save、GUI Handler、gateway `Front`/真实 server stack、`provider.Catalog`/`CatalogFor`、`agent.SyncCatalog`、backup/Restore/Mirror 等。需要新字段时，通过 `encoding/json` 的 map 或原始 JSON 写入临时 providers 文件，再调用现有 cache invalidation；不要在测试源码中引用尚不存在的 `AccountAliasBinding`、新 setter 或 `RoutingAlias()`。未来字段在旧代码中被忽略，旧行为会被真正执行并触发断言失败。

HTTP API 测试向现有 action mux 发新 action；旧代码返回 404，而预期设置成功、保存后 registry 生效。这是行为失败，不是缺符号编译失败。UI 测试加载实际旧页面，用未来响应 JSON fixture；旧页面找不到对应控件或没有正确 POST，也属于行为失败。目录测试调用现有 Catalog，断言 qualified entry 缺失。provider 保存/backup 测试通过原始 JSON 检查 round-trip，不要求新 Go 字段先存在。

为避免“API 没实现就挡住所有路由测试”，路由、catalog、group 和权限测试自行用 raw JSON fixture 注入 registry；它们不依赖 alias setter 先 GREEN。再另设一条通过 UI API 设置 alias、读取目录并发送请求的最小真实集成路径。

每份隔离夹具先证明合法普通请求可以通过完整 server stack 到达 fake upstream 并成功，确认 auth、guard、body、协议、模型有效，然后才运行“不得访问 upstream”的场景。fake upstream 按 account header/key 记录调用和收到的 model；记录须加锁并区分模型列表、usage 等后台调用。禁止用请求未到目标的 401、fixture setup 失败、compile error 或 skip 充当 RED。

首个可验收里程碑是：同一个 fake key provider 有 K1 无 alias、K2 为 work；旧代码上“普通请求不能用 K2”与“qualified 请求必须剥 suffix 且只能用 K2”的测试均出现明确 FAIL；新代码上普通与 qualified 各成功到达正确 key，上游 model 为 base，K2 失败时 K1 及其他 provider 调用数为零。订阅账号再使用现有 fake Codex 或本地 plugin fixture 验证同样的身份保证。

兼容性测试可能原本就是 GREEN，例如 native `@500k`。它们是回归保护，不伪称新增行为的 RED。实现后 reviewer 通过禁用 alias 过滤、打开 direct fallback、删除 owner 保护等有限 mutation 证明关键测试能捕获真正的破坏；仍由测试作者修改测试代码。

### 3. 按依赖推进的实施批次

| 批次 | 测试作者先交付 | 生产作者随后交付 | 完成判据 |
| --- | --- | --- | --- |
| 0：方案冻结 | 复核 raw JSON fixture 可用、基线普通调用成功；列出每项 RED 目标 | 无功能实现 | 主 Agent 审定文档、接口和作者归属，项目两份工作文档已落盘。 |
| 1：最小真实路径 | key provider 的普通隔离、qualified 成功、失败无 fallback；setter/catalog 的独立行为 RED | registry 核心、resolver、主候选过滤、API 与最低限度目录派生 | API 设置 → catalog 可见 → qualified 正确账号成功；负例零越权调用。 |
| 2：身份和完整隔离 | 订阅/插件、single/all alias、旧 strict/sticky、header、rest/cap、并发改名、空候选恢复 RED | save/clone/withPicks、所有候选分类及 onKeys/allowedKey、dispatch 复核 | 两类身份都不会被旧路由恢复；原无 alias 行为保持。 |
| 3：group 与权限 | 同 base 不同 alias、失效成员 fallback、Match 不扩展、models/accounts 交集 RED | 完整 ModelRef、group 展开/去重/trace、权限检查、group picker 数据 | group 仅消费显式成员，精确 alias 权限不扩大。 |
| 4：协议和特殊路径 | 参数化协议测试及 Codex/media/decision/fetch 旁路 RED | count、media、Codex relay/search/compact、decision、remote、compaction/redaction 等 | 每个独立选账号入口有行为证据；upstream 仅 base。 |
| 5：完整可发现性和 UI | API JSON、真实页面行内编辑、agent config round-trip、旧 pick 不改写 RED | UI/i18n、derived catalog、agent/catalog adaptors、CodexOwn 和 flat/dashed | 用户可在 session 模型选择里选 qualified；普通列表保留，自动 group 不增加 alias。 |
| 6：生命周期与 sync | rename/clear/owner reuse、key rotation、backup/keyless、owner 冲突、Restore/Mirror RED | 生命周期迁移、owner 历史合并、keyless、hash/undo | 旧引用不会转绑；缺 credentials 明确失败，无凭据复制。 |
| 7：整合和验收 | 必要回归补充；review mutation 证据 | 仅修复已发现缺陷，更新 owning references 和用户说明 | 全部有限矩阵和适用仓库检查通过，review 无阻塞。 |

批次按依赖推进，单个批次可先做完独立测试，不必等待无关 UI 或全量 suite。batch 1 的最小 catalog 不能作为完整目录可发现性验收；batch 4 不能以“核心 plan 已改”代替其他入口。若一轮证据发现入口或身份模型假设不成立，主 Agent 调整任务边界；生产作者不得自行缩小支持范围。

### 3.1. 历史验证记录：96a703a5 基线

以下结果绑定最初基于 `96a703a5` 的候选，不作为本轮 rebase 后的验收结论。生产作者与测试作者始终为不同实际实例；生产作者未修改测试、夹具或阈值。后续测试在独立 worktree 编写，主 Agent 按文件 SHA 原字节整合。设计阶段的首批结果同样仅保留为历史证据。

| 已核实批次 | 真实结果与范围 |
| --- | --- |
| 核心、身份与配置健康 | 第三批六包 58 个顶层测试 PASS；包括 F1 稳定 plugin slot、F3 当前配置健康与旧备份展示语义。临时 Bun 1.4.2 下 plugin 用例实际执行；此前因旧 Bun 跳过的相关用例已补跑并 PASS。 |
| 媒体与自动模型绑定 | 10 个顶层 PASS；真实 title/description/prediction、classifier、subagent/resume 正例及未知/retired 的零消费断言已执行。 |
| setter、group、权限与预判 | 原四项补测、后续九项服务/预判测试及八项 health/allocation 回归均 PASS；普通 fallback 按完整 reference 去重，qualified 不扩展不可执行 fallback。 |
| auth 与 RPM | 先取得 3 个顶层、10 个子例的有效 RED，再全部 GREEN；旧 auth/RPM 与媒体等 27 个顶层兼容测试 PASS。decision 原先缺 live catalog 的两项前置失败已单列修正，未伪称 RED。 |
| video owner 与资源授权 | 最终 5 个新顶层及 35 个旧兼容测试全部 PASS；包含 remote/single-plan 第二 key、资源保持 creating key、models/accounts 交集、retired alias 的历史资源授权，以及裸模型名兼容。 |
| 最终 race | gateway、gui、provider、agent 四包，85 个顶层 PASS，FAIL/SKIP/数据竞争警告均为 0。 |
| 有限 mutation | 独立副本正基线 5 项 PASS；分别令 `RoutingCurrent` 和 `RoutingAllows` 始终为 true，目标行为断言均使测试失败；临时副本恢复，主候选未变。只证明这两类破坏可被捕获，不扩称整个矩阵的 mutation 覆盖。 |

最终 Go/race/mutation 证据位于 `/tmp/account-alias-final-validation/`；后续定向日志位于 `/tmp/account-alias-final-implementation/`。原失败、夹具修正与 GREEN 日志分别保留，不覆盖证据。运行绑定的源码和文件 SHA 见对应 manifest。

完整 GUI 首轮正常结束，但 Chromium 为 7683 项、7554 PASS、129 FAIL，WebKit 为 7668 项、7384 PASS、284 FAIL；不能写成全绿。相同环境的失败文件基线分别复现大量失败。主 Agent 接受加权 key 名称裁切为新增回归；第一次 CSS 修复又造成空 alias 入口不可点击和 hover 名称过窄，已保留 RED。第二轮 CSS 下，完整 alias 编辑/复制、picker 与 key-weight-fit 三文件在两个引擎各 46 PASS，FAIL/cancelled/skip 均为 0。reviewer 随后发现旧 hover selector 测到 alias 而非 Remove；独立测试作者仅修正真实 Remove 的唯一定位，保留全部原断言并增加身份校验，两个引擎各 8 项重跑全部 PASS。这 16 次重跑不增加独立测试场景数。其余候选独有时序、菜单、真实小位移等失败按具体证据保留，不能仅按标题差异认定全部为回归或全部为基线。

最终完整 Go 正常结束并返回 exit 1：76 个包通过，4 个包中有 10 个顶层失败，无 panic。相同环境的干净基线完整 Go 也返回 exit 1，有 12 个顶层失败，其中 8 项与候选相同；候选独有 `TestHowInstalled`、`TestCodexBehindReverseProxy`。前者此前的基线单测已失败；后者的单测曾通过，不能将本轮具体差异一概称为已证明与本次无关。两份完整日志与逐项对照位于 `/tmp/account-alias-final-validation/{full-go.log,baseline/full-go.log,full-go-final-comparison.json}`。同步两项本次仅在基线失败，不把此前的时钟对照改称本次结果。Linux nogui/Windows 的最终 vet/build 四项均 exit 0；宿主默认 native GUI build/vet 缺系统开发依赖，不计为通过。

有限矩阵不扩称所有 provider/协议/状态组合均已专测。AA-08 中 all-invalid group 及 GUI 的具体 alias/owner 诊断仍为非阻塞 P2 未完成子项；video POST 已局部保留具体 alias 错误，不等于通用 group 诊断已完成。嵌套 group、provider-off、实际 Undo 等未专测边界仍按原测试作者报告的覆盖范围处理。最终独立增量 review 已结束：R1–R4、Remove 定位和 GUI 文档残留均关闭，无新增 P0/P1 生产问题；完整 Go 失败、完整 GUI 部分差异未归因及 native GUI 依赖缺失使整体判定保持 BLOCK，批次 7 尚未完成。

### 3.2. 本轮 rebase、Bailian 共存与验证

本轮从最新 fetch 的 `ce2e81da` 开发，alias rebase 后为 `920e003c`，保留既有 Bailian 功能后为 `d09e681f`。后续生产与测试修正仍保持实际作者分离；主 Agent 只整合经 SHA 核对的文件。运行结果按源码、测试和环境绑定，不把不同版本的 PASS 相加为独立覆盖。

| 验证对象 | 已确认结果及边界 |
| --- | --- |
| 上游语义整合 | 保留 `OwnModelID` / `ListedModelID` 权限映射、AtOnce、Message Thread 拒绝、`IconFrom` 和上游新增 account settings。两个新增模型权限/AtOnce 组合先在干净上游 RED，整合后 PASS。 |
| 统一候选功能定向 | 七包 141 个顶层测试 PASS，FAIL/SKIP 为 0；包含 alias、Bailian、新 quota history 回归及原脚本预算检查。记录为 `/tmp/account-alias-upstream-validation/focused-final.log`。 |
| provider 完整测试 | quota history 修复整合后，1654 个顶层测试 PASS，0 FAIL、6 SKIP；跳过项为平台专属、被父测试启动的子进程和无适用对象用例。记录为 `provider-full-final.log`。 |
| quota history 共存修复 | 干净的共享 history cache 上，失效 generation 原先会修改嵌套 map/slice，随后被另一账号有效更新写盘。独立测试三次稳定 RED；生产修复把共享修改、去重时间和写盘放入同一次成功 publication。新增测试 1 PASS，兼容 38 PASS，同组 race 38 PASS，无数据竞争警告；独立 reviewer 已关闭该问题。日志位于 `/tmp/account-alias-quota-fix/`。 |
| alias race | 四包 87 个顶层测试 PASS，无 FAIL/SKIP/数据竞争警告；这次运行先于 quota history 修复，不能与后续 history race 合称一次完整 race 验收。 |
| 平台检查 | quota history 生产修复整合后，Go 1.26.8 下 native GUI、Linux nogui、Windows 各 vet/build 共六项 exit 0。使用具备 GTK/WebKit 开发依赖的隔离验证镜像，未修改宿主依赖。记录为 `/data/tmp/magpie-alias-upstream-validation/platform-final-summary.log`。 |
| GUI 功能专项 | 早期五文件专项在 Chromium/WebKit 各 56 PASS。随后修复空 alias、active32 与编辑态账号行布局，冻结 CSS `661b408f`、JS `a2bd8c01` 后，三个完整 alias/picker/key 文件各引擎 50 PASS，原账号行两个文件双引擎共 4 PASS，模型标签文件双引擎 60 PASS，均无 FAIL/cancelled/SKIP。新增 8 项行内用例包含在完整文件中，不重复累计。独立 reviewer 在限定 UI diff 中未确认 P1/P2 阻塞；DOM/Tab 顺序保持原状，不声称按视觉位置重新排序。 |
| 当前 GUI 完整执行 | 首轮 16 个大 shard 均达到 550 秒，保留为未完成历史。后续按固定清单每批 24–25 文件执行：候选 `478087b9` 的 1126 文件、干净 baseline 的 1122 文件均已取得完整结果。长滚轮文件原有 15 分钟预算，改用其既有 `MAGPIE_OPEN_WHEEL_HEARD` 参数分区覆盖全部 160 个子用例，两侧均 PASS。完整 suite 仍失败；执行计数含嵌套及分区重复父项，不能当独立缺陷数。 |
| 完整 GUI 差分 | 对 1122 个公共文件按文件和完整测试名称比较，不含父测试失败汇总：382 个失败签名共同，29 个只在候选出现，72 个只在 baseline 出现。这是单次观测；不把单侧失败自动称为回归或随机失败。低并发成对复验已覆盖这些差异；部分未再现，部分在原 baseline 相同用例中出现过。最后两项未解释差异分别定位为历史日期按钮误选和托盘异步请求计数竞争，其纯 baseline 定向修正已验证。原完整 suite 的共有失败仍未解决，全部原日志及不完整批次证据保留。 |
| 最终 UI 后 Go 与平台复验 | GUI Go 包 560 PASS、4 项与原全量一致的条件 SKIP、0 FAIL。冻结候选的 native GUI、Linux nogui、Windows 各 vet/build 六项均 exit 0；Windows 只做交叉编译，未宣称原生执行。原 `TestCoreScriptBudget` 真实 PASS：app.js 1312021 字节，12 个 core 脚本 1533802 字节，原阈值未改。 |
| Bailian 独立提交边界 | 只含 Bailian AK/SK quota 及 history 修复的版本，两包 46 个顶层测试 PASS，包括 `CoreScriptBudget`，无 FAIL/SKIP。把最终候选已有的两段纯注释精简提前归入 Bailian，净减少 567 字节，运行语句和最终候选源码不变，预算阈值未改。 |
| 完整 Go 失败对照 | 本轮首次完整运行 83 包通过，agent/cli/gateway/gui 四包失败，gateway 触发 7 分钟包级超时。干净 `ce2e81da` 已复现对应测试失败；gateway 在 9 分钟预算下 456.086 秒完成，TTFT 两版单测均通过。已有失败涉及 PATH 缓存刷新、shell/容器假设、Codex 共享状态和单账号 check-in 路由漏登记，不能把已复现的基线失败直接豁免为验收通过。 |
| 上游测试兼容修正 | 独立测试作者仅修改十一个既有测试文件，保留行为断言；父 Agent 核对并按 SHA 整合。另对 CLI parity 的常量条目做机械分组与 gofmt，键和值不变。后续完整 agent 包 1170 PASS、0 FAIL、11 项原有 SKIP，补齐安装 fixture 后的显式缓存刷新。 |
| 完整 Go 按包复验 | 在两笔功能提交上分组执行 gateway 和其余全部包，再只修改并完整复验唯一失败的 agent 包。106 个预期包全部有结果：87 个包通过，19 个包无测试文件；合并有效结果为 10061 个顶层 PASS、66 项原有条件 SKIP、0 FAIL。其余包的代码和测试未变，原失败日志保留。gateway 包 3296 PASS、8 SKIP，耗时 467.042 秒。完整证据与输入比较见 `full-go-final-combined.json`，这不包含 GUI 浏览器测试结论。 |

本轮定向和完整 Go 运行材料位于 `/tmp/account-alias-upstream-validation/`，上游 Go 对照位于其 `baseline-diagnosis/`，测试兼容修正记录位于 `/tmp/account-alias-suite-compat/`。早期 GUI 未完成记录保留在 `/data/tmp/magpie-alias-upstream-validation/ui-full/`；完整候选、完整 baseline 和逐项比较分别位于同目录下的 `ui-full-final/`、`ui-full-baseline/`、`complete-ui-baseline-candidate-comparison.json`。只按各记录实际源码和终态报告，不覆盖历史失败或扩大验证范围。

用户已授权验证通过后合回 main、push、构建并替换网关 binary、重启验收。最新提交要求覆盖原“两笔总数”的安排：基线缺陷修复先按相关问题拆成可独立验证的 commits，再放 Bailian AK/SK quota 与账号 alias 两笔功能 commit。当前已拆为十组：CLI fixture 缓存、容器条件、Claude JSON、Codex 进程状态、CLI parity、导航超时、模型列表折叠、历史日期定位、托盘非法 period 观测、动画平台前置。前七组已在不带功能的 commit 上独立验证；日期定位完整文件 4 PASS，动画完整文件 26 PASS；托盘目标 panel 子用例十项均 PASS，完整文件另有两个既有德文窄屏点击失败及其父汇总，未称全绿。Bailian history 与 `keepLast` 签名适配仍归 Bailian；新增 alias 控件引起的 Remove 定位适配仍归 alias，不冒称 baseline 修复。没有创建或发送上游 PR；这里只准备未来可转 PR 的 commit。用户已确认按本节现有验证范围继续交付；完整 GUI 的共有失败仍保留为已知限制，不改称验收全绿。

### 3.3. 历史验证记录：再次同步上游至 96acc59c

本轮保留原账号隔离契约，并适配新上游接口。普通请求保留上游新增的 OpenCode Go → Zen 免费档重试；带 alias 的请求不能切换到公共 key，因此不进行这次重试。System One 按新的 `postDecideSent` 返回类型处理发送前错误；Ark/Seedance 的视频路径保留上游协议及 creating credential 约束。plugin held snapshot 按保存的 slot ID 读取 owner，vendor 显示名变化不改变绑定。

| 原 baseline 修复 | 本轮处理 | 依据 |
| --- | --- | --- |
| CLI executable fixture 缓存 | 保留已有修复 | 相关 lookup 缓存与测试前置未被上游改变。 |
| 容器 CLI/updater 条件 | 保留已有修复 | 容器分支仍存在，原测试假设未变。 |
| Claude JSON 日志 | 保留已有修复，并保留上游相邻修复 | 上游改了写入 `$file` 的兄弟行，写入 `$log` 的本地修复仍需要。 |
| Codex client 版本状态 | 保留已有修复 | 上游进程级 memo 与测试隔离仍未变。 |
| 单账号 check-in parity | 删除本地 commit | 上游 `8abdf53a` 已登记同一路由。 |
| 页面导航 timeout | 删除本地 commit | 上游 `809801f1` 已应用相同补丁。 |
| 模型列表折叠、历史日期定位、非法 panel period 观测、动画平台前置 | 保留四笔已有修复 | 对应测试前置及生产行为未被上游替代。 |

测试与生产修正由不同实际作者执行。新增 held plugin snapshot 回归在旧标签查找的 overlay 下取得有效 RED；Zen 回归在只修编译签名的旧候选上证明 qualified 请求实际切换匿名 key，普通请求正例通过。最终调用点 guard 的行为结果以本轮真实候选测试为准。公开 qualified 引用含 `/`，`GroupFor` 本来就拒绝映射，因此未为不可达的 direct-mapping 情形构造非法 fixture。

注释精简前的原 `TestCoreScriptBudget` 实际 FAIL：12 个 core scripts 共 1534512 字节，阈值 1533802，超 710。最终只精简六段独立行注释，减 967 字节，静态计数 core 1533545、app.js 1311871；原阈值保持。两版非独立行注释 bytes 相同，Bun 1.4.2 解析后重新输出也逐字相同；不把静态计数替代真实 Go budget test。

已完成的 UI 专项：Bailian/账号行四个完整文件双引擎 24 PASS；alias 编辑、模型引用 picker、key 行布局三个完整文件双引擎 100 PASS，均无 FAIL/cancelled/SKIP。目录组四个完整文件（model-chip-steady、provider-fetch-again、remote-magpie、routing-upstream）在最终候选双引擎 90 PASS；tray-totals、side-toggle、window-visibility 三个完整文件双引擎 78 PASS，均无失败、取消或跳过。它们绑定注释精简前的同一执行代码，纯注释差异的等价检查单独保留。

纯上游行为对照已完整结束：常规 105 包中四包有九个顶层失败，gateway 另有两个顶层失败；其中包括 `TestCCListingKeepsItsNames`。本轮不增加修复。最终候选六包功能专项已实际通过：140 个顶层 PASS，无 FAIL/SKIP；包含原 `TestCoreScriptBudget`、held plugin snapshot、普通 Zen retry 成功、qualified Zen 零匿名 fallback，以及 System One 当前绑定复核。最终候选常规 105 包已完整执行：85 包通过、19 包无测试文件，唯一失败为 `sessions/TestCCListingKeepsItsNames`；纯上游和候选同一断言均为 12149 次分配，记录在 `sessions-failure-comparison.json`，不新增修复。native GUI、Linux nogui、Windows 各 vet/build 六项均 exit 0；Windows 仅交叉编译。gateway 完整包已通过：3405 个顶层 PASS、8 项既有 SKIP、0 FAIL。与常规包合并，106 个预期包终态齐全：86 包通过、19 包无测试文件、1 包保留上述 baseline 失败；10296 个顶层 PASS、66 项与纯上游同名的 SKIP、1 个已确认的 baseline FAIL。`full-go-summary.json` 保留合并范围与代码指纹。有限 race 已实际通过：provider/gateway 两包七个顶层 PASS，FAIL/SKIP/数据竞争警告均为零；范围限本轮交叉点及 auth/RPM/System One 相关检查，不扩称全仓 race。所有本轮必要检查已收齐；唯一已确认的 baseline 失败按用户要求保留，不以其为由新增修复。本节记录发布准备时的验证状态，push、build、备份与部署实际终态保存在同目录的交付记录中。

本轮原始日志、输入与后续交付记录统一保存在 `/data/tmp/magpie-rebase-20261103/`。上游固定在本节所列 commit，不因测试期间出现新提交重新开始 rebase。发布与部署在检查结果收齐、确认本地功能无未解决回归后执行，保留已知 baseline 失败与备份。

### 3.4. 历史验证记录：同步上游至 5ee5274b

本轮固定 upstream/main 为 `5ee5274bd9dbf7730977a8d583c1244c21193060`，在 `96acc59c` 之后新增 44 笔 commit；`go.mod`、`go.sum` 未变。采用上游 `17f2387e` 在共享 `fresh()` 中重置 Codex client memo 的修复，删除对应本地子进程隔离 commit；其余七笔已有 baseline 测试修复保留在 Bailian、alias 两笔功能 commit 之前。本轮不新增 upstream baseline 修复。纯上游 `TestCCListingKeepsItsNames` 仍失败，本次隔离环境测得 12159 allocations；这份结果不能改称候选测试通过。

本轮必要的本地功能适配包括：Codex backend search 按 ordinary alias 资格和 caller accounts 交集选择账号，保留原 qualified provider/owner，并在实际发送和 RPM 等待后复核。新增回归在当前实现通过；Go overlay 恢复未适配的上游 search 实现后，真实请求错误使用已配置 alias 的 `acct-1`，取得有效行为 RED。

native compaction 的账号隔离缺漏按本地 alias 功能修复处理，不归为 upstream baseline 修复。测试与生产修改仍由不同实际作者执行：合法未设 alias 账号先成功；同一账号设置 alias 后普通请求必须零发送；另验证真实 client bearer 所属账号、未知外部账号原样 relay、pin 交集、gateway key 的 writer/无 seal 普通选择，以及 keychain token 更新后的身份读取。身份查询只读取本机 metadata，不调用 OAuth refresh；未匹配时 fresh 读取后才决定 unknown 或冲突。旧 managed 正例改用真实 fixture token，原 raw bearer、owner、状态、错误和编码断言保留；外部 unknown fixture 与真实 gateway-key fixture 不改成默认账号。

两个中文 640px key 行用例在纯上游通过、候选失败，确认是新增 alias 入口占位与上游 spend 控件共存导致的本地回归。修复只限定空 alias 控件之后的 masked key 最小宽度，保留 22×22 命中区和其余控件，通过换行保持完整文本。修复后 alias 三文件双引擎 100 PASS，spend cap/weight 两文件补测 22 PASS，均无 FAIL/SKIP/cancel；纯上游另一个英文 640px 失败保留记录，没有增加通用 baseline 修复。其它相关界面组为 Bailian 24、catalog 90、既有 fixture 组 78、新上游界面组 96 PASS；未重跑完整 GUI suite。

首轮完整代码快照为 `8b4297b4af53f41bad7d7fb07cc5c3b5b775db89`：gateway 3438 PASS、5 FAIL、9 SKIP，其余包 6919 PASS、3 FAIL、65 SKIP，全部预期包都有终态。sessions allocation、CLI 的 `GET /api/context/reply` 登记、GUI GC 调整和 gateway 内存 body limit 四种失败均已在纯上游复现，本轮不增加修复；GC 独立复验三次通过不覆盖完整运行的原始失败。其余四种 gateway 失败经最后两项兼容修复及 managed fixture 适配处理，定向复验 63 个顶层测试、47 个子测试全部通过；旧断言未放宽。

修正后的可执行源码快照为 `2807ae48e2647eede2a4452e0e63b15025f07bd9`，实际完整验证 commit 为 `c4450d7ad7b5cb452a2cbb133dec522f3406613e`（两者只差相关文档）。106 个预期 Go 包终态齐全：83 包通过、19 包无测试文件、4 包保留前述 baseline 失败；顶层测试合计 10361 PASS、74 SKIP、4 FAIL。gateway 完整包为 3442 PASS、9 SKIP、1 个已确认 baseline FAIL，523.126 秒完整结束；其余 105 包为 6919 PASS、65 SKIP、3 个已确认 baseline FAIL。最终 sessions allocation 为 12151，纯上游目标运行 12159，均超同一原阈值；CLI、GC、body limit 在两侧的失败断言一致，不新增修复，不把完整 Go 称为全部通过。

native GUI、Linux nogui、Windows 各 vet/build 六项均 exit 0，Windows 只做交叉编译。有限 race 两包 10 个顶层、13 个子测试全部通过，FAIL/SKIP/数据竞争警告为零；包含新增 native compaction 身份和 keychain 更新用例，不扩称全仓 race。独立 review 已关闭本轮确认的本地隔离和兼容缺陷。可执行源码和 GUI assets 的 Git blob 清单含 5270 个文件，SHA-256 为 `2dac2b51cd00a2a1d684b2e5c7d5b0488111e25337969199774712db49ab5918`；最终文档补充后须保持该清单不变。

本轮原始运行材料及发布、构建、备份和部署实际终态统一位于 `/data/tmp/magpie-rebase-20261103b/`。按用户授权，在确认只剩已复现的 baseline 失败后发布；替换网关前保留旧 binary 和配置，验收版本、运行进程 SHA、健康状态及既有配置。历史各节保留原版本与范围，不用本轮结果覆盖旧失败。

### 3.5. 历史验证记录：同步上游至 50265256

本轮固定上游为 `50265256ba56ac164a680da0689e1c6d90dac471`，相对 `5ee5274b` 有 47 笔 commit、208 个文件变化，`go.mod`、`go.sum` 未变。初次 rebase 候选为 `dd0899f0fdd27f533d4d1ae5da52af3de45c1ed9`，九笔 commit 无文本冲突；这不构成行为兼容结论。上游 `62f612fd` 已在源码层修复目录时间戳同一 tick 内的 lookup 缓存问题，本轮删除对应本地测试 workaround。在只保留六笔 baseline 修复和两笔功能 commit 的候选上，整个 agent 包取得 1199 PASS、12 项条件 SKIP、0 FAIL。

初始 rebase 候选的 106 个 Go 包均取得终态：10451 个顶层 PASS、76 SKIP、5 FAIL。Gateway 完整包在 528.584 秒结束，3471 PASS、9 SKIP，唯一失败为 `TestCodexBodyLimitFromMemory`。其余包的四项失败为 sessions allocation、CLI 的 `GET /api/context/reply` 登记、GUI boot 的 `lanURLs=null` 与 GUI GC 的 `GOGC=50`；后四项已在同环境纯上游完整常规包中以相同断言复现，body-limit 另有纯上游定向证据。本轮不新增修复，也不把这些失败称为通过。

初始候选的六项 native GUI/Linux nogui/Windows vet/build 全部 exit 0，Windows 只做交叉编译。选定 provider/gateway 用例的有限 race 为 10 个顶层 PASS，无 FAIL/SKIP。六组 GUI 专项共 21 个不同完整文件，Chromium/WebKit 合计 428 PASS，无 FAIL/SKIP/cancelled；未运行完整 GUI suite。以上均绑定初始候选，不代替后续生产修复的必要复验。

独立 review 确认三处新上游路径与本地 alias 的兼容缺陷：Ark native 路由直接使用 primary key；plugin sign-in carry 未保留 namespace/Home/saved owner；Codex sign-in 导入可能重用已被 alias 保留的 ID。两个独立测试作者先取得合法正例 GREEN 和目标行为 RED，再由第三位作者修改生产代码。冻结的 native、provider sign-in、sealed backup 三个测试文件未放宽断言；首轮生产修复三包 10 个顶层测试全部通过，包括原 native guard 与 streaming。两个真实 sealed backup 用例进一步复现 incoming registry 在 sign-in 导入时尚不可见；真实 DAV 两轮同步证明 metadata 进入 credential hash 后会复活对端删除。保留 credential-only 同步协议后，另以真实 syncOnce 复现 metadata-only 上传被跳过。原生产作者已按这些独立 RED 完成 V4：在 backup/DAV 相关写入前联合校验实际选中的 registry 与身份，credential 删除协议保持上游语义，whole-part 变化检测单独覆盖净化的 owner metadata。V4 定向三包 31 个顶层、27 个子测试全部通过，八份独立测试文件 SHA 均保持。最终扩大回归为四包 161 个顶层、172 个子测试 PASS，FAIL/SKIP 为零；这些重复执行不相加为独立覆盖。独立 reviewer 已关闭全部已确认阻塞，包括 incoming registry preflight、现代与 legacy 删除、metadata-only DAV 上传以及原 native 隔离，没有新增 confirmed blocker。

最终验证候选为 `ed8666a0bf7a9e471042d51ceee2c1b214428ffc`。106 个预期 Go 包全部取得终态：83 包通过、19 包无测试文件、4 包保留已复现的 baseline 失败；10470 个顶层 PASS、76 SKIP、5 FAIL，无 build failure。Gateway 为 3475 PASS、9 SKIP、1 个已确认 baseline FAIL，529.880 秒正常结束；其余包为 6995 PASS、67 SKIP、4 个已确认 baseline FAIL。五项失败与本节初始候选和纯上游对照一致，没有新增失败，本轮不额外修复。`full-go-summary.json` 与 `failure-comparison.json` 保存最终终态及对照。

最终 native GUI、Linux nogui、Windows 各 vet/build 共六项全部 exit 0，Windows 仅交叉编译。provider、gateway、backup、davsync 四包的选定 race 用例为 29 个顶层、38 个子测试 PASS，无 FAIL/SKIP 或数据竞争警告，不扩称全仓 race。21 文件 GUI 专项的 assets 和测试 Git tree 与初始候选逐字一致，沿用本节所列 428 PASS，未重跑完整 GUI suite。sign-in 路由资格通过真实 credential/owner/registry/resolver/RoutingAllows 和 sealed backup、真实 DAV 验证；没有新增 gateway 429 后 HTTP 零发送实测，也未单独验证 owner/auth 发布的并发交错，不扩大覆盖声明。

全部非 Markdown Git tree 条目共 7808 个，规范化清单 SHA256 为 `7d8e195d91c6ce389935a10d84c036615ea5af262cd73aeeeadb8c0f495fd341`。最终验证文档与 commit message 更新后必须保持该清单不变；发布 commit 可与上述验证 commit 不同，实际 push、构建、备份与部署终态以本轮交付记录为准。

本轮材料位于 `/data/tmp/magpie-rebase-20261103c/`。`initial-full-go-summary.json`、`initial-failure-comparison.json`、`gui/summary.json` 分别记录初始完整 Go、纯上游失败对照和 GUI 专项；冻结测试及每次运行的源码、SHA、原始日志独立保留。发布前继续保持 alias 强绑定与 fail-closed 契约，不为未解决的上游 baseline 问题增加修复。

### 3.6. 当前同步上游：2741b749

本轮固定 upstream/main 为 `2741b749f95c8548d1f9107c5cfe474ed1d05c8f`（`v0.1.2888`），相对 `50265256` 改动 125 个文件，`go.mod`、`go.sum` 未变。六笔现有 baseline 测试修复涉及的文件在该 upstream 范围内未变，其对应的容器、JSON 日志、折叠列表、历史日按钮、panel 轮询和 Linux 动画前置仍存在，因此继续保留。上游 `452c28a7` 修复了 `GET /api/context/reply` 的 CLI 登记；纯上游目标测试已实际通过，本轮未重新引入对应本地修复。

唯一文本冲突位于 `groups.go` 的 `servedRef`：保留上游的 `Free`/`Zero` 和本地 `RoutingAlias`/`BaseID`，同 id 共享 group 的替换行为已自动合并并保留。初次 rebase 候选为 `db86c471d9155fac89d5c0b806b595b630c722b5`。独立 review 发现上游 `Same as the lead` 新分支可能把合法 qualified 请求重写为原生 Codex 模型并使用客户端 API key。独立测试先证明 qualified、native lead 和 ordinary subagent 三条合法请求成功，再实际复现跨 provider 误发；生产作者随后增加 `constrainedRef` 守卫。冻结回归和旧兼容用例合计 13 个顶层、3 个子测试 PASS，独立复核关闭了该阻塞；普通显式 `p/m` 仍按上游设置跟随 lead。

初始六包定向为 253 PASS、1 FAIL，唯一失败是原 `TestCoreScriptBudget`。纯上游 core scripts 为 1534819 bytes，候选为 1535142 bytes，原阈值 1533802；阈值未改，本轮不修复该 baseline 超限。纯上游常规包已完整执行，6948 PASS、67 SKIP、7 FAIL，包括 script budget、`TestMoveCutShort`、sessions allocation、GUI boot/GC 和两项由既有容器 fixture 修复覆盖的测试。Gateway body-limit 另有纯上游定向失败证据。初始九组 GUI 专项已结束：30 个不同文件、638 PASS、14 FAIL，无 SKIP/cancelled，计数包含父子测试。两组失败在同环境纯上游完整对照中逐项复现：免费标签组两侧均为 40 PASS/10 FAIL，agent/context 组两侧均为 90 PASS/4 FAIL，无候选单侧失败。本轮不修复这些 baseline 布局问题，不把该专项或完整 GUI suite 称为通过；未执行完整 GUI suite。

最终可执行源码验证候选为 `c6d786831513afee78af89cfbd94e9a42a20dc2d`。106 个预期 Go 包全部取得终态：84 包通过、19 包无测试文件、3 包保留已确认 baseline 失败；10523 个顶层 PASS、79 SKIP、5 FAIL，无 build failure。Gateway 完整包为 3502 PASS、12 SKIP、1 FAIL，532.092 秒正常结束；其余包为 7021 PASS、67 SKIP、4 FAIL。最终失败为 sessions allocation、GUI boot、core script budget、GUI GC 和 Codex body limit，均有本轮纯上游证据；`TestMoveCutShort` 只在本轮纯上游运行失败，候选没有复现，不称为本地已修复。CLI 路由登记继续通过。

最终 native GUI、Linux nogui、Windows 各 vet/build 共六项全部 exit 0，Windows 仅交叉编译。provider、gateway、backup、davsync 四包的选定 race 用例为 30 个顶层、38 个子测试 PASS，无 FAIL/SKIP 或数据竞争警告，包含新增 lead 隔离回归，不扩称全仓 race。独立 review 已关闭本轮 qualified lead 误发阻塞，没有新的 confirmed blocker。GUI assets 和测试 tree 与初始候选逐字一致，沿用上段实际专项和对照，不扩大覆盖。

最终文档补充及 commit message 更新后须保持全部非 Markdown Git tree 清单不变；`code-fingerprint.json` 保存验证 commit、条目数及 SHA。实际发布 commit 可与验证 commit 不同，push、构建、备份与部署终态分别以本轮交付记录为准。

本轮材料位于 `/data/tmp/magpie-rebase-20261103d/`，保留 `baseline-rest.summary.json`、`budget-comparison.json`、`lead-red-01.jsonl`、`lead-fix-green-1.summary.json`、独立 review 与冻结文件 SHA。历史验证不替代本轮结果，发布和部署仍以必要检查及最终交付记录为准。

### 4. 文件归属和最小修改范围

以下是预计 seam，不是要求每个文件都必须改。实施者先用符号和调用点确认需要；没有行为变化的文件不顺手修改。

| 生产职责 | 预计路径 |
| --- | --- |
| registry、resolver、存储身份 | `internal/provider/routing_aliases.go`（新）、`provider.go`、`logins.go` 或 GUI 账号包装、`keys.go`、`models.go`、`account_models.go`。 |
| group/catalog/权限语义 | `internal/provider/group.go`、`groupeffort.go`、`groupmatch.go`、`visible.go`、`flat.go`、`claudedash.go`、`group_share.go`；`internal/gateway/key_models.go`。 |
| gateway 候选与边路 | `internal/gateway/fallback.go`、`gateway.go`、`affinity.go`（仅必要处）、`draw.go`、`video.go`、`embeddings.go`、`decide*.go`、`codex_backend.go`、`codex_search.go`、`native_compaction.go`、`redact.go`、`cap_careful.go`。 |
| API、目录、UI、agent | `internal/gui/providers.go`、`groups.go`、`assets/app.js`、`assets/i18n.js`、`assets/plugins/routing/client.js`；`internal/agent/route.go` 和确实丢 alias 的 catalog adaptor。 |
| backup/sync/rename | `internal/provider/backup.go`、`keep_secrets.go`、`keys.go`、`rename.go`；`internal/backup/backup.go`、`internal/davsync` 的 collect/hash/take/undo 对应点。 |
| 完成后的文档 | `docs/subsystems/providers-accounts.md`、`gateway-routing.md`、`agent-wiring.md`、`sync-backup.md`，必要的 `gui-shell.md`；`docs/reference.md` 与 GUI tests README。 |

测试使用独立的 `routing_alias*_test.go` 和 `account-routing-alias.test.cjs`，沿源码职责放置。不要把新路由语义塞进已有仅测显示名的 `account-alias.test.cjs`，也不要大规模重写现有 pin/fallback tests。复用 existing fake Codex、httptest、plugin host fixtures、backup 和 Playwright harness；不增加通用测试框架。

### 5. 有限验收矩阵

下面 18 个 acceptance IDs 是首版的完成边界；测试可参数化合并。不是对每种 provider、协议、语言、状态做无限笛卡尔积。每项记录测试名、RED/GREEN 或兼容测试状态、运行 SHA/文件树及完整日志路径。

| ID | 有限场景 | 必须观察到的结果 |
| --- | --- | --- |
| AA-01 | 两个 keys、两个订阅账号；一个无 alias，一个有；single/all alias 各一例 | 普通请求只使用无 alias；全部 alias 时无 primary 消费；原普通 fallback 可继续使用其普通账号。 |
| AA-02 | direct qualified 成功、目标 429/500、目标 model list 拒绝、provider 有 fallback | 正确目标成功，上游 base；失败不消费另一账号/provider，cap/rest 不解除限制。 |
| AA-03 | 未知、改名后的旧 alias、清除、缺失账号/provider off；叠加 title/subagent/resume 自动模型设置和同步 middleware 改写 | 明确本地错误，无普通账号、stand-in 或 fallback 调用；清除后普通请求恢复。 |
| AA-04 | 1/32/33 字符、大小写/trim、数字首字母、非法符号、重复、其他 provider 同名 | 合法保存、规范化；非法/重复/retired owner 冲突拒绝；跨 provider 允许；错误不写配置。 |
| AA-05 | native `@500k`、`@20260801`、完整已知 `@latest`、native path `@cf/...`、native base 再加 alias、后来 native 冲突 | 保留 native 原值；qualified 正确拆最后 suffix；冲突 409；未知字母 suffix fail closed。 |
| AA-06 | effort + `[1m]`、Claude dashed、flat `~`，旧 alias 从列表消失后再请求 | reference round-trip，upstream base；旧 spelling 不能绕过 retired 错误。 |
| AA-07 | 旧 affinity、strict cap restore、header 与 alias 一致/冲突、普通 header 指向 alias、改名时 held dispatch | 不恢复隔离账号，header 只能收窄；未发送重试检查新 binding，已发出的流可结束。 |
| AA-08 | `[p/m@a,p/m@b]`；a 失败/删除；group 只有失效成员；嵌套 group | 两个成员不合并；只向显式 b fallback；无成员时报具体原因。 |
| AA-09 | found groups、Match `p/*` 与 regexp、GUI patternCount、group import 缺 alias | alias 不自动成为成员；picker 可显式选择；import 不映射为普通/其他 provider。 |
| AA-10 | gateway key models：base、wildcard、exact alias、group；accounts 允许另一账号但不允许目标 | base/wildcard 含 qualified；exact 仅本 alias；group 保持既有授权；accounts 交集生效，目录也不泄漏目标。 |
| AA-11 | Chat/Responses/Messages/Gemini/Ollama 各一条成功；count 两种形态一条表驱动 | 正确目标与 plain body/URL；未知/retired count 明确失败；估算不访问其他账号。 |
| AA-12 | image generation/edit、video create、embedding/rerank/transcription、System One 各一条；媒体 group 一例 | 每个独立入口使用统一隔离，onKeys 不恢复 provider；video poll 保持创建 key，丢 key 不换账号。 |
| AA-13 | Codex 单账号 bare relay、qualified、search、compact/sealed writer；配置 title/description/prediction/subagent/resume；fake plugin 同 key/slot 改显示名；remote key 一例 | 优化不能绕过；alias 失败不走备用搜索账号；fetch/remote 收到 base 且只用目标 auth。 |
| AA-14 | `/v1/models`、CatalogFor、GUI picker、真实 agent config（至少 Codex、Claude、Pi/MiniMax 各对应形态） | qualified 可选择且发送完整 ref；普通仍展示；模型能力/价格继承 base，Codex 不变 bare slug。 |
| AA-15 | UI 单/多订阅账号和 key，设置/修改/清除/重复；显示名重命名 | 仅正确 endpoint/ID；Enter 一次，Escape 不写；显示名语义不变；错误具体。 |
| AA-16 | 保存 model picks/普通 provider save、AlsoOn、显示名/provider rename、ReplaceKey/RemoveKey | registry 不丢；key owner 不变，指纹迁移；旧 alias 不转给其他 owner；sign-in/key 不被 alias 操作复制或重写。 |
| AA-17 | 带/不带 keys round-trip、另一机器无 login、本地 keyless、Mirror/Restore owner 冲突、旧格式无字段；冷启动/热缓存的整文件损坏或读取失败及较旧无 alias 备份 | registry 与身份一致；keyless 保留本机 key registry；缺账号 fail closed；冲突更新原子拒绝；本机隔离不因字段缺失消失；当前配置状态未知时零上游调用、明确报配置错误；合法无 alias 旧配置仍成功。 |
| AA-18 | alias 改名/清除后的 SyncCatalog 与旧已选模型、Requested/Model/响应 headers、可选 OTLP | 更新目录但不把旧 pick 改成普通/其他账号；本地 Requested 完整，Model/价格/header base；OTLP 保持已有可选发送行为。 |

UI 增量行为在 Chromium、WebKit、五种语言运行，在 1100px 和 360px 验证可操作、不横向溢出、点击不滚动、无 native select/色条，并检查 pageerror 与 placeholders。新增 API/UI 数据会影响共享 fixtures，因此候选稳定后运行整个 GUI suite。没有真实 vendor 访问；alias 身份隔离通过 fake vendor 和真实 gateway/agent 文件形状验证，不把它说成 vendor 端实测。

### 6. 验证环境与命令

设计阶段仅做读取和文档检查，尚未执行 Go/GUI tests。后续测试必须 sandbox HOME，Go caches 在切 HOME 之前固定；按本机预装依赖运行，fake upstream 仅使用 loopback。不能访问真实 vendor，不能读写真实 agent 配置。

建议将下列命令作为执行环境要求，不把它写成已经执行的证据：

```bash
(
  set -euo pipefail
  alias_gopath=$(go env GOPATH)
  alias_modcache=$(go env GOMODCACHE)
  alias_buildcache=$(go env GOCACHE)
  alias_verify_home=$(mktemp -d)
  trap 'chmod -R u+w "$alias_verify_home"; rm -rf "$alias_verify_home"' EXIT
  export GOPATH="$alias_gopath" GOMODCACHE="$alias_modcache" GOCACHE="$alias_buildcache"
  export HOME="$alias_verify_home" USERPROFILE="$alias_verify_home"
  export XDG_CONFIG_HOME="$alias_verify_home/config"
  export XDG_CACHE_HOME="$alias_verify_home/cache"
  export XDG_DATA_HOME="$alias_verify_home/data"
  export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local
  go test -tags nogui -timeout 45m ./internal/provider ./internal/gateway ./internal/gui ./internal/agent ./internal/backup ./internal/davsync -run 'RoutingAlias|Qualified'
)
```

各包现有 TestMain/testenv 继续清理 agentenv 变量并管理子进程。额外的 vendor dispatch、plugin、模型目录、usage 和 auth refresh fixture 由测试作者显式重定向到 loopback；只设置 HOME 或 GOPROXY 不足以证明零 vendor 访问。依赖未缓存时报告环境阻塞，不能临时访问 vendor 来补测试。Playwright browser/tool 路径取执行环境预装位置，换 HOME 前固定必要的缓存路径。

定向测试先运行相关 `routing_alias` 文件；RED 运行绑定基线源码，GREEN 运行绑定候选源码。后续缺陷先补最小失败用例，再修实现并重跑受影响批次。涉及并发写入/异步刷新和 dispatch 变化的用例执行 `-race`；时间条件用可控状态或 barrier，不靠 sleep 或重跑到绿。

候选稳定后的仓库必要检查：

```bash
gofmt -l <实际改动的Go文件>
go vet ./...
GOOS=linux go vet -tags nogui ./...
GOOS=windows go vet ./...
go build
GOOS=linux go build -tags nogui
GOOS=windows go build
go test -tags nogui -timeout 45m ./...
node --test internal/gui/tests/account-routing-alias.test.cjs
make test-ui
```

上述 build、vet、test 均在独立 sandbox 环境执行；交叉 build 的产物放本轮临时目录，避免覆盖不属于执行者的文件。两个浏览器由既有 GUI test loop 或显式 BROWSER 运行，核对实际 pass 数，不假定 `make test-ui` 本身负责重复两次。后续完整 GUI 改为固定清单分批，候选与 baseline 使用相同镜像及规则；依据验证主机实际 192 CPU 和可用内存安排并行批次，各输入冻结并在执行前后核对。每批保留完整 stdout/stderr 和终态，不用部分输出代替完成；skip、cancelled、超时单独报告。

用户要求长命令使用 `bg_run` 并等待原生完成通知。执行者应通过其实际提供的后台工具运行，不用轮询 dispatch 成功代替完成。本设计作者的工具没有 `bg_run`，因此没有启动长命令；后续执行者若同样缺工具，应向主 Agent 交代这一能力限制，不能默默改为前台长跑。

本轮用户已授权 commit 整理：baseline 原有问题的修复按问题分组前置，之后保持 Bailian AK/SK quota、账号 alias 两笔功能 commit。合回 main、push、替换网关 binary 和重启以验证通过为前提；没有发送 GitHub 消息的授权。仓库规则对已知 RED 的处理继续适用：保留问题和日志，不把未解决失败计为验收通过。

### 7. review 与交付责任

设计 review 先结束，再进入测试；每个实现批次以独立作者的行为 RED 开始。主 Agent 保留文件所有权、RED 输出、GREEN 输出、skip 原因和候选文件树，reviewer 可以按原始 JSON/HTTP/agent 文件证据独立复核，而不只依赖作者说明。

最终实现 review 至少检查四条完整路径：UI 设置并持久化；普通请求从候选生成到重试都排除 alias；direct qualified 从输入到 upstream 始终只用目标；group/catalog/permissions/sync 不丢 owner 或完整 reference。reviewer 对 AA-01、AA-02、AA-03、AA-07、AA-08、AA-10、AA-13、AA-17 的关键保证做有限 mutation 检查，主 Agent 处理每条阻塞意见并在最终候选复验。

交付完成需同时满足：18 项矩阵均有有效证据；原无 alias 回归通过；适用全量 Go/build/vet/GUI 检查完成；owner 冲突与 native 兼容边界有用户说明；没有通过 skip、编译失败或错误 fixture 充当 RED；生产作者未修改测试；没有 staged 文件或未经授权的外部操作。主 Agent 负责最终取舍和验收，文档作者的设计结论不替代 implementation review。


### 8. 上轮交付准备时的执行状态（历史快照）

- [x] fetch upstream，最初从 `96a703a5` 建立候选，本轮已 rebase 到 `ce2e81da`。
- [x] 完成路由与 UI 并行源码探索。
- [x] 完成设计和计划，复核并接受 F1–F3 修正。
- [x] 测试作者建立可编译的行为 RED，记录真实失败。
- [x] 生产作者实现，Go 与 UI 功能专项 GREEN；全量验收状态另列。
- [x] 完整执行候选与干净 baseline 的 GUI 清单，并补齐长滚轮分区；完整结果仍有失败。
- [x] 按最新要求拆出十组前置 baseline 修复，再重放两笔功能 commit；运行代码与 `478087b9` 一致。
- [x] 完成十组修复的目标范围独立验证，明确保留托盘完整文件及全量 GUI 的既有失败。
- [x] 完成最终 review 与验收处置；用户明确接受记录中的既有 GUI 失败及差异复验结果，不将共同失败或差异未再现称为完整 GUI 通过。
- [ ] 按用户确认的验证范围合回 main、push、构建替换并重启网关，清理已合入且不再需要的 branch/worktree；本项为交付准备时的待执行动作，终态以实际执行记录为准。

内部辅助接口名称允许按源码习惯调整；HTTP endpoint、JSON 字段和用户可见行为按设计冻结。测试和生产代码必须由不同实际 agent 实例编写，后续修复同样遵守。

## 方案 review 决策记录

- F1：接受。源码 `pluginLogins` 已按 slot 保持 saved ID；routing owner 若继续使用名字 hash，会在显示名变化后解除隔离。实现和测试必须读取真实稳定身份。
- F2：接受。title/subagent/resume 和同步 middleware 在 resolver 前能换 model；必须在这些入口之前保存原始 qualified 约束，不能仅复核最终 body.model。
- F3：接受。配置读取失败会丢失 picks 并继续发现账号，旧备份也不能证明当前隔离状态；冷启动、热缓存及旧备份分别纳入有限测试。
- 主 Agent 负责最终验收；接受方案 review 不代表功能或测试已完成。

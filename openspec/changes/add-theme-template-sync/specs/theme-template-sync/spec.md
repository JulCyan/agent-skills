## Purpose

为 AI agent 提供可安装、通用、默认只读且具有确定性 readback 的 Shopify JSON
template 同步能力。

## ADDED Requirements

### Requirement: Skill 必须可独立发现与安装

系统 SHALL 将 `theme-template-sync` 作为 `skills/theme-template-sync` 下的自包含
产品 Skill，通过仓库既有 Skills CLI 发现和 copy-install；安装副本不得依赖 provider
仓库路径或 `shopify-theme-multi`、`shopify-theme-next`。

#### Scenario: 从 tracked snapshot 安装

- **WHEN** 验收从 tracked Git snapshot 列出并 copy-install Skills
- **THEN** `theme-template-sync` 被发现且安装副本可在仓库外 cwd 运行 `--help`

#### Scenario: 环境缺少可信执行器

- **WHEN** 未提供显式 binary 且系统没有可用 Go，也没有固定 Release asset
- **THEN** launcher 返回结构化 `NEEDS_SETUP`，且不得尝试下载或执行 `latest`

### Requirement: Template 入口必须通用于 Shopify JSON templates

系统 SHALL 通过 `--template` 接受 page、product、collection 与其它合法 template key，
并只解析为单个 `templates/<key>.json` asset；能力模型不得限定为 Page。

#### Scenario: 解析不同资源类型

- **WHEN** 用户分别传入 `page.example`、`product.lp4` 与 `collection.example`
- **THEN** 系统分别解析为对应 `templates/*.json` 且使用相同同步流程

#### Scenario: 使用旧 Page 兼容入口

- **WHEN** 用户传入 `--page example` 且未传 `--template`
- **THEN** 系统将其严格映射为 `--template page.example` 并提示该 alias 已 deprecated

#### Scenario: 同时使用两个 Template 入口

- **WHEN** 用户同时传入 `--page` 与 `--template`
- **THEN** 系统在 adapter 调用前拒绝

#### Scenario: Template 参数尝试路径逃逸

- **WHEN** template 包含 slash、backslash、绝对路径、`..` 或非 JSON asset 表达
- **THEN** 系统在 adapter 调用前拒绝

### Requirement: Source 与 targets 必须显式且确定地解析

系统 SHALL 要求 source 和每个 target 显式提供 store/theme，精确解析 theme ID 或唯一
精确名称，并在 plan 中冻结 ID、name 与 role；不得自动选择主题或默认扩散到全部店铺。

#### Scenario: Theme 无匹配或多匹配

- **WHEN** 任一 source/target theme reference 零命中或名称多命中
- **THEN** 系统在读取 template 或写入前失败并指出对应 target

#### Scenario: 多目标保持显式顺序

- **WHEN** 用户按顺序提供多个 targets
- **THEN** plan、evidence 与后续串行执行均保持该顺序，不增加隐式目标

### Requirement: Live target 与主题生命周期操作必须被禁止

系统 SHALL 允许显式 source Live theme 只读，但 SHALL 在 plan、preflight 或写入前发现
任一 target 为 Live 时阻断；系统不得创建、复制、Publish 或自动选择主题。

#### Scenario: Target 在初始解析时为 Live

- **WHEN** target theme role 解析为 Live
- **THEN** 系统在 template 写入前失败，mutation call count 为零

#### Scenario: Target 在写入前变为 Live

- **WHEN** target 通过全量 preflight 后、轮到写入前的即时身份检查显示其 role 已变为 Live
- **THEN** 系统停止该 target 和后续写入，并按已完成目标数量报告 `FAILED` 或 `PARTIAL`

### Requirement: Shopify JSONC 必须安全解析且完整保留数据

系统 SHALL 支持 Shopify 生成的前导注释，使用通用 JSON object tree 和精确 number
表示保存文档；完整模式不得丢失 source 未知顶层字段，Section/字段模式不得改变 target
非目标数据。

#### Scenario: 带 Shopify 头部注释的未知字段模板

- **WHEN** template 在 root object 前包含 Shopify 注释且包含未知顶层字段
- **THEN** parser 成功，canonical document 包含未知字段，Section/字段输出保留 target 注释

#### Scenario: JSONC 后含额外内容

- **WHEN** root object 后还有第二个值、注释外垃圾或 parser 不支持的正文语法
- **THEN** 系统拒绝输入而不是静默截断

### Requirement: Template 核心结构必须在写入前验证

系统 SHALL 要求 root `sections` 为 object、`order` 为无重复值的 string array，并在
source、target 和 planned document 上验证 order 不超过 25 项。

#### Scenario: Sections 或 order 类型错误

- **WHEN** source、target 或 planned document 的 `sections`/`order` 类型不符合要求
- **THEN** 系统在任何写入前 fail closed

#### Scenario: Section 同步超过 Shopify 上限

- **WHEN** 新增 Section 使 planned order 从 25 项变为 26 项
- **THEN** dry-run 报告结构失败，execute mutation call count 为零

#### Scenario: Order 含重复 Section key

- **WHEN** source、target 或 planned order 中同一 key 出现多次
- **THEN** 系统在任何写入前 fail closed，而不自行去重

### Requirement: 系统必须支持完整 template 同步

系统 SHALL 在完整模式中以 source 的完整 semantic document 生成 target planned document，
包含全部 Sections、order 和未知顶层字段，并要求 target template 已存在。

#### Scenario: 完整同步包含未知字段和 order 变化

- **WHEN** source 与 target 的 Sections、order 和未知顶层字段不同
- **THEN** planned document 与 source canonical document 一致，diff 稳定报告全部变化

#### Scenario: Target template 不存在

- **WHEN** target theme 没有指定 template asset
- **THEN** 系统 fail closed 且不得隐式创建 template

### Requirement: 系统必须支持单 Section 同步

系统 SHALL 在 Section 模式只替换或新增指定 Section，保留 target 其它 Sections 与未知
顶层字段；已有 Section 不改变 order，新增或 order 缺失时按 source 前驱规则确定位置。

#### Scenario: 替换已有 Section

- **WHEN** source 与 target 都包含指定 Section
- **THEN** 只有 target 该 Section value 改为 source value，target 其它 Sections 与 order 不变

#### Scenario: 新增 Section

- **WHEN** source 包含指定 Section 而 target 缺少它
- **THEN** 系统新增该 Section，并插入 source order 中最近且在 target 存在的前驱之后，
  没有可用前驱时追加

### Requirement: 字段同步只能操作 custom_css

系统 SHALL 只允许字段路径 `sections.<sectionKey>.custom_css`，并要求 source 与 target
Section 的该字段均存在且为 JSON string array；不得接受任意 path 或 JSON Pointer。

#### Scenario: 精确同步 custom_css

- **WHEN** source 与 target 的 `custom_css` 都是 string array 且内容不同
- **THEN** planned document 只替换目标 array，目标 Section 的 settings、blocks、其它字段、
  其它 Sections、order 与未知顶层字段保持 canonical 等价

#### Scenario: custom_css 缺失

- **WHEN** source 或 target Section 缺少 `custom_css`
- **THEN** 系统在任何写入前 fail closed

#### Scenario: custom_css 类型错误

- **WHEN** source 或 target 的 `custom_css` 为 string、null、mixed array 或其它非 string array
- **THEN** 系统在任何写入前 fail closed 并指出字段类型错误

#### Scenario: 请求未开放字段

- **WHEN** 用户请求同步 settings、blocks 或任意自定义 path
- **THEN** 系统在 adapter 写入前拒绝并列出允许的 `custom_css`

### Requirement: Diff 必须稳定并识别结构与 order 变化

系统 SHALL 对 canonical semantic tree 生成稳定排序的 added、removed、changed 与 order
diff，并明确报告 Section added/removed/changed keys。

#### Scenario: 同时存在 Section 和 order 差异

- **WHEN** planned document 相对 target 同时新增、删除、修改 Section 并改变 order
- **THEN** 重复运行得到字节稳定的 diff，四类变化均可独立审计

#### Scenario: 只有格式或头部注释不同

- **WHEN** target 与 planned semantic document 相同但空白、object key 顺序或头部注释不同
- **THEN** 系统判定为 `NO_OP` 且 write call count 为零

### Requirement: Removed 内容必须显式授权

系统 SHALL 默认允许展示含 removed 的 plan/diff，但 SHALL 阻断 execute；只有 plan 与
execute binding 都包含显式 `allow-remove` 决策时才允许写入。

#### Scenario: 未授权删除

- **WHEN** diff 含 removed Section、字段、array element 或缩短后的 `custom_css`
- **THEN** execute 在所有 targets 写入前阻断，mutation call count 为零

#### Scenario: 已审查删除授权发生漂移

- **WHEN** preview 未绑定 allow-remove，但 execute 临时增加该参数，或反向发生变化
- **THEN** 系统拒绝旧 binding 并要求重新 preview

### Requirement: Dry-run 与 no-op 不得写入

系统 SHALL 默认执行 plan/preview；只有显式、有效且 identity-matched 的 `--execute`
才可调用写 adapter，且 no-op target 永远不得写入。

#### Scenario: 默认 dry-run

- **WHEN** 用户完成 plan 或 apply 但未传 `--execute`
- **THEN** 系统输出 `NO_OP` 或 `PLANNED`，write call count 为零

#### Scenario: Execute 中包含 no-op target

- **WHEN** 多目标 execute 中部分 targets canonical 等价
- **THEN** 等价 targets 标记 `NO_OP`，只对有差异且通过 gate 的 targets 调用 write

### Requirement: 多目标必须先全量 preflight 再串行写入

系统 SHALL 在任何 mutation 前完成全部 targets 的 resolve、Live 检查、read、parse、
transform、diff、removed gate 与 before hash 验证；全部通过后才按输入顺序串行写入。

#### Scenario: 后序 target preflight 失败

- **WHEN** 第一个 target 可应用但后序 target 在解析或安全检查中失败
- **THEN** 整个 execute 在写入前失败，所有 targets 的 write call count 为零

#### Scenario: 多目标成功执行

- **WHEN** 全部 targets 通过 preflight 且每个 write/readback 成功
- **THEN** adapter write 最大并发为一，调用顺序等于 target 输入顺序

### Requirement: 每次写入后必须立即 canonical readback

系统 SHALL 在每个 target 写入返回后立即重新读取同一 asset，解析并比较 planned
canonical hash；readback 成功前不得写入下一个 target。

#### Scenario: Canonical readback 一致

- **WHEN** 写入后的远端 document 只在格式或 Shopify 头部注释上不同
- **THEN** canonical hash 一致，该 target 标记 `APPLIED` 并继续下一 target

#### Scenario: Readback mismatch

- **WHEN** 写入后的 canonical hash 与 planned hash 不同
- **THEN** target 标记 `READBACK_MISMATCH`，系统停止后续写入并返回非零 exit

### Requirement: 结果必须区分失败、部分成功与读回不一致

系统 SHALL 以 target 状态 `NO_OP`、`READY`、`APPLIED`、`WRITE_FAILED`、
`READBACK_MISMATCH`、`SKIPPED_AFTER_FAILURE` 和整体状态 `NO_OP`、`PLANNED`、
`APPLIED`、`FAILED`、`READBACK_MISMATCH`、`PARTIAL` 解释结果。

#### Scenario: 中途写入失败

- **WHEN** 至少一个 target 已完成 canonical readback，后续 target 写入失败
- **THEN** 整体状态为 `PARTIAL`，失败 target 为 `WRITE_FAILED`，剩余 targets 为
  `SKIPPED_AFTER_FAILURE`

#### Scenario: 第一次写入即 readback mismatch

- **WHEN** 没有 target 已确认 applied，首个写入的 readback 不一致
- **THEN** 整体状态为 `READBACK_MISMATCH`，不得自动 rollback 或重放

#### Scenario: 已有成功后发生 readback mismatch

- **WHEN** 至少一个 target 已确认 applied，后续 target readback 不一致
- **THEN** 整体状态为 `PARTIAL`，manifest failure kind 为 `READBACK_MISMATCH`

### Requirement: 运行 evidence 必须位于消费项目并可审计

系统 SHALL 默认在调用者 cwd 的 `.runtime/theme-template-sync/<run-id>` 保存 source
before、每 target before/planned/diff/after/failure 与 manifest；不得向 Skill 安装目录、
provider 仓库或真实项目源码目录写 evidence。

#### Scenario: 从任意 cwd 运行已安装 Skill

- **WHEN** 用户在消费项目 cwd 调用已安装 Skill
- **THEN** 默认 evidence 位于该 cwd 的 runtime root，launcher cwd 不发生改变

#### Scenario: 写入后失败

- **WHEN** target write、readback 或 canonical compare 失败
- **THEN** manifest 和对应 failure/after evidence 记录已知事实、状态与 hash，且不包含凭据

#### Scenario: Evidence path 试图逃逸

- **WHEN** run ID、template、target label 或 symlink 试图让输出落到 evidence root 外
- **THEN** 系统 fail closed，且 root 外文件保持不变

### Requirement: Production adapter 不得依赖项目秘密或 shell 拼接

系统 SHALL 使用 Shopify CLI 的显式参数执行 list/pull/push，参数不得经 shell 解释；
Skill 不读取 `.env`、token、cookie、个人路径或业务 stores config，不传 publish/live
类 flags。

#### Scenario: Store 或 theme reference 含 shell 元字符

- **WHEN** 输入包含 shell metacharacters
- **THEN** adapter 将其作为单独参数处理或在验证阶段拒绝，不得产生额外 shell command

#### Scenario: 自动化测试运行

- **WHEN** 仓库运行 Go、Node、安装或完整 `npm test`
- **THEN** 只使用合成 fixture 与 fake/local adapter，不连接或写入真实 Shopify

# Design

## 1. 设计选择

采用 Skill 内独立 Go 执行器，以旧 Node.js 实现作为行为迁移来源，不扩展
`shopify-theme-next` 中未接通远端 adapter 的 Go `sync-page`。后者的窄结构只保留
`sections` 与 `order`，会丢失未知顶层字段，不适合作为本能力的数据模型。

备选方案及取舍：

1. **独立 Go 执行器（采用）**：可生成单一确定性 binary，adapter 易于 fake，状态与
   evidence 可以集中管理，同时保留成熟 Node 行为。
2. **继续扩展 Next Go 实现（拒绝）**：把消费项目耦合和未完成 adapter 带回 provider
   Skill，也违反本次不修改 Next 的边界。
3. **Go 只负责 JSON 变换、Node 负责编排（拒绝）**：形成双运行时核心，增加安装、错误
   传播与测试矩阵，却没有带来当前需求所需的独立能力。

Go module 对齐仓库现有 Go 目标版本 `1.25.1`。实现优先使用标准库：固定的小型命令树
使用 `flag`，依赖通过构造函数手工注入，不引入 Cobra、Viper 或 DI 框架。通用 Go
编码、错误、安全和测试规范属于开发期 user-scope Skills；仓库只通过 `AGENTS.md`、
OpenSpec、代码审查和可执行测试固化本项目特有约束，不 vendoring `.agents/skills`。

产品标识采用 `theme-template-sync`。名称使用资源域与动作表达能力，不使用 `shopify-*`
或 `*-for-shopify` 形式暗示 Shopify 官方归属；Shopify 适用范围保留在准确、可发现的
description、文档与安装来源中，UI metadata 明确显示独立维护者。

## 2. 安装面与目录

安装单元为 `skills/theme-template-sync`：

```text
skills/theme-template-sync/
├── SKILL.md
├── agents/openai.yaml
├── references/
│   ├── cli-contract.md
│   └── result-contract.md
└── scripts/
    ├── theme-template-sync.sh
    └── template-sync-go/
        ├── go.mod
        ├── cmd/theme-template-sync/main.go
        └── internal/
            ├── app/
            ├── adapter/
            ├── evidence/
            ├── syncengine/
            └── templatejson/
```

POSIX wrapper 只负责解析 Skill 自身位置并选择可信执行器；同步逻辑全部位于 Go。首期按
“显式 trusted binary -> bundled Go source -> structured `NEEDS_SETUP`”选择运行时，
不下载 `latest`，也不声称已有 Release。source fallback 临时 `go build` 后执行并清理，
从而原样透传应用 exit 0/2/3/4，不暴露 `go run` 的 wrapper exit。wrapper 必须覆盖继承的
caller-cwd 环境并保留真实调用者 cwd；build 清空继承 `GOFLAGS`、固定 `GOWORK=off` 与
`GOTOOLCHAIN=local`，避免 build flag/workspace 注入和隐式 toolchain 下载。默认 evidence
写入消费项目，而不是安装目录。

## 3. 命令与授权契约

正常流程为：

```text
plan -> apply preview -> apply --execute
```

`plan` 从 Shopify 只读拉取 source 与全部 targets，生成固定 plan、planned documents、
diff、canonical hashes 和 target identities。`apply --plan <path>` 默认重新做只读
preflight，并将 preview binding 原子写入该 run 的 manifest；后续对同一显式 plan 执行
`apply --plan <path> --execute` 时读取该 binding。只有 plan SHA、source identity、target
identities、before hashes、template/mode/section/field 与 allow-remove 决策全部一致时才
进入写入阶段；不接受 `last`、mtime 或隐式“最近一次 run”。

`--template` 是通用入口，接受不带 `templates/` 前缀和 `.json` 后缀的 template key，
例如 `page.example`、`product.lp4`、`collection.example` 或 `customers/account`。实现将
其规范化为且只能为 `templates/<key>.json`；允许 Shopify 的一个受控子目录层级，拒绝
更多层级、空 segment、backslash、`..`、绝对路径、重复 `templates/` 与 `.json` 后缀。
保留 `--page <name>` 作为 deprecated alias，并严格映射为 `--template page.<name>`；
两者互斥，内部不得形成独立 Page 数据模型。

source 通过 `--source-store`、`--source-theme` 显式提供；每个 target 通过成对、可重复的
`--target-store`、`--target-theme` 提供，数量不等或为空即拒绝。不得存在 golden store、
all stores 或默认主题。多目标参数保持输入顺序，该顺序也是串行执行顺序。plan 中冻结
解析后的 theme ID、name 与 role，后续阶段不得重新按“最近”“第一个”或模糊名称选择。

## 4. Shopify adapter

执行器在消费侧定义最小接口：

```go
type ThemeAdapter interface {
    ResolveTheme(context.Context, StoreRef, ThemeRef) (ResolvedTheme, error)
    ReadTemplate(context.Context, ResolvedTheme, TemplateKey) ([]byte, error)
    WriteTemplate(context.Context, ResolvedTheme, TemplateKey, []byte) error
}
```

`ShopifyCLIAdapter` 使用 `exec.CommandContext` 和分离参数调用 `shopify theme list --json`、
`theme pull --only` 与 `theme push --only --nodelete --json`。它不得拼接 shell command，
不得传入 `--live`、`--allow-live`、`--publish`、`--unpublished`，不得读取 `.env` 或密钥
文件，也不得创建/复制主题。执行前移除继承环境中的全部 `SHOPIFY_FLAG_*` 行为覆盖，
防止环境变量隐式注入 publish/Live flags；CLI 会话与认证环境继续传递。认证来自用户已经
建立的 Shopify CLI 会话。失败 stderr 仅以有界、控制字符清理且 credential-redacted 的
摘要向上传播；Authorization 与多值 Cookie/Set-Cookie header 必须整体脱敏，原始 stderr
不写 evidence。若 push 已成功但本地临时目录 cleanup 失败，
adapter 返回 typed accepted-write outcome，编排层仍立即 readback，再以 evidence failure
报告而不是把远端状态误判成普通 write failure。

theme ID 必须精确命中；theme name 必须精确且唯一命中。零命中、多命中、无法解析 role
或 store/theme list 失败均在 mutation 前失败。显式 source 可以是 Live，因为它只读；
任何 target 在 plan、apply preflight 或写入前的即时身份检查中为 Live，都必须阻断。

`FakeAdapter` 实现同一接口，记录 resolve/read/write call，并可注入解析失败、写入失败、
读取失败、role 漂移与 readback mismatch。所有自动化测试只使用 fake/local adapter。

## 5. Shopify JSON/JSONC 模型

parser 支持 UTF-8 BOM、前导空白和 Shopify 生成的前导 block/line comments，然后使用
`json.Decoder.UseNumber()` 解码恰好一个 JSON root object；尾随非空内容、正文内注释、
重复解码值、任意深度 duplicate object key 或非对象 root 均拒绝。此范围覆盖 Shopify JSON template 的生成式 JSONC，
而不承诺任意 JSON5 语法。

文档以通用 JSON object tree 保存，`sections`、`order` 与 `custom_css` 通过受控 typed
accessor 访问。root `sections` 必须为 object，`order` 必须为无重复值的 string array，
且 order 不得超过 Shopify JSON template 的 25 Section 上限。这样完整模式不会因内部
struct 过窄而丢失未知顶层字段。Section/字段模式从 target document 深拷贝后只修改
目标路径，保留 target 的头部注释和全部非目标数据；transform 后再次执行结构与上限
校验。

canonical encoder 递归排序 object keys，保留 array 顺序和 `json.Number` 表示，输出
稳定 UTF-8 JSON，再计算 SHA-256。canonical 比较忽略头部注释和无意义格式差异，
不忽略 object/array 值、Section order 或未知字段差异。

## 6. 三种同步粒度

### 完整 template

planned document 使用完整 source semantic document，包含 source 的所有已知与未知顶层
字段。target template 必须已经存在；本能力不隐式创建 template asset。

### 指定 Section

source Section 必须存在。target 已存在同 key 时，只替换该 Section value，不改变 target
的其它 Sections、未知顶层字段或现有 order。target 缺少该 Section 时允许新增 Section，
并沿用旧 Node 行为：优先插入 source order 中最近且也存在于 target order 的前驱之后，
没有可用前驱时追加；Section 已存在但 order 缺失时也按该规则补入。所有新增、删除、
修改和 order 变化进入稳定 diff。

### 指定字段

首期 allowlist 只有 `custom_css`，其固定语义为
`sections.<sectionKey>.custom_css`。source 和 target Section 及其 `custom_css` 都必须存在，
且值必须为 JSON string array；缺失、`null`、单 string、mixed array 或其它类型均
fail closed。同步只替换该 array，保留目标 Section 的 `settings`、`blocks`、其它字段、
其它 Sections、order 与未知顶层字段。不接受用户提供任意 path 或 JSON Pointer。

## 7. Diff 与 removed gate

diff 基于 canonical semantic tree，按稳定 path 排序，至少输出：

- `added`：新增 Section、字段、object member 或 array element；
- `removed`：删除 Section、字段、object member 或 array element；
- `changed`：同一路径的 scalar/type/value 改变；
- `order`：template Section order 的新增、删除、移动或整体变化。

Section 摘要还要明确列出 added/removed/changed keys。默认只要 diff 含任何 removed
内容，plan/preview 可以展示，但 execute 必须阻断；只有 reviewed plan 与 execute 都
绑定显式 `--allow-remove` 才允许继续。该 gate 同样适用于 `custom_css` 数组缩短，以及
object/array 被 scalar、`null` 或其它 container type 替换时消失的所有后代路径。

## 8. 多目标状态机与 readback

所有 targets 必须在任何写入前完成 resolve、Live 检查、read、parse、transform、diff、
removed gate 与 before hash preflight。任一目标失败时 mutation call count 为零。

execute 按用户给定顺序串行处理。每个 target 写入前再次只读验证 theme identity/role 与
template before hash；通过后才写入。每次成功返回的写入后立即重新拉取同一 template，
解析并 canonical 比较 planned hash。readback 不得延后到全部目标完成后。

target 状态为 `NO_OP`、`READY`、`PREFLIGHT_FAILED`、`WRITE_IN_PROGRESS`、
`READBACK_IN_PROGRESS`、`APPLIED`、`WRITE_FAILED`、`READBACK_FAILED`、
`READBACK_MISMATCH`、`EVIDENCE_FAILED` 或 `SKIPPED_AFTER_FAILURE`。整体状态为：

- `NO_OP`：全部 target 无差异，且 write call count 为零；
- `PLANNED`：dry-run/preview 有待应用差异，且 write call count 为零；
- `APPLIED`：所有有差异 target 均写入并 canonical readback 一致；
- `FAILED`：未确认任何 target applied 即发生 preflight/write/readback/evidence 失败；
- `READBACK_MISMATCH`：首个已接受写入的 canonical readback 不一致，且此前无 target
  已确认 applied；
- `PARTIAL`：至少一个 target 已确认 applied，之后发生失败；也包括 matching readback
  已确认 applied 但 cleanup/evidence checkpoint 失败；manifest 另保留精确 failure kind。

`EXECUTING` 是 manifest-only 的 nonterminal durable checkpoint。执行器在调用 write 前
先原子记录 write intent，在调用 readback 前先记录 readback intent。进程若在边界中断，
远端结果视为 unknown，同一 run 不可重入；必须新建 plan 做只读恢复检查。

每次 apply 在读取 plan/manifest 或调用 adapter 前，以 `O_EXCL` 获取 run-scoped
`.apply-lease`。正常且尚未形成 mutation intent 的退出释放 lease；准备持久化 write intent
时先将 lease 标为保留，之后无论成功、失败或 cleanup 不确定都不删除。并发 preview/
execute 的迟到调用必须在 adapter call count 仍为零时失败，且不得覆盖先行 apply 的
manifest。进程若在更早的只读阶段异常退出也会留下 lease，并按 fail-closed 原则要求新 plan。

出现 write failure 或 readback mismatch 后停止后续写入，其余 READY targets 标记为
`SKIPPED_AFTER_FAILURE`，no-op targets 保持 `NO_OP`。执行器不自动 rollback，因为第二次远端写入会扩大不确定性；
evidence 必须足以支持人工判断与后续显式恢复。

## 9. Evidence 与错误输出

默认 evidence root 为调用者 cwd 下
`.runtime/theme-template-sync/<run-id>/`，可由显式参数覆盖；显式 root 必须为空或尚不存在，
且仍位于调用者 cwd 内、权限不宽于 owner-only，不得改权、覆盖/复用已有 run。永久
`.run-reservation` 防止 root 并发抢占；apply 另使用上述 `.apply-lease` 防止同一 run
重入。root 创建后尽力写 manifest；若 plan 或 plan manifest 落盘失败，结果仍保留已知
run ID、plan path/SHA 与 evidence root。其它 artifact 按实际到达的阶段生成，成功 plan
才保证：

```text
manifest.json
plan.json
.run-reservation
.apply-lease                 # apply 运行中或 mutation 后保留
source/before.json
source/canonical.json
targets/<index>/before.json
targets/<index>/planned.json
targets/<index>/diff.json
targets/<index>/after.json        # 仅发生 readback 时
targets/<index>/failure.json      # 仅失败时
```

manifest 记录 plan/binding SHA、mode、template、section/field、resolved identities、
before/planned/after hashes、每目标状态、整体状态、write/readback call evidence 与错误类别，
但不记录 token、cookie、环境变量内容或 CLI 凭据。文件使用受控相对路径和原子写入；
不得跟随可使 evidence 逃逸 root 的路径或 symlink。plan 会重算 content hash、diff 与
派生 status；manifest 具有自身 SHA，并与显式 plan 的 run ID/path/SHA、template、scope、
allow-remove、targets 和 preview binding 全量验证，并按 APPLIED count、唯一 failure target/
kind 与 target 顺序重新派生终态；任何 tamper 都在 adapter 调用前拒绝。
plan/manifest strict decoder 同时拒绝未知字段与 duplicate object key，使文件全部可见内容都
进入审计边界。CreatePlan 在落盘并报告成功前也必须运行相同完整 Plan validation。

若写 primary failure 后 target failure artifact 或终态 manifest 又写失败，返回的 primary
failure kind 不变，但 message 必须把 secondary evidence persistence failure 放在有界脱敏
摘要中；仍可写的 manifest 同步记录，不能暗示 evidence 已完整持久化。

`main` 只负责 signal-derived context、参数解析、依赖装配、一次性错误渲染与 exit code。
内部错误小写并使用 `%w` 保留 cause；预期错误不得 panic。stdout 输出机器可读结果，
诊断写 stderr。`context.Context` 作为首参传播到 adapter，不存入 struct。

## 10. 测试与验收

Go 测试与源码同目录，使用具名 table-driven subtests；独立用例可并行，状态机用例保持
确定性串行。覆盖：

- Shopify 头部注释、page/product/collection 名称与非法 path；
- `--page` alias、未知顶层字段和数字表示保留；
- sections/order 类型、重复 order key 与 25 Section 上限；
- 完整、Section、`custom_css` 三种同步；
- 非目标 settings、blocks、Sections、order 不变化；
- `custom_css` 缺失和全部错误类型；
- stable added/removed/changed/order diff 与 allow-remove gate；
- source/target 解析失败、target Live 阻断、target identity/role 漂移；
- dry-run、no-op、全目标 preflight、串行写入、即时 readback；
- write failure、canonical readback mismatch、停止后续写入与 `PARTIAL`；
- evidence 完整性、跨 cwd、路径逃逸与 symlink 防护；
- plan/manifest unknown/duplicate field、派生 invariant 和 secondary evidence failure；
- fake/local adapter 下 mutation call count 与顺序。

验收至少运行 `go test -count=1 -race ./...`、`go vet ./...`、shell syntax、
skill-creator validation、`npx skills` discovery/copy-install、跨 cwd 执行、公开内容扫描、
严格 OpenSpec 校验与仓库 `npm test`。所有 fixtures 使用合成 store/theme/template，
测试不得调用真实 Shopify。

## 11. 旧 Node 行为迁移边界

直接迁移或等价保留：通用 `--template`、deprecated `--page` alias、Shopify 前导注释解析、
Section extract/replace、source predecessor order 插入、稳定 Section diff、canonical hash、
removed gate、dry-run/execute、同店跨主题与跨店语义。

重新组合的成熟邻接行为：旧 `sync-page` 写入后没有做 canonical readback；新实现将其
audit canonical hash 与相邻 push/readback 模式组合为强制即时 readback。

明确移除：golden/all/exclude 店铺默认值、真实域名/theme ID、自动 staging theme
duplicate、`code/sections` Liquid 扫描、媒体 plan/视频清理、项目根 `tmp-sync`、业务
backup/log 路径、项目 dotenv/Admin helper 与 shell command string 拼接。

# Implementation Plan

**Goal:** 交付可从 tracked snapshot 独立安装的 `theme-template-sync` Skill，以 Go
确定性执行器安全同步任意 Shopify JSON template、单个 Section 或受控
`custom_css` 字段。

**Architecture:** Skill 目录包含薄 POSIX launcher、Go CLI、Shopify CLI adapter、fake
adapter、通用 JSONC document model、纯 transform/diff engine、plan-bound apply 状态机和
受控 evidence store。CLI 的唯一写路径是 `plan -> apply preview -> apply --execute`；所有
targets 在 mutation 前完成只读 preflight，写入按参数顺序串行并立即 canonical readback。

**Tech stack:** Go 1.25.1 standard library、POSIX `sh`、Node.js `node:test` 仓库验收、
OpenSpec 1.7.0、Skills CLI 1.5.21。

**Authoritative specification:** `proposal.md`、`design.md` 与
`specs/theme-template-sync/spec.md`。本文件是唯一任务状态事实源，不再创建第二份实施计划。

## Global constraints

- [x] 所有自动化测试只使用合成 fixture、fake adapter 或 fake Shopify CLI；不得读取
  `.env`、token、cookie、凭据或调用真实 Shopify。
- [x] 不修改或安装到 `shopify-theme-next`，不让新 Skill 依赖
  `shopify-theme-multi`。
- [x] 不实现 theme create/duplicate、Live target override、Publish、隐式 target、任意
  JSON Pointer 或 Git 代码同步。
- [x] 每项行为先写失败测试，再写最小实现；实现阶段保留可复查的 RED/GREEN 命令证据。
- [x] 只在本 change 的功能分支提交；不 push、PR、Release、Publish 或真实写入。
- [x] 规格 commit 与实现 commit 独立；实现默认汇总为一个可验证、可回滚的本地 commit，
  不按文件类型机械拆分。

## 1. 固定仓库验收入口

### Files

- Modify: `package.json`
- Modify: `scripts/tests/skills-discovery.test.mjs`
- Modify: `scripts/tests/sandbox-install.test.mjs`
- Create: `scripts/tests/theme-template-launcher.test.mjs`

### Steps

- [x] 将 discovery 预期从一个 Skill 改为两个，并分别断言
  `shopify-media-sync` 与 `theme-template-sync`，继续排除 `openspec-*`。
- [x] 扩展 tracked-snapshot copy-install 测试：断言安装两个 Skill、两个 lock entry 的
  SHA 格式和 `verifyInstalledSkill(...).status === "MATCH"`；新 Skill 只运行 `--help`，
  不执行远端命令。
- [x] 为新 POSIX launcher 写仓库外 cwd 测试：显式 fake binary 接收原样 argv 且 cwd
  不变并覆盖 stale caller env；显式 binary exit 4 和 bundled executor exit 2 原样透传；
  `PATH` 中无 Go 且未指定 binary 时 stdout 为
  `{"status":"NEEDS_SETUP","reason":"trusted-binary-and-go-missing"}`、exit 2。
- [x] 先运行
  `node --test scripts/tests/skills-discovery.test.mjs scripts/tests/sandbox-install.test.mjs scripts/tests/theme-template-launcher.test.mjs`
  并确认因新 Skill 尚不存在而失败，而不是网络、凭据或 fixture 原因。
- [x] 将 `test:go` 扩展为依次测试两个独立 Go module；将 `test:shell` 扩展为对两个
  wrapper 执行 `sh -n`。此时不放宽旧 Skill 的任何断言。

## 2. 建立安装单元与 CLI 外壳

### Files

- Create: `skills/theme-template-sync/scripts/template-sync-go/go.mod`
- Create: `skills/theme-template-sync/scripts/template-sync-go/cmd/theme-template-sync/main.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/app/run.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/app/run_test.go`
- Create: `skills/theme-template-sync/scripts/theme-template-sync.sh`

### Contract

```go
func Run(
    ctx context.Context,
    args []string,
    stdout io.Writer,
    stderr io.Writer,
    deps Dependencies,
) int
```

### Steps

- [x] 先测试 `--help` exit 0、未知 command/flag exit 2、stdout 只含机器结果、stderr
  只含诊断，且 dependency 未建立时不得 panic。
- [x] 建立 `module theme-template-sync`、`go 1.25.1`，不增加第三方依赖。
- [x] `main` 使用 `signal.NotifyContext`，只装配 production dependencies、调用一次
  `app.Run` 并 `os.Exit`；context 不存入 struct。
- [x] wrapper 按“可执行的 `THEME_TEMPLATE_SYNC_BINARY` -> bundled Go source ->
  structured `NEEDS_SETUP`”选择运行时。source fallback 以内部
  `THEME_TEMPLATE_SYNC_CALLER_CWD` 传递原调用目录，在临时目录 `go build` 后执行并清理；
  build 固定 local toolchain/off workspace/empty GOFLAGS；CLI 对用户路径仍以原调用目录解析，
  应用 exit code 不得被 `go run` 折叠。
- [x] 运行 `go test ./cmd/theme-template-sync ./internal/app` 和
  `node --test scripts/tests/theme-template-launcher.test.mjs`，确认外壳 GREEN。

## 3. Template key 与 Shopify JSONC document model

### Files

- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/templatejson/key.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/templatejson/key_test.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/templatejson/document.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/templatejson/document_test.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/templatejson/canonical.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/templatejson/canonical_test.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/templatejson/testdata/*.jsonc`

### Contracts

```go
type TemplateKey string

func ParseTemplateKey(template, page string) (TemplateKey, []string, error)
func (k TemplateKey) AssetPath() string

type Document struct {
    Header []byte
    Root   map[string]any
}

func Parse(data []byte) (Document, error)
func (d Document) Validate() error
func (d Document) Clone() (Document, error)
func (d Document) Canonical() ([]byte, error)
func (d Document) Render() ([]byte, error)
func (d Document) CanonicalSHA256() (string, error)
```

### Steps

- [x] 先用 table-driven tests 覆盖 `page.example`、`product.lp4`、
  `product.lx2_42w`、`collection.example`、`customers/account`、deprecated `--page` alias、
  双入口冲突，以及多层/空 segment/backslash/absolute/`..`/`.json`/`templates/` 注入。
- [x] 实现只返回 `templates/<key>.json` 的 `TemplateKey`；warning 只用于 alias，不建立
  Page 专属模型。
- [x] 先测试 BOM、前导空白、连续 Shopify block/line comments、正文 comment、尾随垃圾、
  第二 root value、非 object root、root/嵌套 duplicate object key 与 JSON parse error。
- [x] 用 `json.Decoder.UseNumber()` 解析恰好一个 root object；只剥离前导 comment，保存
  target Header，拒绝 JSON5 其它语法。
- [x] 先测试未知顶层字段、精确 number token、object key 顺序无关、array 顺序敏感和
  canonical hash 重复运行稳定。
- [x] 实现通用 `map[string]any` clone 与 canonical encoding；object keys 排序、array
  保序、`json.Number` 不转 float，canonical 输出为单个 UTF-8 JSON object 加换行。
- [x] 先测试 `sections` 非 object、`order` 非 string array、重复 key、超过 25 项；实现
  `Validate`，并对 source、target、planned 复用同一验证。
- [x] 运行 `go test -count=1 ./internal/templatejson`，确认全部 GREEN。

## 4. 纯同步变换与稳定 diff

### Files

- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/syncengine/scope.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/syncengine/transform.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/syncengine/transform_test.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/syncengine/diff.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/syncengine/diff_test.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/syncengine/testdata/*.jsonc`

### Contracts

```go
type Mode string

const (
    ModeTemplate Mode = "template"
    ModeSection  Mode = "section"
    ModeField    Mode = "field"
)

type Scope struct {
    Mode       Mode   `json:"mode"`
    SectionKey string `json:"section_key,omitempty"`
    Field      string `json:"field,omitempty"`
}

func Transform(source, target templatejson.Document, scope Scope) (templatejson.Document, error)
func Compare(before, planned templatejson.Document) (Diff, error)
func (d Diff) HasRemoval() bool
```

### Steps

- [x] 先测试完整模式 planned canonical 完全等于 source，包含未知顶层字段，且 target
  comment/格式差异不影响语义；target read 不存在由 adapter/preflight fail closed。
- [x] 先测试 Section 已存在时只替换该 Section、其它 Section/未知字段/order 不变；
  缺失时按 source 最近可用前驱插入，找不到前驱时追加，existing Section 不在 order 时
  补入，超过 25 项失败。
- [x] 先测试 field 模式只接受 `custom_css`；source/target Section 或 field 缺失、
  `null`、string、object、number、mixed array 均报路径化错误。
- [x] 实现 field 模式只替换目标 `[]string`，并用 canonical before/after 断言目标 Section
  的 settings、blocks、其它字段、其它 Sections、order 与未知顶层字段不变化。
- [x] 先构造同时含 added/removed/changed/order/Section summary 的 fixture，连续运行两次并
  断言 marshaled diff 字节相同；只有 comment/format/key order 差异时 `Diff.Empty()`。
- [x] diff path 使用稳定的 dot/index 表达，object key 排序遍历；`order` 单独输出 before、
  after、added、removed、moved，避免与通用 array diff 重复。
- [x] `HasRemoval` 覆盖 object/Section/array/order 删除；`custom_css` 数组缩短必须为
  removal，scalar 内容替换只算 changed；container 被 scalar/null/其它 container 替换时，
  消失后代同时计入 removed。
- [x] 运行 `go test -count=1 ./internal/syncengine`，确认全部 GREEN。

## 5. Adapter 边界与 production argv

### Files

- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/adapter/types.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/adapter/fake.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/adapter/fake_test.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/adapter/shopify_cli.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/adapter/shopify_cli_test.go`

### Contracts

```go
type ThemeAdapter interface {
    ResolveTheme(context.Context, StoreRef, ThemeRef) (ResolvedTheme, error)
    ReadTemplate(context.Context, ResolvedTheme, templatejson.TemplateKey) ([]byte, error)
    WriteTemplate(context.Context, ResolvedTheme, templatejson.TemplateKey, []byte) error
}

type CommandRunner interface {
    Run(context.Context, string, []string, string) ([]byte, error)
}
```

### Steps

- [x] 先测试 store/theme 空值和不安全 control/path 输入在 runner 前拒绝；合法参数始终作为
  独立 argv，不经 `sh -c`。
- [x] FakeAdapter 记录单调序号的 resolve/read/write calls，并支持按 target/call 注入
  resolve/read/write failure、role drift 和 readback body；测试可断言 mutation count、
  最大并发与调用顺序。
- [x] 用 fake runner 先测试 theme list JSON 的 ID string/number、精确 ID、唯一精确 name、
  零命中、名称多命中、未知/空 role 和 CLI failure。
- [x] 实现 list argv 仅为 `theme list --store <store> --json`；规范化 Live 判断时大小写
  不敏感，但不猜测主题。
- [x] 用临时目录测试 pull argv 含唯一 asset path、读取缺失文件返回 typed not-found；push
  argv 仅含 `theme push --path <tmp> --store <store> --theme <id> --only
  templates/<key>.json --nodelete --json`。
- [x] 断言 production adapter 从不生成 create/duplicate/publish/`--live`/`--allow-live`/
  `--unpublished` flag，也不读取 `.env`；移除所有继承的 `SHOPIFY_FLAG_*` 行为覆盖。
- [x] CLI stderr 只传播 bounded credential-redacted 摘要；push 成功后的 cleanup failure
  使用 typed outcome，使编排层仍执行 readback。
- [x] 运行 `go test -count=1 ./internal/adapter`，确认全部 GREEN。

## 6. Evidence store 与 plan/preview binding

### Files

- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/evidence/model.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/evidence/store.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/evidence/store_test.go`

### Contracts

```go
type Store struct {
    root *os.Root
}

func Open(root string) (*Store, error)
func (s *Store) WriteJSON(relative string, value any) error
func (s *Store) WriteBytes(relative string, data []byte) error
func (s *Store) ReadJSON(relative string, value any) error
func (s *Store) Close() error
```

```go
type PreviewBinding struct {
    PlanSHA256    string           `json:"plan_sha256"`
    AllowRemove  bool             `json:"allow_remove"`
    Source       ResolvedIdentity `json:"source"`
    Targets      []BoundTarget    `json:"targets"`
}
```

### Steps

- [x] 先测试默认 root 从 caller cwd 形成
  `.runtime/theme-template-sync/<UTC timestamp-random run-id>`，run-id 不接受用户输入。
- [x] 先测试绝对相对路径、`..`、空 segment、root 内 symlink file/dir 与已有 symlink
  manifest 均 fail closed，root 外 victim 保持不变。
- [x] 使用 Go 1.25 `os.OpenRoot`/`os.Root` 约束所有 artifact；文件先写同目录随机 temp、
  `Sync` 后原子 rename，JSON 固定缩进和尾随换行，权限不宽于 `0700/0600`。
- [x] 测试并实现 plan SHA：对不含自身 hash 的稳定 JSON payload 计算 SHA-256；plan 显式
  路径可位于 caller cwd 下，但 apply 不查找“最新”文件。
- [x] 测试 preview binding 原子写入 manifest，且 plan SHA、source/target identities、
  before hashes、template/scope 与 allow-remove 任一变化都产生明确 mismatch。
- [x] manifest 计算自身 SHA 并与显式 plan 的 path/SHA、run ID、template、scope、targets、
  派生 status 和 preview binding 全量验证；plan/manifest strict decode 拒绝 unknown/duplicate
  字段；终态按 APPLIED count、唯一失败 target/failure kind 和 target 顺序重算，审计加载在
  plan 存在时执行相同 validation；runtime root 用永久 reservation 防并发抢占，apply 在读取
  authority 前用 run-scoped exclusive lease 防止同 plan 重入。
- [x] 测试 manifest/失败文件不会序列化环境、token、cookie 或 command output 中的
  credential-shaped value；`Authorization` 与 `Cookie`/`Set-Cookie` 多参数/多值 header 必须
  整段清洗，错误只保留类别和已清洗摘要。
- [x] `CreatePlan` 可创建全新的受控 run root；`apply` 只能打开既有 run root，错拼或不存在
  的 plan path 不创建目录且 adapter call count 为零。
- [x] 运行 `go test -count=1 ./internal/evidence`，确认全部 GREEN。

## 7. Plan 与只读 preview 编排

### Files

- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/app/options.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/app/options_test.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/app/model.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/app/plan.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/app/plan_test.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/app/apply.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/app/apply_preview_test.go`

### Steps

- [x] 先测试固定命令树 `plan` 与 `apply`，显式 source store/theme、成对 repeatable target
  store/theme、输入顺序、`--template`/`--page`、三种 scope 和只允许 field
  `custom_css`；数量不等或空值时 adapter call count 为零。
- [x] `plan` 顺序执行 source resolve/read/parse，再为全部 targets resolve/Live check/read/
  parse/transform/diff；任一失败返回 `FAILED` 且 write count 为零。
- [x] plan evidence 写 source before/canonical 和每 target before/planned/diff/manifest，状态仅
  `NO_OP` 或 `PLANNED`；target template not-found 不转成 create。
- [x] 先测试任一 target 初始 role 为 Live 时所有 write 为零；source Live 仍可只读。
- [x] `apply --plan <path>` 默认重做全部只读 preflight，与 plan identity/hash/scope/diff
  对比；一致时写 preview binding，返回 `NO_OP` 或 `PLANNED`，write count 为零。
- [x] 先测试 source/target template 或 identity 漂移、plan 文件篡改、target 后序解析失败、
  removed 未授权都在全体 mutation 前失败。
- [x] 即使 plan 被重新计算 SHA，也要重算 source/target content hash、diff、target index/status
  和 overall status；CreatePlan 成功落盘前先自校验，manifest tamper 在任何 adapter 调用前拒绝。
- [x] removed plan 允许生成和 preview；binding 中冻结 `allow_remove`，preview/execute 参数
  不一致时要求重新 preview。
- [x] 运行
  `go test -count=1 ./internal/app -run 'Test(ParseOptions|Plan|ApplyPreview)'`，确认 GREEN。

## 8. 串行 execute、即时 readback 与终态

### Files

- Modify: `skills/theme-template-sync/scripts/template-sync-go/internal/app/apply.go`
- Create: `skills/theme-template-sync/scripts/template-sync-go/internal/app/apply_execute_test.go`
- Modify: `skills/theme-template-sync/scripts/template-sync-go/internal/evidence/model.go`

### Steps

- [x] 先测试 execute 没有 matching preview binding 时拒绝；no-op targets 保持 `NO_OP` 且
  永不 write。
- [x] 先测试 execute 在任何 write 前重做全部 target preflight；后序 target failure 时
  失败 target 为 `PREFLIGHT_FAILED`，后续 `READY` 为 `SKIPPED_AFTER_FAILURE`、`NO_OP`
  保持不变且 write count 仍为零；无 mutation 时允许重新 preview。
- [x] 每个 READY target 写前再次 resolve，并精确比较 store/ID/name/role；role 变 Live、
  identity drift 或 before canonical hash drift 时停止且不得写该 target。
- [x] WriteTemplate 成功后立即 ReadTemplate；只有 canonical hash 等于 planned hash 才标
  `APPLIED` 并进入下一 target。格式/comment 差异必须成功。
- [x] 先测试两个及以上 targets 的 call sequence 为
  `resolve/read ... all-preflight -> resolve/read/write/read target-0 ->
  resolve/read/write/read target-1`，最大 write concurrency 为 1。
- [x] 先测试首个 write failure => `FAILED`/`WRITE_FAILED`，首个 readback mismatch =>
  `READBACK_MISMATCH`，已有 APPLIED 后失败或 mismatch => `PARTIAL`，剩余 targets =>
  `SKIPPED_AFTER_FAILURE`。
- [x] 写入后无自动 rollback/retry；manifest 保留 failure kind、write/readback evidence 和
  before/planned/after hash，after/failure artifact 在已知时立即落盘。
- [x] primary failure 后的 failure/manifest persistence error 必须更新最终安全 message，并在
  仍可写的 manifest 记录，不能假定 evidence 完整。
- [x] adapter write/readback 前分别持久化 `WRITE_IN_PROGRESS`/`READBACK_IN_PROGRESS`；
  `EXECUTING` crash checkpoint 与所有 mutation intent 使同一 run 不可重入。
- [x] 并发 execute+execute、execute+preview 的第二个 apply 在 adapter call count 为零时拒绝，
  不覆盖先行 mutation manifest；正常只读退出释放 lease，mutation 或 cleanup 不确定时保留。
- [x] `plan.json` 或成功 plan manifest 落盘失败时，result 保留 run ID、plan path/SHA、
  evidence root、targets 和 primary failure，并尽力写 failure manifest。
- [x] readback transport/parse/audit failure 为 `READBACK_FAILED`，只有有效 canonical hash
  不等才是 `READBACK_MISMATCH`；matching readback 后 cleanup/evidence failure 为 `PARTIAL`。
- [x] `Run` 将 `NO_OP`/`PLANNED`/`APPLIED` 映射 exit 0，输入、只读 preflight 中断与安全错误
  映射 exit 2，`WRITE_FAILED`/`READBACK_FAILED`/`EVIDENCE_FAILED` 的 `FAILED` 与
  `READBACK_MISMATCH` 映射 exit 3，`PARTIAL` 映射 exit 4；只读 preview 的 lease cleanup
  failure 也按 `EVIDENCE_FAILED`/exit 3，结果 JSON 只输出一次。
- [x] 运行
  `go test -count=1 ./internal/app -run 'TestApplyExecute'`，确认全部 GREEN。

## 9. Skill 指令、合同与安装元数据

### Files

- Modify: `README.md`
- Modify: `docs/verification.md`
- Modify: `docs/dependencies.md`
- Modify: `docs/maintenance.md`
- Create: `skills/theme-template-sync/SKILL.md`
- Create: `skills/theme-template-sync/agents/openai.yaml`
- Create: `skills/theme-template-sync/references/cli-contract.md`
- Create: `skills/theme-template-sync/references/result-contract.md`
- Modify: `scripts/tests/sandbox-install.test.mjs`

### Steps

- [x] SKILL.md 保持精简：何时使用、何时不要使用、默认 dry-run、plan/preview/execute
  授权、Live/Publish/theme create 禁止、多目标 preflight/readback、结果解释与 evidence 路径。
- [x] CLI contract 列出通用 template、deprecated page alias、source/target pairing、scope、
  allow-remove、plan 路径、runtime root 和完整示例；不含真实 store/domain/theme ID。
- [x] Result contract 列出 target/overall 状态、exit code、partial/readback mismatch、artifact
  authority 和明确 Cannot Claim。
- [x] `agents/openai.yaml` display name 为 `Theme Template Sync`，description 明确适用于
  Shopify JSON templates，default prompt 使用 `$theme-template-sync`，metadata 不暗示官方
  Shopify 产品。
- [x] copy-install 测试运行安装副本 wrapper `--help`，从 caller cwd 执行且不写 Skill
  安装目录；lock hash 用仓库 verifier 验证 `MATCH`。
- [x] 仓库 README、verification、dependencies 与 maintenance 文档列出两个 Skill、各自
  runtime 边界和通用 copy-install/verifier 用法，不把本地实现描述为已发布 Release。
- [x] 运行
  `python3 <skill-creator-dir>/scripts/quick_validate.py skills/theme-template-sync`
  与三个 Node 安装/discovery/launcher tests，确认 GREEN。

## 10. 迁移审计、全量验证与本地提交

### Files

- Modify: `openspec/changes/add-theme-template-sync/tasks.md`
- Inspect only:
  - `shopify-theme-multi/scripts/ops/sync-page.js`
  - `shopify-theme-multi/scripts/ops/lib/sync-page-core.js`
  - `shopify-theme-multi/scripts/ops/lib/page-json.js`
  - 同目录 audit/config/tests/callers

### Steps

- [x] 对照迁移审计表确认直接迁移：template/page alias、前导 comment、Section replacement、
  source-predecessor order、stable diff/hash、removed gate、dry-run/execute、跨店/跨主题。
- [x] 确认移除耦合：golden/all/exclude、真实 store/theme config、dotenv/secret helper、
  staging duplicate、Liquid/media/Git sync、业务 tmp/backup/log、shell command strings。
- [x] 用合成 fixture 运行 `go test -count=1 -race ./...` 和 `go vet ./...`（新 module），
  再对旧 module 运行既有 `go test ./...`，不得把 baseline 失败误报为本 change 成功。
- [x] 运行 `sh -n` 两个 wrapper、skill validator、Node tests、公开内容扫描、
  `OPENSPEC_TELEMETRY=0 npx --yes @fission-ai/openspec@1.7.0 validate --all --strict --no-interactive`
  与完整 `npm test`。
- [x] 从干净 tracked snapshot copy-install 两个 Skills，确认新 Skill 在 provider 外 cwd
  `--help` 成功且安装目录无 runtime evidence。
- [x] 检查 `git diff --check`、`git status --short`、tracked 文件名与公开扫描；确认没有
  `.env`、credential、真实 domain/theme ID、个人绝对路径、runtime evidence 或消费项目
  改动。
- [x] 将所有已完成 checkbox 更新为 `[x]`；只记录实际通过的验证，Release、真实 Shopify、
  Preview、Publish、Live 与 `shopify-theme-next` rollout 保持 Cannot Claim。
- [x] 创建一个本地实现 commit：
  `feat(ops): 新增主题 JSON 模板安全同步 Skill`。不 push、不创建 PR、不安装到消费项目。

# CLI Contract

入口可从任意消费项目 cwd 调用：

```bash
<skill-dir>/scripts/theme-template-sync.sh --help
```

launcher 按显式 `THEME_TEMPLATE_SYNC_BINARY`、临时编译 bundled Go source、结构化
`NEEDS_SETUP` 的顺序选择运行时，不下载 Release 或 `latest`。临时 binary 会保留应用的
原始 exit code，执行后清理；build 会固定 `GOWORK=off`、`GOTOOLCHAIN=local` 并清空继承
`GOFLAGS`，避免 workspace/flag 注入或隐式 toolchain 下载。相对路径始终以调用者 cwd 解析。

## Plan

```bash
<skill-dir>/scripts/theme-template-sync.sh plan \
  --template page.example \
  --source-store store-source \
  --source-theme "Source Theme" \
  --target-store store-one \
  --target-theme "Target Theme One" \
  --target-store store-two.myshopify.com \
  --target-theme 202 \
  --mode field \
  --section synthetic_section_A1 \
  --field custom_css
```

- `--template` 接受不带 `templates/` 与 `.json` 的 key，例如 `page.example`、
  `product.lp4`、`product.lx2_42w`、`collection.example`、`index` 或受控单层子目录
  `customers/account`。
- deprecated `--page example` 严格映射为 `--template page.example`；不能与
  `--template` 同用。
- `--source-store`、`--source-theme` 必填。`--target-store` 与 `--target-theme` 必须成对
  重复，输入顺序即 plan 和 execute 顺序。
- `--mode template` 不接受 Section/field；`--mode section` 要求 `--section`；
  `--mode field` 要求 `--section` 和 `--field custom_css`。
- `--allow-remove` 表示当前 plan 已选择允许 reviewed removals；它不会跳过 preview 或
  用户写入授权。
- `--runtime-root <path>` 可显式覆盖本次 evidence root；默认是
  `.runtime/theme-template-sync/<run-id>`。为保留既有审计证据，显式 root 必须为空或尚不
  存在、位于调用者 cwd 内且权限不宽于 owner-only；工具不会改权、覆盖或复用旧 run。

store 接受安全的 store prefix 或 `<prefix>.myshopify.com`。theme reference 是精确 ID 或
唯一精确名称；名称不会按 contains、最近更新时间或列表顺序选择。

## Preview and execute

```bash
<skill-dir>/scripts/theme-template-sync.sh apply --plan <run>/plan.json
<skill-dir>/scripts/theme-template-sync.sh apply --plan <run>/plan.json --execute
```

含 removal 且已批准时，三个阶段都必须携带相同决策：

```bash
<skill-dir>/scripts/theme-template-sync.sh plan ... --allow-remove
<skill-dir>/scripts/theme-template-sync.sh apply --plan <run>/plan.json --allow-remove
<skill-dir>/scripts/theme-template-sync.sh apply --plan <run>/plan.json --allow-remove --execute
```

`apply` 只接受显式 plan path，不支持 `last`、mtime 或自动寻找最近 run。execute 会要求
manifest 已存在 matching preview binding，并再次执行全目标 preflight。plan 与 manifest
都会校验自身 SHA、派生 diff/status、target 顺序和完整 authority；同一 resolved store/theme
以不同别名重复出现也会拒绝。`apply` 只打开既有 run root；plan path 不存在或错拼时不会
创建其父目录，也不会调用 adapter。

## Production adapter

首期 production adapter 只调用以下 Shopify CLI 形状，参数均为独立 argv：

- `shopify theme list --store <store> --json`
- `shopify theme pull --path <temp> --store <store> --theme <id> --only templates/<key>.json --nodelete`
- `shopify theme push --path <temp> --store <store> --theme <id> --only templates/<key>.json --nodelete --json`

它不使用 `--live`、`--allow-live`、`--publish`、`--unpublished`、`--development`，也不
执行 create/duplicate。执行 Shopify CLI 时会移除继承环境中的全部 `SHOPIFY_FLAG_*`
行为覆盖，避免环境变量隐式注入 publish/Live flags；既有 CLI 会话与
`SHOPIFY_CLI_THEME_TOKEN` 等认证环境仍原样传递。CLI 失败只回传有界且脱敏的 stderr
摘要；不会把原始 command output 写入 evidence。push 已被 CLI 接受但本地临时目录清理
失败时，执行器仍必须立即 readback，并以 `PARTIAL`/`EVIDENCE_FAILED` 报告。

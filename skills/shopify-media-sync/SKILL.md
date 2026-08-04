---
name: shopify-media-sync
description: Use when an agent must plan, preview, execute, supervise, inspect, or recover deterministic Shopify Files media synchronization from local CSV, XLSX, JSON, zip, or optional Lark/Feishu Sheet and Drive inputs.
---

# Shopify Media Sync

使用 Skill 目录内的 `scripts/shopify-media-sync.sh` 运行命令。首次运行或出现 `NEEDS_SETUP` 时读取 [dependency-contract.md](references/dependency-contract.md)；设置 store、API 版本或鉴权时读取 [config-contract.md](references/config-contract.md)；判断终态或恢复动作时读取 [result-contract.md](references/result-contract.md)。

## 安全边界

- `plan` 只生成本地固定计划，不写 Shopify。
- `apply` 必须消费显式 `--plan <plan-path>`，默认只 preview；仅在用户批准当前 plan、store、row、action 后使用 `--execute`。
- 不扫描或猜测“最近一次 plan”，不静默生成新 plan，不扩大 store、locale、template 或 action。
- 不打印 `.env`、token、cookie 或 secret。真实远端命令可按配置合同加载凭据；诊断和初始化只能使用下述显式入口。
- 不 publish、不操作 live theme。`json_replace_needed` 由外部 theme 工作流处理，CLI 必须停在 `WAITING_EXTERNAL`。
- Sheet/Drive adapter 是可选能力；缺少 `lark-cli` 只阻断对应输入，不阻断本地 CSV/XLSX/JSON/zip/directory。

## 入口与诊断

入口可从任何工作目录调用，用户相对路径按调用目录解析。launcher 只使用显式可信 binary、固定版本且 digest 匹配的 Release asset，或本机 Go source fallback；不会执行 `latest` 或未知 binary：

```bash
<skill-dir>/scripts/shopify-media-sync.sh --help
<skill-dir>/scripts/shopify-media-sync.sh --json doctor
```

仓库级本地运行时使用 ignored `.env.local`。首次初始化或从已授权的旧配置导入时：

```bash
<skill-dir>/scripts/shopify-media-sync.sh env-init --root . --dry-run --format json
<skill-dir>/scripts/shopify-media-sync.sh env-init --root . --source <approved-env-path> --format json
<skill-dir>/scripts/shopify-media-sync.sh env-check --root . --format json
```

`env-init` 只导入受支持的 Shopify 鉴权变量，创建权限为 `0600`，已有 `.env.local` 时拒绝覆盖。`env-check` 不调用 Shopify，只检查文件、权限和变量完整性。

`doctor` 默认只诊断进程环境；只有显式传入 `--env-file <path>` 才只读检查该文件。它不调用 Shopify、不修改进程环境、不写 artifact，也不输出凭据值。缺少远端配置时仍返回结构化 `NEEDS_SETUP`，供 AI 给出下一步。

`doctor.capabilities.local_input` 与 `doctor.capabilities.sheet_input` 分开报告。不要把可选 Sheet 依赖缺失描述成整个 Skill 不可用。

## 标准流程

1. 用本地输入生成 plan：

```bash
<skill-dir>/scripts/shopify-media-sync.sh plan --input media.csv --source-root ./images --stores store-us --out-dir .runtime/media-sync/store-us/run-id
```

只有用户明确选择 Sheet/Drive 输入时才调用相应 adapter，并先根据 `doctor` capability 确认可用。

2. 检查 `plan.json`、`desired.json`、resource SHA 与 action scope。`plan.summary.errors` 仅表示规划错误。
3. 固定命令模型为 `plan -> apply --plan <plan-path> -> apply --plan <plan-path> --execute`。先 preview：

```bash
<skill-dir>/scripts/shopify-media-sync.sh apply --plan <plan-path> --format json
```

preview 会把 plan SHA、选定 stores 和 `stores.config.json` 的 path/SHA 写入 `evidence.json.preview_binding`。如果 preview 使用了 `--stores` 或 `--stores-config`，execute 必须原样携带；任一 identity 漂移都应重新 preview，不得直接写入。

4. 展示 plan path、plan SHA、stores、rows/actions；获得真实写入授权后，用与 preview 相同的 scope/config 执行：

```bash
<skill-dir>/scripts/shopify-media-sync.sh apply --plan <plan-path> --execute --format json
```

5. 监督到 terminal summary。`apply-summary.json` 是整体 authority，`evidence.json` 是行与阶段恢复 authority；stdout 仅作进度。

仅在兼容排障时直接使用 `upload`、`alt`、`verify`；正常执行必须使用 `plan` + `apply`，不得由 AI 手工串联阶段。

## 冷启动与恢复

会话中断后必须先取得明确 plan 路径；没有时请求路径，不得选择最近一次 plan。随后只读检查：

```bash
<skill-dir>/scripts/shopify-media-sync.sh inspect --plan <plan-path> --format json
```

按顺序读取 `run_id`、`plan_path`、`plan_sha256`、stores/rows/actions、`resource_integrity`、`safe_to_execute`、`final_status`、`current_stage`、errors、`last_attempt.duration_ms` 与 `next_safe_action`。`apply-attempts.jsonl` 是 append-only attempt history；`MISSING_LEGACY` 不等于未执行，`CORRUPT`/`MISMATCH` 或 stale `RUNNING` 必须 fail-closed。

恢复规则：

- SHA 一致且 upload 已 `SUCCEEDED`：跳过 mutation。
- `AWAITING_READBACK` 且已有 media id：只继续 readback，不重复 mutation。
- alt/translation mutation 已接受：只恢复对应 readback。
- mutation transport 结果含糊或 legacy 状态含糊：进入 `NEEDS_ATTENTION`，不得盲目重跑旧 plan。
- mutation 前失败且 `retryable=true`：可按 evidence 重试。
- 资源 SHA 漂移：拒绝 apply，重新 plan。
- VIDEO 目标已存在同名文件但没有 SHA 匹配的成功 evidence：拒绝复用或创建 UUID 后缀副本，进入人工确认。
- plan 或 store/config scope 在 preview 后变化：拒绝 execute；检查新范围后重新 preview，再单独获取真实写入授权。

## AI 监督纪律

- 禁止 fire-and-forget、`nohup`、detached process；不得用 `&` 启动后结束会话。
- upload/readback 并发只交给同一个 CLI 进程的 `--concurrency 1-3` bounded workers；不得用多个 AI Agent 或后台进程拆分同一 plan。
- apply 为 `RUNNING` 时持续观察并定期轮询，主动给用户简短进度。
- 读取机器 summary 和 evidence，不根据零散 stdout 推断结果。
- `SUCCEEDED` 才能报告成功；`PARTIAL_FAILURE`、`FAILED`、`NEEDS_ATTENTION`、`WAITING_EXTERNAL`、`CANCELLED` 必须主动报告 artifact、错误行和下一安全动作。
- `WAITING_EXTERNAL` 不是成功；说明外部单-template JSON 步骤和继续 verify 所需 mapping-level receipt，且不得暗示已执行 theme JSON、publish 或 live 操作。

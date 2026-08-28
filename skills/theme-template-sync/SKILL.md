---
name: theme-template-sync
description: Use when an agent must plan, preview, execute, or explain scoped synchronization of Shopify JSON templates between explicit stores and themes; not for theme code deployment, Publish, or Live-theme writes.
---

# Theme Template Sync

使用 Skill 目录内的 `scripts/theme-template-sync.sh`。本 Skill 适用于 Shopify Admin
中的 `templates/*.json`：完整 template、指定 Section，或唯一开放的字段
`sections.<sectionKey>.custom_css`。Git 跟踪代码、Liquid、theme deploy、Publish 与 Live
操作不属于这条同步链路。

需要构造命令或解释 flags 时读取 [cli-contract.md](references/cli-contract.md)；判断终态、
失败与恢复动作时读取 [result-contract.md](references/result-contract.md)。

## 权限与安全边界

- 默认只运行 `plan` 或 `apply` preview；没有用户对当前 plan SHA、source、targets、scope
  和 removal 决策的明确写入授权，不得加 `--execute`。
- source store/theme 与每个 target store/theme 必须显式提供。不得猜测默认 theme、最近
  theme、golden store 或全部店铺。
- source 可以是 Live 只读；target 在 plan、preflight 或写入前为 `main`/`live` 都必须
  阻断。不得创建/复制主题，不得 Publish，也不存在 Live override。
- removed 内容默认阻断 execute。只有 plan、preview 与 execute 都使用相同显式
  `--allow-remove`，且用户已经审查 diff，才可继续。
- 多目标先全部完成只读 preflight，再按参数顺序串行写入；每次写入后立即 canonical
  readback。首个即时 preflight、write、readback 或 evidence failure 后停止，不自动
  rollback 或重试。
- 不读取 `.env`、token、cookie 或凭据文件。Shopify 访问只使用调用环境已有的 Shopify
  CLI 登录；缺少登录或 CLI 时报告实际阻断。

## 标准流程

1. 确认通用 template key、三种 scope 之一、明确 source，以及有序 targets。字段模式只
   能选择 `custom_css`。
2. 运行 `plan`。检查 stdout result、`plan.json`、每个 target 的 `diff.json` 和
   `manifest.json`；不要把 `PLANNED` 描述成已写入。
3. 对同一显式 plan 运行不带 `--execute` 的 `apply --plan <path>`。它会重新读取全部
   endpoints 并写入 preview binding。
4. 向用户展示 plan path/SHA、解析后的 theme identities、scope、targets、added/removed/
   changed/order 和 removal 决策。获得对这份固定计划的明确授权后，才运行相同参数加
   `--execute`。
5. 监督命令至 terminal result，读取 manifest 与 target evidence；不要根据零散日志推断
   成功。

默认 evidence 位于调用者 cwd 的 `.runtime/theme-template-sync/<run-id>/`，而不是 Skill
安装目录。`NO_OP`、`PLANNED`、`APPLIED`、`FAILED`、`READBACK_MISMATCH` 与 `PARTIAL`
含义不同；特别是 `PARTIAL` 需要报告已确认 applied 的 targets、失败 target 和未执行的
targets。若 manifest 停在内部 checkpoint `EXECUTING`，远端结果未知；同一 run 必须保持
不可重入，并创建新 plan 做恢复检查。若 `.apply-lease` 存在，也不得手动删除或复用该
run；它表示另一个 apply 正在运行、进程曾中断，或当前 run 已进入 mutation 边界。

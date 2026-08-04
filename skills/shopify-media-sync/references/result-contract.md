# 机器结果合同

同一次 run 的权威 artifact：

- `plan.json`：批准范围与资源 SHA；
- `evidence.json`：逐 row、逐阶段 checkpoint 和恢复状态；
- `apply-summary.json`：当前整体终态、counts、artifacts、`next_safe_action`；
- `apply-attempts.jsonl`：append-only preview/execute attempt history；
- `report.csv`：人类可读行结果。

整体终态至少区分 `SUCCEEDED`、`PARTIAL_FAILURE`/`FAILED`、`NEEDS_ATTENTION`、`WAITING_EXTERNAL`、`CANCELLED`。只有 `SUCCEEDED` 返回成功；其它 terminal summary 返回非零 exit code。

行级 evidence 保存 store、row、source/target filename、resource SHA、file/media id、upload/alt/translation/verify 独立状态、last error 和 updated time。恢复决策以独立阶段状态为准，不以 artifact 存在或单一模糊 status 推断 mutation 是否发生。

`evidence.json.preview_binding` 只记录最近一次 preview 实际检查过的 plan SHA、store scope 与 stores config identity，不代表人类已经授权写入。真实写入授权仍只由后续显式 `apply --execute` 表达；execute identity 与 preview 不一致时必须 fail-closed。

`inspect --plan <path> --format json` 是冷启动读取入口。它不修改 artifact，并返回 plan identity、resource integrity、scope、last attempt、problems 与下一安全动作。

launcher 在没有可信 Release binary、显式 binary 或 Go fallback 时返回 exit code `2`，并输出 `command=launcher` 语义的结构化 `NEEDS_SETUP`；这不是业务 plan/apply 失败。可选 Sheet 依赖不可用则由 `doctor.capabilities.sheet_input` 或 plan preflight 单独报告，不影响 `local_input`。

真实远端命令必须输出 `target_api_versions` store-to-version mapping。该 mapping 是非敏感请求目标证据；不能用配置默认值代替实际解析结果。

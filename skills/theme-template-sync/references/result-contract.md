# Result and Evidence Contract

stdout 每次只输出一个 JSON result；diagnostic 写 stderr。exit code：

- `0`：`NO_OP`、`PLANNED` 或 `APPLIED`；
- `2`：参数、setup、plan integrity、binding、removal gate、preflight 或只读阶段中断；
- `3`：`READBACK_MISMATCH`，或 failure kind 为 `WRITE_FAILED`、`READBACK_FAILED`、
  `EVIDENCE_FAILED` 的 `FAILED`；evidence failure 也可能来自只读 preview 的 lease cleanup；
- `4`：`PARTIAL`。

## Overall status

- `NO_OP`：全部 targets canonical 等价，write call count 为零。
- `PLANNED`：plan/preview 有待应用差异，write call count 为零。
- `APPLIED`：每个有差异 target 都完成 write 和 matching canonical readback。
- `FAILED`：未确认任何 target applied 即发生 preflight、write、readback 或 evidence 失败；
  或只读阶段 fail closed。
- `READBACK_MISMATCH`：首个已接受 write 的 readback 不等于 planned，且此前没有 target
  已确认 applied。
- `PARTIAL`：至少一个 target 已确认 applied，之后发生失败；也包括 write/readback 已确认
  applied、但本地 cleanup 或 evidence checkpoint 失败。

target status 为 `NO_OP`、`READY`、`PREFLIGHT_FAILED`、`WRITE_IN_PROGRESS`、
`READBACK_IN_PROGRESS`、`APPLIED`、`WRITE_FAILED`、`READBACK_FAILED`、
`READBACK_MISMATCH`、`EVIDENCE_FAILED` 或 `SKIPPED_AFTER_FAILURE`。`READY` 只表示
dry-run 结果，不能称为已写入；`READBACK_FAILED` 表示无法取得、解析或审计读回，只有
取得有效 canonical 内容但 hash 不同才是 `READBACK_MISMATCH`。
全目标 preflight 的具体失败 target 标为 `PREFLIGHT_FAILED`；其后尚未检查的 `READY`
targets 标为 `SKIPPED_AFTER_FAILURE`，而 `NO_OP` targets 保持 `NO_OP`。

`EXECUTING` 只会出现在 manifest 的 durable intent checkpoint，不是正常 terminal stdout
status。进程中断后若停在该状态，无法假定远端已写或未写；同一 run 必须 fail closed。
plan、preview 或 execute 的全目标只读 preflight 若被取消，则返回
`FAILED`/`INTERRUPTED`、exit 2 且 write call count 为零；write/readback 阶段的取消仍按对应
执行期失败处理。

## Evidence authority

evidence root 成功创建后始终保留 `.run-reservation`，并尽力写入 `manifest.json`。每次
apply 会在读取 plan/manifest 与调用 adapter 前原子获取 `.apply-lease`；正常且尚未形成
mutation intent 的退出会释放它，一旦进入 write intent、进程中断或 lease 清理不确定就
保留并 fail closed。不得手动删除 lease 或复用该 run。其余文件按实际到达的阶段出现；
成功完成 plan 后才保证 `plan.json`、source 和全部 target 的 before/planned/diff：

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
targets/<index>/after.json      # 发生 readback 时
targets/<index>/failure.json    # 该 target 失败时
```

`manifest.json` 是整体终态和 preview binding authority；target 文件提供 before/planned/
after 与 failure 证据。canonical hash 忽略前导 Shopify comment、空白与 object key 顺序，
不忽略数组、order、未知字段或值差异。

plan 会校验 canonical template key、source/target content hash、重算 diff、target index/status
和 overall status；manifest 具有自身 SHA，并与 plan path/SHA、run ID、template、scope、
allow-remove、target 顺序/identity/hash/status 和 preview binding 全量绑定。任何不一致都在
adapter 调用前拒绝。plan/manifest schema 拒绝未知或重复字段；template JSON/JSONC 也拒绝
任意深度的 duplicate object key，避免文件可见内容被 decoder 静默丢弃。
manifest 的终态还会按 APPLIED count、唯一失败 target、failure kind、mutation evidence 与
targets 顺序重新派生；同目录 plan 存在时，审计加载也执行这份 plan-bound validation，而非
只检查可由文件作者重算的 manifest SHA。

write failure 或 readback mismatch 后不要自动 rollback、重复 write 或跳到下一 READY
target；后续 no-op target 仍保持 `NO_OP`。先
报告 plan path/SHA、overall status、每 target status、failure kind、已知 hashes 和下一步
需要的人工决定；后续动作必须重新做只读检查与 preview。

若记录 primary failure 时 `failure.json` 或终态 manifest 又发生 persistence failure，最终
failure kind 保留 primary 分类，但 stdout/stderr 和仍可写的 manifest 必须明确附加 evidence
未完整持久化；不得只报告第一层错误。

执行器会在远端 write 前先持久化 `WRITE_IN_PROGRESS`，在 readback 前先持久化
`READBACK_IN_PROGRESS`。一旦 manifest 记录任何 write/readback attempt，当前 run 的
mutation evidence 即不可重入覆盖；误重跑同一 plan 会在 adapter 调用前拒绝。恢复或再次
同步必须创建新的 plan/run，旧 manifest 保持最后一个 durable checkpoint 或终态。

同一 plan 的 preview/execute 不能并发：第二个 apply 在读取 authority 或调用 adapter 前
失败，不得覆盖第一个 apply 的 mutation checkpoint。若 `plan.json` 或 plan manifest 落盘
失败，result 仍报告已知的 run ID、plan path/SHA 与 evidence root，并尽力保留失败 manifest。

本地测试、安装、plan、Shopify write/readback、Preview、Publish 和 Live 是不同证据层。
本 Skill 的本地通过不能证明真实店铺写入、Publish、Live、Release 或消费项目 rollout。

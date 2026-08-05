# Measurement protocol

## Profile 边界

| Profile | Form factor | Throttling | 适用解释 |
| --- | --- | --- | --- |
| `desktop-observed-v1` | desktop | `provided` | 本机、当前网络/CPU 条件下的未模拟合成测量；不是 field/RUM/CrUX。 |
| `desktop-lab-v1` | desktop | `simulate` | Lighthouse desktop simulated lab 条件。 |
| `mobile-lab-v1` | mobile | `simulate` | Lighthouse mobile simulated lab 条件。 |

Profile 是编译内置合同，不接受调用者临时改 flags。每次 attempt 使用新 Chrome user-data directory；Lighthouse 固定为 13.4.1，并只运行 performance category。协议还记录 Node、Chrome、OS、arch、resolved flags 与 executor runtime flags。

## 采集协议

1. 在同一时段、相近主机负载下收集 baseline 与 candidate。
2. 每个对象运行一次 `collect --runs 5`；一个 command 产生一个 bundle，内部是 5 个顺序 attempt，不并发、不自动重试。
3. 每个 bundle 至少 3 个成功样本才可能聚合；任何 attempt 非 `OK` 会使正式结果成为 `PARTIAL`，不得用于比较。
4. 先分别 `inspect`。只有两个 bundle 均完整 `OK` 才进入 compare。
5. 仅在以下字段完全一致时比较：schema、profile、form factor、throttling、resolved/runtime flags、Lighthouse/Node/Chrome version、OS、arch 与 fingerprint。不要比较 `observed` 和 `lab`，也不要跨 runtime 强行解释 delta。

## 指标与统计

每个成功 LHR 提供一组独立样本：

- Performance Score：Lighthouse 官方 performance category score × 100，范围 0–100，越高越好。绝不从 FCP/LCP 等中位数重建 score。
- FCP、LCP、Speed Index、TBT：毫秒，越低越好。
- CLS：无量纲，越低越好。
- Benchmark Index：运行环境诊断分布，不参与 compare verdict。

对每个指标分别报告：样本数、median、MAD（median absolute deviation）、Tukey hinges IQR、min 与 max。Median 是主结果；MAD/IQR 与 warnings 描述波动，不能被省略。单次结果既不代表 central tendency，也无法判断不稳定性，因此不用于验收。

## Compare materiality

`delta = candidate median - baseline median`。工具以测量离散度保护结论：

- timing 指标的 materiality floor = `max(基线中位数的 10%, 2 × max(两侧 MAD))`；
- Performance Score floor = `max(5 分, 2 × max(两侧 MAD))`；
- CLS floor = `max(0.02, 2 × max(两侧 MAD))`。

超过 floor 才分类为 `improvement` 或 `regression`，否则是 `no_material_change`。高低方向按指标语义判断；同时存在改善与退步时 overall 为 `mixed`。这是同协议的 lab comparison，不应外推为真实用户体验或 field percentile。

## 不稳定性与报告

Warnings 会标记 LCP selector/final URL 改变、Lighthouse warning、attempt failure、浏览器清理失败，以及高 IQR。报告按以下顺序组织：

1. profile、完整 protocol fingerprint 与 `successfulRuns/requestedRuns`；
2. 官方 Performance Score 和各指标 median + MAD + IQR；
3. warnings 与范围；
4. compatible compare verdict，或明确写 `INCOMPATIBLE_PROTOCOL`/`INCONCLUSIVE`；
5. `Cannot Claim`：field/RUM/CrUX、真实用户 percentile、跨协议优劣。

合成目标示例使用 `https://example.test/page`；真实 target 仅作为命令输入，不写入 Skill 文档或版本控制中的测试 fixture。

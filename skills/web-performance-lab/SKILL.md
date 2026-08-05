---
name: web-performance-lab
description: Use when repeatable Lighthouse measurements are needed for a public web page, a performance claim must be checked across multiple runs, or two web builds need a protocol-compatible performance comparison.
---

# Web Performance Lab

用 Skill 目录内的 `scripts/webperf` 执行命令。它提供合成实验室测量；`desktop-observed-v1` 只是本机未模拟节流的 observed run，不是 field data、CrUX 或 RUM。

## 测量纪律

- 正式验收使用一次 `collect --runs 5` 生成的一个 bundle；禁止把单次分数当成结论，也不要把 5 次测量拆成 5 个 bundle。
- 比较前分别 `inspect` 两个 bundle。只比较状态为 `OK` 且 protocol fingerprint 完全一致的 bundle；`observed` 与 `lab` 之间不比较。
- 把 evidence 写入仓库外或已忽略的运行目录，绝不 `git add`。其中可能包含原始 Lighthouse 报告。
- 报告 median，并同时读取 MAD、IQR、范围和 warnings。Performance Score 使用 Lighthouse 每次给出的官方 score，不从指标中重算。

## 标准流程

1. 从调用者工作目录诊断并列出编译内置 profile：

```bash
<skill-dir>/scripts/webperf --json doctor
<skill-dir>/scripts/webperf --json profiles list
```

2. `doctor` 的 `NEEDS_SETUP` 是复合状态，不代表一定缺少 locked engine。先用当前环境的只读能力确认 Node.js 22.19+、npm 及可识别的 Chrome/Chromium；缺少 host prerequisite 时，先报告并在取得相应授权后修复，再重跑 `doctor`。

只有 host prerequisites 已就绪且 `doctor` 仍返回 `NEEDS_SETUP` 时，才说明 `engine setup` 会写用户缓存并通过联网 `npm ci` 安装 locked engine，取得明确授权后执行并再次运行 `doctor`：

```bash
<skill-dir>/scripts/webperf --json engine setup
<skill-dir>/scripts/webperf --json doctor
```

如果 setup 报告 existing target invalid，停止并报告；不要删除、覆盖或“修复”现有缓存。

3. 选择一个明确的 versioned profile，连续收集 5 次到尚不存在的目录：

```bash
<skill-dir>/scripts/webperf --json collect \
  --url https://example.test/page \
  --profile desktop-lab-v1 \
  --runs 5 \
  --out <untracked-evidence-dir>/candidate
```

4. 验证 evidence 后再解读：

```bash
<skill-dir>/scripts/webperf --json inspect --run <untracked-evidence-dir>/candidate
```

5. 仅对同一严格协议下、完整成功的 baseline/candidate 比较：

```bash
<skill-dir>/scripts/webperf --json compare \
  --baseline <untracked-evidence-dir>/baseline \
  --candidate <untracked-evidence-dir>/candidate
```

收到 `INCOMPATIBLE_PROTOCOL`、`PARTIAL` 或 `INCONCLUSIVE` 时停止比较并重新采集，不手工绕过。结论应区分实测结果、波动和无法声明的 field experience。

需要精确参数、JSON status 或 exit code 时读 [cli-contract.md](references/cli-contract.md)；选择 profile、解释 median/MAD/IQR 或判断 materiality 时读 [measurement-protocol.md](references/measurement-protocol.md)。

`raw lighthouse -- ...` 只供专家诊断：它没有 profile、聚合、证据脱敏或比较保证，不作为正式验收结果。

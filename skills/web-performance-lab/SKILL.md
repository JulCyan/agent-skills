---
name: web-performance-lab
description: Use when repeatable Lighthouse measurements are needed for a public web page, a performance claim must be checked across multiple runs, two web builds need a protocol-compatible comparison, or verified evidence should be rendered as an offline HTML report.
---

# Web Performance Lab

用 Skill 目录内的 `scripts/webperf` 执行命令。它提供合成实验室测量；`desktop-observed-v1` 只是本机未模拟节流的 observed run，不是 field data、CrUX 或 RUM。

## 测量纪律

- 正式验收使用一次 `collect --runs 5` 生成的一个 bundle；禁止把单次分数当成结论，也不要把 5 次测量拆成 5 个 bundle。
- 当用户只给 URL、未指定形态时，Skill 默认选择 `mobile-lab-v1`；明确要求 desktop 时选择 `desktop-lab-v1`；只有用户明确要求本机未模拟、unthrottled 或 observed 观察时才选择 `desktop-observed-v1`。CLI 本身仍要求显式传入 `--profile`。
- 比较前分别 `inspect` 两个 bundle。只比较状态为 `OK` 且 protocol fingerprint 完全一致的 bundle；`observed` 与 `lab` 之间不比较。
- 把 evidence 写入仓库外或已忽略的运行目录，绝不 `git add`。其中可能包含原始 Lighthouse 报告。
- 报告 median，并同时读取 MAD、IQR、范围和 warnings。Performance Score 使用 Lighthouse 每次给出的官方 score，不从指标中重算。

## HTML 产出策略

- 默认链路到终端/JSON 结果即结束，禁止自动生成 HTML。CLI 始终非交互，不在命令执行中询问用户。
- 用户在初始请求中明确要求 HTML、可分享报告或离线产物时，先完成 `collect` + `inspect`（比较任务还要完成 `compare`），再直接生成，不需要二次确认。
- 用户只要求测试、分析或比较时，先交付结论；链路完整后最多提示一次。`OK` 使用“已验证 evidence，可直接生成离线 HTML，无需重跑 Lighthouse。”；符合下一条的 `PARTIAL` 则明确提示“可生成 `DIAGNOSTIC ONLY` 离线 HTML，但不是验收报告”。只有用户同意后才生成。用户明确说只要数字、不要报告时，不生成也不提示。
- 状态为 `OK` 的 bundle 可生成 verified lab evidence report。`inspect` 顶层为 `INCONCLUSIVE`、但 `data.status=PARTIAL` 且 `data.aggregateAvailable=true` 的完整可验证 bundle，只能生成醒目标记为 `DIAGNOSTIC ONLY` 的单站诊断报告，不能称为验收报告；其它 `INCONCLUSIVE`、损坏、无法校验或没有 aggregate 的 evidence 拒绝生成。
- 单站默认一份 HTML；不含 query 参数的同一 requested URL，其多个不同 profile 可用重复 `--run` 合并为一份。由于 query value 在 evidence 中脱敏，含 query 的 target 不合并多个 profile。baseline/candidate 默认一份 comparison HTML。每次 Lighthouse 自带的原生 HTML 默认生成 0 份。
- HTML 是校验后 evidence 的确定性本地渲染，不调用模型写正文，也不重跑 Lighthouse。输出必须位于 bundle 外、使用新的 `.html` 路径；不覆盖文件或 symlink。
- 报告语言优先服从用户明确指定；未指定时，以用户当前报告请求的主要自然语言判断：中文传 `--locale zh-CN`，其它或无法判断时传 `--locale en`。用户明确要求其它 locale 时不要静默回退，先说明当前只支持 `en`/`zh-CN` 并让用户选择。CLI 为保证自动化确定性仍固定默认 `en`，不读取系统 locale。每份 HTML 只使用一种语言；需要中英文时，对同一 evidence 使用两个新输出路径分别生成，不重测。用户只选择语言，不提供模板或运行时翻译词条。

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

3. 按上述默认路由选择一个明确的 versioned profile，连续收集 5 次到尚不存在的目录：

```bash
<skill-dir>/scripts/webperf --json collect \
  --url https://example.test/page \
  --profile mobile-lab-v1 \
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

6. 仅在符合上面的 HTML 产出策略时，从已验证 evidence 生成派生报告：

```bash
<skill-dir>/scripts/webperf --json report \
  --run <untracked-evidence-dir>/candidate \
  --locale zh-CN \
  --out <untracked-report-dir>/candidate.html

<skill-dir>/scripts/webperf --json report compare \
  --baseline <untracked-evidence-dir>/baseline \
  --candidate <untracked-evidence-dir>/candidate \
  --locale zh-CN \
  --out <untracked-report-dir>/comparison.html
```

示例按本文中文对话规则使用 `zh-CN`；英文报告改为 `--locale en`。多个 profile 的无 query 同站点单报告重复传入 `--run`。报告是 standalone/offline HTML，内联样式、无 script、CDN、font、tracker 或其它网络请求，并包含分布、逐次样本、LCP 数值诊断、allowlisted quantified opportunities、protocol fingerprint 与本地化的声明边界；可分享报告不复制原始 LCP selector。指标缩写、profile、evidence status、report class 与 JSON machine fields 保持规范值。

比较遇到 `PARTIAL` 时拒绝；只有上面精确定义的 verified aggregate 分支可直接生成单站诊断报告且不重测。其它 `INCONCLUSIVE` 或损坏 evidence 拒绝报告。收到 `INCOMPATIBLE_PROTOCOL` 时说明不兼容字段并停止，不手工绕过；重新采集只是用户仍需比较时的后续选择，不自动启动十次新测量。报告命令顶层 `status=OK` 只表示文件写入成功，自动化还必须检查 `data.reportClass` 与 `data.evidenceStatus`。结论应区分实测结果、波动和无法声明的 field experience。

Unix 平台保证终止 executor-owned process group；其他平台的直接 Go executor 只终止
direct Lighthouse process，browser descendants 或临时状态可能残留。中断后应报告该
边界并检查本机状态，不能声称 process tree 已完整清理。

需要精确参数、JSON status 或 exit code 时读 [cli-contract.md](references/cli-contract.md)；选择 profile、解释 median/MAD/IQR 或判断 materiality 时读 [measurement-protocol.md](references/measurement-protocol.md)。

`raw lighthouse -- ...` 只供专家诊断：它没有 profile、聚合、证据脱敏或比较保证，不作为正式验收结果。

# Web Performance Lab Specification

## Purpose

为 AI agent 提供通用、可重复、可解释且可比较的网页性能实验能力。

## ADDED Requirements

### Requirement: 采集必须使用固定官方引擎
系统 SHALL 通过 Node 调用显式安装的固定版本 Lighthouse 引擎；Go 执行器不得重新
实现 Lighthouse audit、simulation 或 score，也不得在采集期间安装或更新引擎。

#### Scenario: 尚未安装固定引擎
- **WHEN** 用户在 locked Lighthouse engine 不可用时执行 `collect`
- **THEN** 系统返回结构化 `NEEDS_SETUP`，不调用未固定版本的 `npx` 或全局
  `lighthouse`

#### Scenario: 显式设置引擎
- **WHEN** 用户执行 `engine setup`
- **THEN** 系统只安装 manifest 声明的精确版本，并将安装结果写入版本化用户
  cache

#### Scenario: 既有 cache 不可信或 payload 被篡改
- **WHEN** cache hierarchy 含 symlink 或不安全类型、Unix cache 权限或 euid owner
  不安全，或 payload tree 与安装时 integrity marker 不一致
- **THEN** 系统不得执行或覆盖该 cache；诊断返回 `NEEDS_SETUP`，显式 setup 返回
  path-free `engine_target_invalid` 并要求停止处置

### Requirement: 性能结论必须来自重复串行采样
系统 SHALL 默认串行执行五次 Lighthouse 采集，且只有至少三次成功样本时才生成
aggregate 结论；系统不得并发采集、静默重试或以单次结果作为验收结论。

#### Scenario: 五次采集全部成功
- **WHEN** 用户对有效公共 URL 使用显式 profile 执行默认 `collect`
- **THEN** 系统保存五份 LHR，并生成官方 score 与核心指标的中位数和离散度

#### Scenario: 成功样本不足
- **WHEN** 五次 attempt 中少于三次成功
- **THEN** 系统保留所有 attempt evidence 并返回 incomplete 非零结果，不生成
  稳定性结论

### Requirement: 测量 Profile 必须显式且版本化
系统 SHALL 要求 `collect` 显式选择 `desktop-observed-v1`、`desktop-lab-v1` 或
`mobile-lab-v1`，并将 resolved flags 与环境写入不可含糊的 protocol fingerprint。

#### Scenario: 未选择 Profile
- **WHEN** 用户执行 `collect` 但未提供 `--profile`
- **THEN** 系统在启动 Chrome 前返回 `INVALID_INPUT` 并列出可用版本化 profile

#### Scenario: Observed 与 lab 结果混用
- **WHEN** 用户尝试比较 observed profile 与 simulated lab profile 的 bundle
- **THEN** 系统返回 `INCOMPATIBLE_PROTOCOL`，不输出 improvement 或 regression

### Requirement: Evidence bundle 必须完整可追溯
系统 SHALL 在调用方指定目录保存 manifest、protocol、逐次 LHR 和 summary；现有目录
不得被覆盖，失败 attempt、redirect、runtime version 与 artifact path 必须可追溯。

#### Scenario: 输出目录已存在
- **WHEN** 用户把已有文件或目录作为 `--out`
- **THEN** 系统在启动引擎前拒绝且不改写该路径

#### Scenario: 中途收到终止信号
- **WHEN** collection 收到 interrupt 或 terminate signal
- **THEN** Unix 平台系统终止自己创建的进程组；其他平台至少终止直接子进程且不
  对未解析 PID 发信号。系统保留可诊断 evidence，并将状态标记为 `INTERRUPTED`；
  非 Unix 平台不得声称 descendant cleanup 已完成

### Requirement: 汇总不得伪造 Lighthouse 分数
系统 SHALL 保留每次官方 performance score，并只汇总这些 score 的分布；系统不得
使用各指标中位数重新计算或声称一个新的 Lighthouse score。

#### Scenario: 各指标的代表样本不同
- **WHEN** LCP、TBT 和 CLS 的中位数分别来自不同 attempt
- **THEN** summary 分别报告指标中位数和官方 score 中位数，不合成新的官方
  报告

### Requirement: 比较必须先验证协议兼容性
系统 SHALL 只比较 schema、profile、Lighthouse、Chrome、Node、OS、architecture 与关键
runtime flags 一致的 bundle，并结合静态容差与组内波动判断 materiality。

#### Scenario: 协议一致且变化落在噪声带内
- **WHEN** candidate 变化未超过 metric materiality floor 与两倍组内 MAD 的较大值
- **THEN** 系统将该 metric 分类为 `no_material_change`

#### Scenario: 浏览器版本不同
- **WHEN** baseline 与 candidate 的 Chrome version 不一致
- **THEN** 系统返回 `INCOMPATIBLE_PROTOCOL` 并指出不兼容字段

### Requirement: URL 与 JSON 输出必须安全且可组合
系统 SHALL 只接受无 userinfo 的 HTTP/HTTPS URL；summary 与 JSON stdout 不得暴露
query value、fragment、credential 或个人绝对路径，且 `--json` stdout 必须只有一个
稳定 JSON document。

#### Scenario: URL 包含 userinfo
- **WHEN** 输入 URL 包含 username 或 password
- **THEN** 系统在任何网络访问前拒绝，错误信息不得回显 userinfo

#### Scenario: URL 包含 query token
- **WHEN** 输入或最终 URL 包含 query values
- **THEN** manifest、summary 与终端输出只保留参数名或脱敏形式，不输出原值

### Requirement: Skill 必须可发现并跨工作目录运行
仓库 SHALL 通过 Skills CLI 暴露 `web-performance-lab`；复制安装后的 Skill SHALL 从
自身目录解析 launcher、reference、Go source 与 fixture，并保留调用方 cwd。

#### Scenario: 从临时项目调用安装副本
- **WHEN** Skill 被复制到干净临时项目并从其它 cwd 运行 help、doctor 与 profile
  list
- **THEN** 命令不依赖 provider repository path 且输出与源目录调用一致

### Requirement: 公开仓库只包含合成测试内容
系统 SHALL 使用 `example.test` 与本地 deterministic server 作为 fixture；外部验收
URL、原始 LHR、运行 evidence、cookie、credential 与组织特定标识不得进入 tracked
files。

#### Scenario: 执行公开内容扫描
- **WHEN** CI 或维护者运行 public scanner
- **THEN** 新 Skill、fixture、Git index 与相关文档不包含运行 evidence 或禁止标识

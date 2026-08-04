## Purpose

为 AI agent 提供独立、确定性且可恢复的 Shopify Files 媒体同步能力。

## ADDED Requirements

### Requirement: 规划默认不写远端
系统 SHALL 从显式输入、资源目录与 store 配置生成固定 plan、desired state 与资源
identity，且规划过程的 Shopify mutation call count 必须为零。

#### Scenario: 从 CSV 生成计划
- **WHEN** 用户提供 CSV、资源目录、store scope 与输出目录执行 `plan`
- **THEN** 系统生成可审查计划及资源 SHA，并且不调用任何 Shopify mutation

### Requirement: 执行必须绑定已审查身份
系统 SHALL 让 `apply` 消费显式 plan path；默认只 preview，只有 `--execute` 且
plan、store scope、配置 identity 与 preview binding 一致时才允许远端写入。

#### Scenario: 缺少执行授权
- **WHEN** 用户对有效 plan 运行 `apply` 但未传入 `--execute`
- **THEN** 系统输出 preview binding，远端 mutation call count 为零

#### Scenario: Preview 后 identity 漂移
- **WHEN** execute 的 plan、store selection 或 stores config identity 与 binding 不一致
- **THEN** 系统在任何 mutation 前 fail closed，并要求重新 preview 与授权

#### Scenario: 绕过 apply 直接执行阶段命令
- **WHEN** 用户对 `upload`、`alt` 或 `video-copy` 传入 `--execute`
- **THEN** 系统在任何网络写入前拒绝，并指引使用 plan-bound `apply --execute`

### Requirement: 远端身份与本地输入必须冻结
系统 SHALL 只接受合法的 Shopify store handle，在请求前冻结并验证 stores config 与资源
bytes/SHA；dotenv 不得导出 Shopify 白名单以外的进程变量。

#### Scenario: Store handle 试图改变请求 origin
- **WHEN** `shopifyStore` 包含 URL、path、dot、port 或其它非 handle 字符
- **THEN** 系统在读取或发送凭据前拒绝，且携带 Shopify 凭据的请求不得跟随跨 origin redirect

#### Scenario: Preview 后资源被替换
- **WHEN** 上传时的资源 snapshot SHA 与 plan resource SHA 不一致
- **THEN** 系统在 staged upload HTTP 前拒绝，且不得采用变化后的 bytes

#### Scenario: Dotenv 包含传输层变量
- **WHEN** dotenv 除 Shopify auth/API version 外还包含 proxy、TLS 或其它进程变量
- **THEN** 系统忽略非白名单变量且保留已有进程环境

### Requirement: 确定性执行器拥有阶段状态
系统 SHALL 由同一 executor run 管理 upload、alt、translation、verify 的阶段顺序、
bounded concurrency、错误聚合、安全停止和 terminal summary。

#### Scenario: 部分行失败
- **WHEN** 同一 run 中至少一行失败而其它行已完成
- **THEN** 系统输出非零 exit、`PARTIAL_FAILURE`、完整错误集合与每行恢复状态

### Requirement: 冷启动恢复必须唯一且安全
系统 SHALL 从显式 plan path、attempt history、evidence 和 summary 恢复 execution
identity；不得依赖“最新文件”、mtime 或会话上下文选择恢复对象。

#### Scenario: 已接受 mutation 等待 readback
- **WHEN** evidence 已保存 provider object identity 但 readback 未完成
- **THEN** 重跑只继续 readback，不重复 mutation

#### Scenario: Transport outcome 含糊
- **WHEN** mutation transport outcome 无法确认且没有安全 readback
- **THEN** 系统进入 `NEEDS_ATTENTION` 并禁止自动重放 mutation

### Requirement: 只有成功终态返回成功
系统 SHALL 区分 `SUCCEEDED`、`PARTIAL_FAILURE`、`FAILED`、`NEEDS_ATTENTION`、
`WAITING_EXTERNAL` 与 `CANCELLED`；只有 `SUCCEEDED` 返回零 exit code。

#### Scenario: 等待外部 JSON 处理
- **WHEN** plan 包含 executor 不拥有的 template JSON replacement
- **THEN** 系统返回非零 exit 与 `WAITING_EXTERNAL`，并说明下一安全动作

### Requirement: 外部输入依赖必须显式
系统 SHALL 将本地输入视为内建能力；协作平台输入缺少外部 CLI 时必须返回安全设置
指引且不触发 Shopify 写入。

#### Scenario: 缺少可选协作平台 CLI
- **WHEN** 用户选择协作平台 Sheet 输入但对应外部 CLI 不可用
- **THEN** 系统在规划前返回 `NEEDS_SETUP`，本地输入能力仍保持可用

## Purpose

确保仓库中的 Skill 能被精确发现、复制安装、独立运行并安全维护。

## ADDED Requirements

### Requirement: 仓库只暴露明确发布的 Skills
系统 SHALL 让 `npx skills add <repo> --list` 只发现公开产品 Skill。

#### Scenario: 列出仓库 Skills
- **WHEN** 用户对仓库运行 `npx skills@1.5.21 add . --list`
- **THEN** 列表只包含 `shopify-media-sync`

### Requirement: 安装副本必须自包含
已安装 Skill SHALL 从自身目录解析脚本、reference 与 executor source，并从任意调用
工作目录接收用户相对路径。

#### Scenario: 从临时项目调用
- **WHEN** Skill 被复制到干净临时项目且调用 cwd 不在 Skill 目录
- **THEN** help、doctor、plan 与 inspect 不依赖 provider 工作目录

### Requirement: 未发布执行器必须 fail closed
launcher SHALL 只选择显式 binary、固定版本且 digest 匹配的 Release asset，或 bundled
Go source；没有可信运行时时不得下载 `latest` 或执行未知 binary。

#### Scenario: Release 尚未发布且本机没有 Go
- **WHEN** 用户首次调用 Skill 且没有可信 binary 或 Go
- **THEN** launcher 返回结构化 `NEEDS_SETUP`

### Requirement: Provider 与 consumer lockfile 所有权必须分离
provider SHALL 不因发布自身 Skill 而提交根 `skills-lock.json`；consumer SHALL 保存 Skills
CLI 生成的 lockfile，并可校验安装目录 hash。

#### Scenario: 安装副本发生漂移
- **WHEN** consumer 中已安装 Skill 的 computed hash 与 lockfile 不一致
- **THEN** verifier 返回 `DRIFT` 且不修改安装副本

### Requirement: 公开内容必须通过门禁
仓库 SHALL 检查 credential pattern、个人绝对路径、真实目标 ID、非许可 email domain、
本地 provider lockfile 与运行时提供的禁止标识；命中时必须非零退出且不打印值。

#### Scenario: 运行时禁止标识命中
- **WHEN** tracked file 包含 `AGENT_SKILLS_FORBIDDEN_TOKENS` 提供的任一 token
- **THEN** 扫描报告文件路径和通用规则名，不在输出或仓库中记录 token

### Requirement: API 版本必须显式可维护
系统 SHALL 验证 Shopify Admin API 版本格式并提供明确升级位置，不依赖退役版本的静默
fall-forward。

#### Scenario: 配置无效 API 版本
- **WHEN** 用户提供不符合 `YYYY-MM` 的 API 版本
- **THEN** 系统在请求前拒绝并报告配置错误

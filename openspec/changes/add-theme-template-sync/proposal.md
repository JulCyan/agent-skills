# Add Theme Template Sync

## Why

AI agent 需要一项可安装、可审计的 Shopify JSON template 同步能力，以便在明确的
source store/theme 与一个或多个 target store/theme 之间，同步完整 template、单个
Section，或受控字段 `sections.<sectionKey>.custom_css`。现有业务项目中的 Node.js
实现已经验证了主要同步语义，但依赖项目内店铺配置、主题生命周期与运行目录，不能作为
独立 Skill 安装。

## What Changes

- 新增可安装的 `theme-template-sync` 产品 Skill。
- 在 Skill 内提供自包含的 Go 确定性执行器、Shopify CLI adapter、fake adapter、
  合成 fixtures 与测试。
- 将旧 Node.js 实现中已验证的 JSONC 解析、Section 替换、order 定位、稳定 diff、
  removed gate、dry-run/execute 和跨店跨主题语义迁移为通用 template 能力。
- 新增字段级 `custom_css` allowlist，不开放任意 JSON Pointer 或任意字段写入。
- 新增多目标全量只读 preflight、串行写入、即时 canonical readback、运行 evidence 与
  明确的终态契约。
- 扩展仓库的 Skill discovery、copy-install、跨 cwd、Go、shell、公开内容和 OpenSpec
  验证，使其覆盖新增 Skill。

## Scope

- `templates/*.json`，包括 page、product、collection 与其它合法 Shopify JSON
  template 名称。
- 显式 store 与 theme reference；theme 可以通过确定的 ID 或精确名称解析。
- 完整 template、指定 Section、指定 Section 的 `custom_css` 三种同步粒度。
- Shopify CLI 作为首期 production adapter；认证由调用环境已有的 Shopify CLI 会话
  负责，Skill 不读取凭据文件。
- 默认 dry-run；显式 plan-bound execute 才允许写入未发布目标主题。

## Non-goals

- 修改或安装到任何消费项目，或继续补消费侧尚未接通远端 adapter 的 Go `sync-page`。
- 依赖旧业务来源项目才能运行，或迁移其中的店铺身份、theme ID、密钥、
  `.env`、公司配置与真实 evidence。
- 自动创建主题或 template、自动选择 Live theme、写入 Live theme、Publish，或默认
  扩散到全部店铺。
- 同步 Git 跟踪代码；Admin JSON 与 Git 代码仍是两条独立链路。
- 开放任意字段路径、任意 JSON Pointer 或任意远端 asset 写入。
- 在本 change 中执行真实 Shopify 写入、创建 Release、push、PR 或安装到消费项目。
- 将通用 `golang-*` 开发规范复制为仓库内 `.agents/skills`。

## Cannot Claim

本 change 通过后只能证明合成 fixture 下的本地行为、仓库验证与可复制安装。不能据此
声称真实 Shopify 凭据、店铺、主题、跨店写入、Preview、Publish、Live、Release 或
消费项目 rollout 已验证。

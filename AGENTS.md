# agent-skills 协作约束

本仓库是 JulCyan 个人维护的通用 Agent Skills 集合。规划、评审、交接与
commit message 默认使用中文；公开 README、API、代码标识和稳定技术术语可使用英文。

开始 change 前先读取 `openspec/config.yaml` 以及对应 change 的 proposal、specs、
design 和 tasks。OpenSpec 是唯一持久化规格与任务事实源；brainstorming、worktree、
TDD、review 和 completion verification 是执行纪律。

## Git 边界

- `main` 工作区只读；任何修改在 `codex/<feature-slug>` 隔离 worktree 完成。
- commit message 使用 `type(scope): 中文描述`。
- push、merge、rebase、reset、Release、package publish 和 Shopify 写入必须取得明确授权。
- 保留用户已有改动，不执行无关清理。

## Skill 边界

- 产品 Skill 只放在 `skills/<skill-name>/`。
- Skill 从自身目录解析 scripts、references 与 assets，不依赖宿主仓库路径。
- 远端 mutation 默认关闭；preview、execute authorization、identity、readback 和
  recovery 必须可区分。
- 不读取或提交 `.env`、token、cookie、凭据或真实运行 evidence。
- 公开内容只使用合成 store、domain、resource ID 和 fixture。
- 新增或修改 Skill 后运行 skill validation、跨 cwd、copy-install、敏感信息和相关
  executor tests。

## Evidence

本地测试、GitHub 安装、Release asset、consumer rollout、Shopify readback 和 Live
是不同证据层，不把一层成功升级为另一层成功。

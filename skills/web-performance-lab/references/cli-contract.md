# webperf CLI 与 JSON 合同

## 入口

从任意工作目录运行：

```bash
<skill-dir>/scripts/webperf [--json] <command>
```

相对 URL 以外的路径均按调用者 cwd 解析。`WEBPERF_BINARY` 是显式可信 binary 覆盖并优先执行；未设置时，launcher 将 Skill 自带 Go module 构建到唯一临时目录、运行后清理。缺少 Go 时 launcher 输出稳定 `NEEDS_SETUP` JSON 并以 2 退出；它不会输出本机绝对路径。

运行 wrapper 需要本机 Node.js；source fallback 需要符合 `go.mod` 要求的 Go 1.25.1+，且不会自动下载 toolchain。locked engine 另外需要 npm、Node.js 22.19+ 及可识别的 Chrome/Chromium。也可把 `WEBPERF_BINARY` 指向调用者已验证的可执行文件以跳过 source build。

## 命令

| 命令 | 合同 |
| --- | --- |
| `doctor` | 只读返回 composite readiness；`NEEDS_SETUP` 可能来自 locked engine、Node 或 Chrome，不能单独定位缺项。 |
| `profiles list` | 列出唯一可用的编译内置 versioned profiles。 |
| `engine status` | 只读返回 locked engine 状态。 |
| `engine setup` | 只安装 locked engine：写用户缓存并联网运行 `npm ci`；不安装 host prerequisites，执行前需要明确授权。 |
| `collect --url <http(s)> --profile <name> --runs 5 --out <new-dir>` | 顺序执行准确次数、无隐藏重试，创建一个 evidence bundle；最少 3 次，正式验收用 5 次。URL 不允许 userinfo。 |
| `inspect --run <bundle>` | 校验 manifest、protocol、hash、artifact ledger、原始 LHR 与重算聚合，再输出安全摘要。 |
| `compare --baseline <bundle> --candidate <bundle>` | 只比较完整 `OK` 且 protocol 完全一致的 bundle。性能退步仍是成功分析：读取 `data.overall`，不要只看 exit code。 |
| `raw lighthouse -- <official flags>` | 透传到锁定 Lighthouse/Chrome 的专家入口；不支持全局 `--json`，不提供正式测量保证。 |

全局 `--json` 必须放在 managed command 前，例如 `webperf --json inspect ...`。

## `NEEDS_SETUP` 分流

不要把 `doctor=NEEDS_SETUP` 直接映射为 `engine setup`。先用当前环境的只读能力确认 Node.js 22.19+、npm、Chrome/Chromium；缺项应单独报告，并在获得对应授权后修复，再重跑 `doctor`。只有 host prerequisites 已就绪且状态仍为 `NEEDS_SETUP` 时，才披露用户缓存写入与联网 `npm ci` 的副作用、取得明确授权、运行 `engine setup`，然后重跑 `doctor`。

如果 setup 返回 path-free `error.code=engine_target_invalid`，停止并报告。CLI 不授权
删除、覆盖、自动重试或修复该缓存；需要由用户另行决定处置。
Integrity schema 升级使用新的内部 cache generation；旧 generation 保持只读原状，
不会被自动迁移、覆盖或删除。

## JSON envelope

Managed commands 输出：

```json
{
  "schemaVersion": 1,
  "command": "inspect",
  "status": "OK",
  "data": {},
  "warnings": [],
  "error": {
    "code": "stable_machine_code",
    "message": "safe summary",
    "remediation": "next safe action"
  }
}
```

`data`、`warnings`、`error` 按结果省略。不要从 stderr 解析状态；stderr 只提供简短诊断。

## Status 与 exit code

| Status | Scope | 含义 | Exit |
| --- | --- | --- | ---: |
| `OK` | envelope / attempt | 命令、分析或 attempt 成功；compare verdict 仍在 `data.overall` | 0 |
| `INVALID_INPUT` | envelope | 命令、flag、profile、URL 或目录参数无效 | 2 |
| `NEEDS_SETUP` | envelope | locked engine、Node、Chrome 或安装条件未就绪 | 3；launcher 缺 Go 为 2 |
| `INTERRUPTED` | envelope / attempt | 收到取消或超时 | 130（attempt 无独立 exit） |
| `PARTIAL` | envelope / summary | 至少一个 collect attempt 未完整成功；不可比较 | 1 |
| `INCONCLUSIVE` | envelope | aggregate 缺失、bundle 不完整或证据无效 | 1 |
| `INCOMPATIBLE_PROTOCOL` | envelope | profile/runtime fingerprint 不一致 | 1 |
| `ENGINE_FAILED` | envelope / attempt | fatal collection failure，或单次 Lighthouse/browser attempt 失败 | 1（attempt 无独立 exit） |
| `PARSE_FAILED` | attempt only | 单次 LHR 无法按锁定 schema 解析；collect envelope 汇总为 `PARTIAL` | n/a |
| `NAVIGATION_FAILED` | reserved | schema v1 保留值，当前 executor 不产生该状态 | n/a |

## Evidence 与恢复

`collect` 的 `--out` 必须是新目录。bundle 以 `manifest.json` 为最终 commit point，包含 `protocol.json`、可验证的 `summary.json` 及 `samples/run-N.lhr.json`。每个 artifact 有 SHA-256；`inspect` 会重新读取并核对原始报告，不信任手工修改的 summary。

中断或失败后保留 evidence 供诊断，但不要续写、覆盖或手工修复旧 bundle。使用新目录重新 `collect`。URL query value 在安全摘要中会被替换为 `REDACTED`，原始 LHR 仍应视为敏感运行证据且不进入 Git。

Unix 平台会终止 executor-owned Lighthouse/Chrome process group。其他平台的 Go
executor 只保证终止直接 Lighthouse process，browser descendants 或临时浏览器状态
可能残留；报告该边界并在重新采集前检查本机状态，不得声称 process tree 已完整清理。

## 合成示例

```bash
webperf --json collect --url https://example.test/page \
  --profile desktop-observed-v1 --runs 5 --out /tmp/webperf-observed
webperf --json inspect --run /tmp/webperf-observed
```

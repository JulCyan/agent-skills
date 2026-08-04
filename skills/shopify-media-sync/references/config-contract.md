# 配置合同

通过 `--stores-config <path>` 显式提供 store 配置。未提供时，CLI 只从命令调用目录向上查找 `stores.config.json`。

```json
{
  "stores": [
    {
      "id": "store-us",
      "label": "US Store",
      "aliases": ["us"],
      "shopifyStore": "example-us",
      "apiVersion": "2026-01",
      "primaryLocale": "en",
      "enabled": true
    }
  ]
}
```

执行层不从公司、产品或店铺命名猜测 locale、alias、theme 或 template。非 `en` 主语言和短 alias 必须在配置中声明。

## Shopify Admin API 版本

API 版本解析优先级为：`SHOPIFY_API_VERSION_<STORE>`、`SHOPIFY_API_VERSION`、store 的 `apiVersion`、Skill 安全默认值。只接受 Shopify 季度版本 `YYYY-01`、`YYYY-04`、`YYYY-07` 或 `YYYY-10`；无效值必须在 HTTP 请求前失败。真实远端命令通过 `target_api_versions=<store>:<version>` 回显每个 store 的实际版本。

版本环境变量属于非敏感运行配置，不替代鉴权变量，也不从未经许可的 env 文件自动发现。

支持以下任一鉴权组合：

- `SHOPIFY_CLIENT_ID_<STORE>` + `SHOPIFY_CLIENT_SECRET_<STORE>`；
- `SHOPIFY_CLIENT_ID` + `SHOPIFY_CLIENT_SECRET`；
- `SHOPIFY_ADMIN_TOKEN_<STORE>`；
- `SHOPIFY_ADMIN_TOKEN`。

## 本地 env runtime

仓库根可提供 tracked `.env.example` 与 ignored `.env.local`。Skill 自有入口：

```bash
<skill-dir>/scripts/shopify-media-sync.sh env-init --root . --format json
<skill-dir>/scripts/shopify-media-sync.sh env-check --root . --format json
```

`env-init --source <approved-path>` 只复制上述 `SHOPIFY_CLIENT_*` / `SHOPIFY_ADMIN_TOKEN*` 鉴权变量，不复制 API 版本或其它 key，不输出值，创建 mode `0600`，已有 `.env.local` 时拒绝覆盖。

远端命令的鉴权优先级为：已有进程环境优先；其后为显式 `--env-file`；未显式指定时从命令调用目录向上查找 `.env.local`。env 文件不得覆盖已存在的进程变量；`--no-env-file` 禁止文件加载。`plan`、preview 和 `inspect` 不加载 env 文件。

`doctor` 按 `--stores` 所选 store 检查鉴权完整性。默认只检查进程环境；显式 `--env-file <path>` 时只读该文件，不导出变量、不输出值，也不验证远端 endpoint。缺少 config 时只能诊断全局来源。

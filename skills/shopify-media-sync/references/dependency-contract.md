# 依赖合同

## 基础入口

安装后的 Skill 包含 Node launcher、Go executor 源码和 POSIX 兼容入口。运行 `scripts/shopify-media-sync.sh` 需要 Node.js 20.19 或更高版本。

launcher 按以下顺序选择确定性 executor：

1. 显式 `SHOPIFY_MEDIA_SYNC_BINARY`，且文件可执行；
2. `runtime.json` 固定 tag/platform 所声明、SHA-256 匹配的缓存或 Release asset；
3. 本机存在 `go` 时，从 Skill 自带源码构建到私有 cache；
4. 否则返回 exit code `2` 和结构化 `NEEDS_SETUP`。

禁止把 `latest`、缺少 digest、未列入 manifest 的 platform 或下载校验失败的 asset 当作可信 executor。`runtime.json` 为 `unpublished` 时，无 Go 用户尚不能走 Release 路径；不得宣称已经达到非技术用户开箱即用。

## 内建输入

CSV、XLSX、JSON、zip 和本地目录由 executor 自身处理，不依赖协作平台 CLI。缺少可选 adapter 时仍可使用这些输入完成 plan。

## 可选输入 adapter

Sheet 和 Drive token 输入依赖已安装且已认证的 `lark-cli`。`doctor --format json` 通过 `capabilities.sheet_input` 报告可用性；选择 Sheet 输入但依赖缺失时，plan 在任何 Shopify 调用前返回 `NEEDS_SETUP` 指引。

不要自动安装、自动认证外部 CLI，也不要因为本地输入可用而推断 Sheet/Drive 可用。

## 服务依赖

Shopify Admin GraphQL API 只用于明确的远端读取、mutation 和 readback。`plan`、`inspect`、env 诊断及 launcher runtime 诊断不应发起 Shopify mutation。

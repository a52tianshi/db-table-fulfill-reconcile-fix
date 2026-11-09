# 贡献指南 / Contributing

从当前工作分支创建小范围变更，说明具体缺陷或文档读者的使用困难。不要混入 import 重排、批量重命名、依赖升级、LICENSE 或作者信息修改。

## 本地检查 / Local checks

仓库有两个独立 Go module。Go 1.22+，race 检查需要可用的 C 编译工具链。

```sh
./scripts/check.sh all
```

该命令运行两个 module 的 `go build ./...`、`go vet ./...`，根模块 `go test -race -count=1 ./...` 和 e2e 非数据库包的 race 测试，最后执行 lint。lint 明确仅为 `gofmt -l .` 与 `bash -n`，不包含 staticcheck / golangci-lint，不下载第三方工具、不自动改文件。CI 分步调用同一脚本。缓存只读的沙箱可设置 `GOCACHE=/tmp/codex-clarity-go-build`。

数据库验证使用隔离库；运行前阅读脚本的 DROP 范围：

```sh
mysql -u root -e "CREATE DATABASE IF NOT EXISTS reconcile_codex"
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3306)/reconcile_codex" DIST=uniform ROWS=2000 USERS=1000 WARMUP=2 GAP=3 ./e2e/run.sh

mysql -u root -e "CREATE DATABASE IF NOT EXISTS reconcile_it"
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3306)/reconcile_codex" ./scripts/check.sh integration
```

集成测试硬编码改用 `reconcile_it`，会重建测试表；同实例上须串行跑，不能通过 E2E_MYSQL_DSN 改为专属库。原测试连接不上会 Skip，发布脚本将 Skip 视作失败。缩小参数仅用于快速验证，全量脚本默认值未改；小规模 uniform 不覆盖 skew 的全部分页边界。

## 提交与评审 / Review

- 修复前写能暴露缺陷的数据面断言；保留所有现有测试。覆盖率不能替代独立逐行校验。关键路径与覆盖口径见 [docs/testing.md](docs/testing.md)。
- 任何 API / 语义变更必须保留 deprecated 包装或 adapter，旧调用可用；CHANGELOG 同步写迁移说明，新增前后数据面等价测试。不能兼容的变更不能混进补丁版本。
- 错误必须保留 `errors.Is` / `errors.As` 链；部分查询失败必须返回 error，不能把缺失 map 条目当作不存在；修复结果只能在提交确认后记账。
- 变更契约时同时更新 [接口约定](docs/interface-contracts.md)、[状态机](docs/state-machine.md) 与 README 的中英文镜像；区分已实现与设计中能力。
- 验证记录写具体命令、真实输出、退出码和环境限制；Skip、mock 与未执行必须单独标注。CI 的 MySQL service 不是要求贡献者在本机安装 Docker。

不在公共 issue / PR 中提交业务数据或凭据。安全问题参见 [SECURITY.md](SECURITY.md)。

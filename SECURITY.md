# 安全与数据完整性 / Security and data integrity

本库可能覆盖新表整行内容；权威源、不可变路由键和比较规则配置错误都可能导致业务数据损坏。部署边界与恢复方法见 [运维手册](docs/operations.md)。

## 报告问题 / Reporting

若发现可导致越权读取、错误跨表写入、数据损坏或凭据泄露的问题，优先使用仓库 GitHub 的 **Security → Report a vulnerability**（若已启用）。若该入口不可用，请先创建不含攻击细节与敏感数据的 issue，请维护者提供私密渠道；不要公开生产数据、DSN、密钥或可直接利用的细节。本仓库未承诺专用安全邮箱或固定响应时限。

私密报告请包含版本/tag、Go/MySQL/驱动版本、最小表结构、脱敏配置、最小复现、预期与实际数据、事务是否提交、影响范围及可能的缓解方法。以数据库唯一键替代真实用户标识，日志需要先脱敏。

## 部署边界 / Deployment boundary

- 生产账号最小权限：**老表必须 `SELECT` + `LOCK TABLES`**。复核事务对两侧都执行 `SELECT ... FOR UPDATE`，MySQL 8.0.22 起锁定读要求 `UPDATE` / `DELETE` / `LOCK TABLES` 三者之一，只授 `SELECT` 会在首个复核事务报 `1142` 并直接终止（Warn 模式同样复核，同样失败）。新表需要 `SELECT`、`INSERT`、`UPDATE`。

  ```sql
  -- 授权示例（生产账号，不含 DROP/ALTER/DELETE）
  GRANT SELECT, LOCK TABLES ON prod_db.orders_old TO 'reconciler'@'%';
  GRANT SELECT, INSERT, UPDATE ON prod_db.orders_new_00 TO 'reconciler'@'%';
  ```

  完整上线清单见 [运维手册](docs/operations.md)。
- 生产账号限制网络来源并按部署要求启用 TLS。测试 quickstart 的本机无密码 root 不用于生产。
- Schema、Router、表名清单与回调是受信任的应用代码，不接收终端用户任意表名或 SQL。参数值使用占位符不等于允许不受信任的路由配置。
- 引擎不会删 Extra，也不会修复错片；没有跨实例原子性、通知可靠交付或整轮回滚。不要把 `Consistent()` 单独作为授权切流的依据。
- `Finding` 包含整行数据，通知实现和日志应按业务数据分级控制访问、保留与脱敏。

## 维护范围 / Maintenance scope

当前基线 v0.1.0；尚未承诺同时维护多个历史版本。收到问题后先复现和评估影响，再在 CHANGELOG 记录修复及兼容影响。本次发布准备不构成安全审计或已通过灾备演练的声明。

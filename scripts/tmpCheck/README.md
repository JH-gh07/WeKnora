# Task002 一键验核器

`verify_task002.sh` 把 Task002/G1 拆成可重复执行的三层 Gate，并通过退出码阻止“未执行即 PASS”。

## 快速使用

只跑不依赖 Docker/provider 的确定性检查：

```bash
./scripts/tmpCheck/verify_task002.sh static
```

验证真实 PostgreSQL migration（创建并删除隔离的临时容器，不接触现有数据库）：

```bash
./scripts/tmpCheck/verify_task002.sh postgres --allow-docker
```

执行完整检查，包括真实 Evaluation、正常重启和 SIGKILL：

```bash
TASK002_API_KEY='仅通过环境变量提供' \
./scripts/tmpCheck/verify_task002.sh all \
  --allow-docker --allow-restart --start-stack
```

也可以使用 `TASK002_JWT`，或使用 `TASK002_EMAIL` 与 `TASK002_PASSWORD` 自动登录。脚本不会把凭据、
Token、完整 HTTP 响应或 Prompt 写入报告。

## Gate 边界

| 层级 | 自动验证内容 |
| --- | --- |
| `static` | T3–T5、repository/restart/tenant fixture、SQLite migration、Task002 定向 race、vet、server link、Evidence JSON/secret/Prompt/Markdown 扫描 |
| `postgres` | 隔离 PostgreSQL fresh migration、重复执行、27 列结构、down migration |
| `live` | 真实 POST、完成轮询、重启后同 task 查询、SIGKILL 后 reconciliation |

以下内容不能被脚本凭空证明：真实 provider 输出的确定性、导师对 Prompt Cache 的口径、多副本 lease
安全性。它们不属于 Task002 的无条件自动 PASS 项。

## 安全与退出码

- `live` 会产生模型调用并重启 Compose 的 `app` 服务，因此必须显式传入 `--allow-restart`。
- `postgres` 会创建隔离临时容器，因此必须显式传入 `--allow-docker`。
- 默认报告写入 `/tmp/task002-check-<timestamp>/`，不会自动覆盖正式 Evidence。
- `0`：全部请求的 Gate PASS。
- `1`：至少一个 Gate FAIL。
- `2`：没有 FAIL，但有 Gate 因环境或授权不足而 BLOCKED。

> 修改原因：Task002 同时包含确定性单测、双库 migration、真实 HTTP restart 和 Evidence 安全要求；
> 单一布尔脚本容易把 Docker/provider 未执行误报成 PASS，因此采用 PASS/FAIL/BLOCKED 三态并保留逐项报告。

# Task004 一键验核器

`verify_task004.sh` 自动执行两层验核：

```bash
./scripts/tmpCheck/task004/verify_task004.sh static
./scripts/tmpCheck/task004/verify_task004.sh live --allow-docker --start-local
./scripts/tmpCheck/task004/verify_task004.sh all --allow-docker --start-local
```

- `static`：合同、repository、migration、handler、前端状态推导、类型、i18n 与 build。
- `live`：启动隔离的本地后端和 Vite，注册一次性测试身份，注入带唯一前缀的事实，执行 API/RBAC/tenant/browser 矩阵，最后清理事实并软删除测试身份/空间。
- 默认报告写入 `/tmp/task004-check-*`；设置 `TASK004_REPORT_DIR` 可指定目录。

例如将最终证据写入项目 Evidence：

```bash
TASK004_REPORT_DIR=../status/evidence/task004/task004-local-check \
  ./scripts/tmpCheck/task004/verify_task004.sh all --allow-docker --start-local
```

脚本不会打印 JWT、API key 或随机密码。仅允许连接项目的本地 Compose 网络；不调用外部 Provider。

> 修改原因（2026-08-25）：Task004 的无条件 Gate 同时依赖合同测试与真实浏览器闭环，手工执行容易遗漏状态或把 NOT_RUN 误记为 PASS，因此用退出码和逐项 Evidence 固化执行。

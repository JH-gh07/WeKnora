# Requirement Matrix — WeKnora 开源实战课题三

> 状态机：`NOT_STARTED → IN_PROGRESS → CODED → VERIFIED → ACCEPTED`
> 基线：`main @ 9b4f792a`（Gate 0 Baseline Fidelity，见 status/evidence/task001/）
> Task002/003 implementation：`feature/evaluation-runtime-metering-freeze @ c089ab07`，annotated tag `gate/g1-g2a-implementation-20260825`
>
> Priority 语义：`P0-A` 是承重事实核心，`P0-B` 是必须在 Release 前关闭、但只能在具名前置输出稳定后启动的官方验收扩展；`P1/P2` 才是可延期能力。`P0-B` 不是降级，也不授权与 P0-A 无依赖地并行铺开。

> **修改原因（2026-08-25）：** 原单一 `P0` 无法表达“必须交付，但需等待承重契约稳定”的能力；直接把 R6–R9 降为 P1 又会制造官方验收缺口。双层 P0 保留 Release 义务，同时把 UI、缓存、实验和复现限制在具名依赖及最小实现形态内。

| ID | 官方要求 | 工程问题 | 实现 | 验证 | 最终证据 | Priority | Owner | Status | Code Location | Open Risk |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| R1 | 可重复执行 | 如何冻结运行条件并区分代码身份 | EvaluationProtocol + RunProvenance | 同 Protocol 不同 Commit | config + provenance + result | P0-A | 本人 | CODED | internal/application/service/evaluation_protocol.go | evaluator/metric artifact 仍 UNVERSIONED |
| R2 | 结果持久化 | Run 生命周期如何保存 | EvaluationRun DB | kill/restart | DB evidence | P0-A | 本人 | VERIFIED（实现范围内） | internal/types/evaluation_run.go + internal/application/repository/evaluation_run.go + migrations/versioned/000090 + migrations/sqlite/000013 | 中断 run 无精确 partial aggregate（首版不承诺） |
| R3 | 四类结果（质量/成本/耗时/用量） | 数据如何统一关联 | Metric/Usage | Run 查询 | report | P0-A | 本人 | IN_PROGRESS（G2A 计量后端已 VERIFIED；四类结果统一呈现与 comparison 尚未完成） | internal/application/service | 指标口径漂移 |
| R4 | CI 门禁 | 如何判断 regression | RegressionPolicy + Comparator | 故意降 recall | failed CI | P0-A | 本人 | IN_PROGRESS（local deterministic core = VERIFIED_LOCAL：三轮复审已关闭 trusted evaluator/candidate SUT 分离、AST/import allowlist、metric allowlist/presence/null/范围 fail-closed、dataset/baseline integrity、path-independent SUT identity、pull_request-only workflow、NUL-safe no-renames changed-file classifier 18-case executable matrix、L1–L16、20-run determinism 与受控 regression BLOCK；required-check context 已冻结为 `evaluation-regression / quality`，外部收尾工具/Runbook READY；GitHub required-check 与真实 PR BLOCK 仍 BLOCKED_EXTERNAL_CONFIGURATION） | internal/application/service/evaluation_regression*.go + cmd/evaluation-regression + tests/evaluation/* + .github/workflows/evaluation-regression.yml + scripts/tmpCheck/task007/* | 2026-08-28 公开审计：上游 Rulesets 可读但无精确 required check、workflow API 404；当前无有效 GitHub 管理员认证，真实 workflow run/PR 未运行；G5 NO-GO / Task008 execution NO；中期反馈未到（2026-09-04） |
| R5 | 调用与成本统计 | 如何统一采集并保持计价可解释 | ModelCall + HistoricalPricingIdentity | 数据库核对 | SQL/API | P0-A | 本人 | VERIFIED_WITH_BOUNDARY（G2A：unknown-cost/DB 已验证；G2B 已验证 currency-safe aggregate，混币/缺币 fail closed） | internal/models/* | SDK 隐藏 retry；生产仍未写 EstimatedCost/Currency/Pricing*，历史估算费用生成路径待后续能力补齐 |
| R6 | 模型管理页 | 如何按模型和时间聚合 + 租户隔离 | Usage API + ModelSettings UI | 筛选与权限测试 | UI/demo | P0-B | 本人 | VERIFIED（G2B GO：两项 contract change + B01–B15 真实 API/RBAC/tenant/browser 矩阵全 PASS） | frontend/src/views/settings | G3 后 Local Embedding Cache 以独立 `local_embedding_cache` 呈现（DISABLED/AVAILABLE/UNKNOWN），不复用 Provider cache 指标冒充 local hit rate |
| R7 | Embedding Cache | Key/失效/并发/租户隔离 | EmbeddingCache | cold/warm/model-change | before/after | P0-B | 本人 | VERIFIED（G3 GO：最终收口审计已闭合，static 8/8、SQLite 4/4、PostgreSQL 4/4；PG parity 13/13；retrieval non-regression 三轮全 true；Warm 零 Provider Call） | internal/models/embedding | query 原文不持久化但 canonical digest 参与 endpoint identity；Measurement COMPLETE 限定为 persisted-observation boundary；不扩展 TTL/LRU/GC/通用 Cache 平台 |
| R8 | Prompt Cache | 现有实现是否提高 cached-token ratio | 现状审计 + A/B Harness | 同文档同模型实验 | reporting coverage + ratio | P0-B | 本人 | VERIFIED_WITH_BOUNDARY（G4 GO：结构 Level C 证明 Current 前缀 ≈2.55× Legacy、modify fingerprint 同 source cardinality=1；adapter reporting classification=UNSUPPORTED、provider native=UNVERIFIED 故真实 cached ratio 不可观测，N/A 而非 0；NO PRODUCTION CHANGE） | internal/models/chat | provider 不可报告时允许诚实 INCONCLUSIVE，不建设新 Prompt Cache 平台 |
| R9 | 干净环境复现 | 避免依赖本机隐式状态 | Reproduction command | fresh clone | reproduction log | P0-B | 本人 | NOT_STARTED | tests/evaluation | 外部 provider 仅作官方复现，Blocking CI 必须 deterministic |

## 本 Gate（G0）已完成的事实核对

- [x] 服务端 Evaluation 请求与 Go SDK 请求字段不一致（`knowledge_base_id` vs `embedding_id`）
- [x] 服务端返回结构与 SDK 响应结构不一致（status int vs string；metric 结构不符）
- [x] `dataset_id` 不对应稳定内容（`GetDatasetByID` 硬编码 `./dataset/samples`）
- [x] 并行 QA goroutine 存在共享 `err` 数据竞争（`evaluation.go` L412）
- [x] 临时 KB 崩溃后残留且无 locator 可定位（name 固定 `"evaluation"`）
- [x] Evaluation 由单实例执行（single-worker assumption）
- [x] Chat/Embedding/Rerank 无统一 ModelCall 计量层；retry 在 service 层（Wiki）可见
- [x] Prompt Cache 稳定前缀仅覆盖 Wiki modify；生成类 prompt 无前缀
- [x] 当前 provider（siliconflow）不可可靠报告 cache usage

> 详细证据见 `status/evidence/task001/`（不在本仓库内，避免提交运行时证据）。

## 本 Gate（G1 Reproducible Run）已完成的事实核对

- [x] `EvaluationRun` 最小持久化闭环：PostgreSQL(000090) + SQLite(000013) 两套 migration，租户隔离 repository（create/find/update/list）。
- [x] Protocol/Provenance 分离：`protocol_hash` 不含 git commit；Prompt 只存 `{sha256,length}` 摘要；`measurement_contract_status=UNVERSIONED`。
- [x] 生命周期状态机 `PENDING→RUNNING→COMPLETED/FAILED/INTERRUPTED`；`PROCESS_LOST/SERVER_RESTART/OWNER_EXPIRED` 为 `interruption_reason`。
- [x] 临时资源 locator：`temporary_resource_key`（run_id 派生）写入 KB Description；`TemporaryKnowledgeBaseFinder` 按 `tenant + is_temporary + description` 发现；`cleanup_status` 可查。
- [x] 启动 reconciliation：single-worker 下将遗留 RUNNING 标为 INTERRUPTED 并清理临时资源（`bootstrap.go` dig.Invoke）。
- [x] 已知共享 `err` data race 已修复，`EvalDataset` 并行路径在 `-race` 下通过（`evaluation_concurrency_test.go`）。
- [x] 评审 P1 修复（三轮）：reconciliation 候选谓词覆盖所有终态×未完成 cleanup（`status IN (RUNNING,PENDING) OR cleanup_status IN (CREATING,CREATED,FAILED) OR persistence_status=PERSIST_FAILED`），删除失败保留 `temporary_kb_id`；关键持久化 fail-closed + `persistence_status`（`Update` 校验 `RowsAffected==0`）；错误只落 `error_type`+allowlist 安全消息；`cleanup_status` 拆分 `DELETE_REQUESTED`；finder transient 错误分类（不误判为「KB 不存在」）；默认日志不写原始 provider 错误（全链路）；`PERSIST_FAILED` 收敛（`persistRun` 复位 PERSISTED + 软删除窗口按「逻辑删除已完成」处理）（见 p1_review_fixes.md）。
- [x] R3/R5 仍未实现：ModelCall 计量、四类结果统一关联、HistoricalPricingIdentity 留待后续 Task（本 Gate 不实现）。
- [x] 原实现身份下 PostgreSQL 实机 migration/restart 验证：000080-000085 实机 up（ON_ERROR_STOP=1）+ 重复 up + down 均通过（ParadeDB pg17）；重放到 2026-08-28 Fork main 后为避免上游编号冲突，等价 SQL 顺延为 PostgreSQL 000090 / SQLite 000013，必须由本次 bootstrap CI 重新验证；live HTTP T1/T2（含 `docker compose kill app` 中断重启 reconciliation）PASS=7 FAIL=0。详见 `status/evidence/task002/migration_postgres.log`、`t2_interrupted_run.md`。

> 详细证据见 `status/evidence/task002/`。

## 本 Gate（G2A Usage Backend）已完成的事实核对

- [x] ModelCall typed contract：一逻辑调用一行（Chat.Chat/ChatStream、Embedder.BatchEmbed+pool、Reranker.Rerank），NULL 语义与 0 严格区分（`internal/types/model_call.go`）。
- [x] 双库 migration：PostgreSQL `000091` + SQLite `000014`（model_calls + model_metering_health），tenant/time/run/model 索引。
- [x] 三类工厂最外层 metering decorator（Decision 003-1 方案 B），`config.Recorder==nil` 时行为不变；初始化页连接测试注入 recorder 并以 purpose=connection_test 归因（真实 provider 调用计入用量）。
- [x] 计量写入与业务分离 + durable 生命周期：`BeginModelCall` 先写 persisted=false 标记；`RecordModelCall` 同事务写 call row + finalize persisted=true；崩溃/取消/DB 故障留下 persisted=false → 窗口 PARTIAL，绝不伪报 COMPLETE；事务失败 fail-closed；关闭写入 detachContext。
- [x] 历史计价身份冻结：`pricing_version/source/effective_at` 随调用落库；未知价格 NULL + `unknown_cost_call_count`，不进 known total。
- [x] Evaluation 调用带 run/task 归属：后台 goroutine `WithLLMCallScope(runID,taskID,runID)` + purpose=evaluation；run/task/trace key 加入跨 detach 白名单。
- [x] 查询 API：`GET /api/v1/model-usage`（run_id/model_id/operation/purpose/from/to）+ `GET /api/v1/model-usage/health`；tenant 只取鉴权 context。
- [x] 确定性测试：fixture 测试全绿（NULL/unknown、租户隔离、历史价格不可变、health 降级、pool 单行、stream 单行/放弃恰好一次、provider 失败含 chat 非流式+embedding、计量失败分离、durable 生命周期、handler 校验），`-race` 通过。
- [x] 原实现身份下 live 验证完成：PG 实机 migration（85→86）+ scratch 库 repeat/down/up 矩阵 + 重启聚合一致（restart_query.md）；重放到 2026-08-28 Fork main 后等价 SQL 顺延为 90→91，必须由 bootstrap CI 重新验证；受控采样发现并修复 purpose 缺口（8 处 setter，重采样验证）；租户隔离双向验证；OFF/ON overhead（persist median 0.82ms/p95 1.63ms，全路径 ORM 上界 ~2.5–2.7ms，同步 durable write 维持，不声明严格 <0.1%）；失败注入（业务 200 + PARTIAL 降级 + 恢复）。见 task003_review.md。

> 详细证据见 `status/evidence/task003/`。

# Task010 Browser Matrix B01-B18 — 使用指南

## 概述

浏览器测试矩阵验证 Evaluation Run Report UI 的真实渲染、状态转换和用户交互。

**两种验证方案：**
- **快速验证** (5-10 分钟) - 使用 agent-browser，立即验证核心功能 ⭐
- **完整测试** (37 分钟) - 使用 Playwright，获得完整证据

---

## 方案 1: 快速验证（推荐 ⭐）

如果你需要**立即验证核心功能**，无需等待完整部署：

```bash
# 1. 设置环境变量
export WEKNORA_BASE_URL="http://localhost:80"
export TEST_RUN_ID="your-completed-run-uuid"

# 2. 运行快速验证（5-10 分钟）
./quick_validate_agentbrowser.sh
```

**优势：**
- ✅ 5-10 分钟完成
- ✅ 无需 Playwright 安装（31 分钟）
- ✅ 验证 B01, B03, B07, B13, B17（核心功能）
- ✅ 生成截图证据
- ✅ 可立即冻结 rc2

**查看详细指南：** `QUICK_VALIDATE_GUIDE.md`

---

## 方案 2: 完整测试（标准流程）

如果需要**完整的浏览器测试证据**（覆盖全部 18 个测试点）：

## 前置条件

### 1. 环境准备

```bash
# 安装 Node.js (如果尚未安装)
# macOS: brew install node
# Ubuntu: apt-get install nodejs npm

# 安装 Playwright
npm install -g playwright

# 安装 Chromium 浏览器驱动
npx playwright install chromium
```

### 2. 应用部署

浏览器测试需要完整的 WeKnora 应用栈（后端 + 前端 + 数据库）正在运行。

#### 选项 A：使用 Docker Compose（推荐）

```bash
# 1. 确保 .env 文件已配置
cp .env.example .env
# 编辑 .env，设置必要的环境变量

# 2. 启动完整栈
./scripts/start_all.sh

# 3. 验证服务运行
curl http://localhost:80/health  # 前端
curl http://localhost:8080/health  # 后端
```

#### 选项 B：本地开发模式

```bash
# 1. 启动基础设施（数据库等）
./scripts/dev.sh

# 2. 启动后端
go run cmd/server/main.go

# 3. 启动前端（新终端）
cd frontend
npm install
npm run dev
```

### 3. 准备测试数据

#### 获取认证 Token

浏览器测试需要有效的认证 token。获取方式：

**方法 1：从浏览器开发者工具提取**
1. 在浏览器中登录 WeKnora (`http://localhost:80`)
2. 打开开发者工具 (F12)
3. 进入 Application/Storage → Local Storage 或 Cookies
4. 查找 `auth_token` 或 `Authorization` 值

**方法 2：通过 API 登录**
```bash
# 如果有用户名密码登录 API
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"password"}' \
  | jq -r '.data.token'
```

**方法 3：使用 API Key（如果启用）**
```bash
# 从配置或管理界面获取 API Key
export WEKNORA_AUTH_TOKEN="<your-auth-token>"
```

#### 准备测试 Run

浏览器测试需要至少一个已完成的 Evaluation run。

**创建测试 Run：**
```bash
# 通过 API 触发一次 Evaluation
curl -X POST http://localhost:8080/api/v1/evaluation \
  -H "Authorization: Bearer $WEKNORA_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "knowledge_base_id": "kb-test-001",
    "dataset_id": "ds-test-001",
    "question_count": 5
  }' | jq -r '.data.run_id'

# 保存返回的 run_id，例如：
# a1b2c3d4-e5f6-7890-abcd-ef1234567890
```

或使用前端界面手动创建一次 Evaluation 并记录其 `run_id`。

## 运行测试

### 快速开始

```bash
export WEKNORA_BASE_URL="http://localhost:80"
export WEKNORA_AUTH_TOKEN="<your-auth-token>"
export TEST_RUN_ID="a1b2c3d4-e5f6-7890-abcd-ef1234567890"

# 运行浏览器测试
./scripts/tmpCheck/task010/browser_matrix/run_browser_matrix.sh
```

### 通过 verifier 运行（完整验证）

```bash
export WEKNORA_BASE_URL="http://localhost:80"
export WEKNORA_AUTH_TOKEN="<your-auth-token>"
export TEST_RUN_ID="a1b2c3d4-e5f6-7890-abcd-ef1234567890"

# 运行完整验证（包括浏览器测试）
./scripts/tmpCheck/task010/verify_task010.sh full --allow-browser
```

### 调试模式（显示浏览器）

```bash
export HEADLESS=false  # 显示浏览器窗口
./scripts/tmpCheck/task010/browser_matrix/run_browser_matrix.sh
```

## 测试覆盖

### 快速验证覆盖（方案 1）

使用 `agent-browser` 验证的核心功能：

- ✅ **B01**: 页面可访问性 - 页面能正常加载
- ✅ **B03**: 四类结果同屏显示 - 检索质量、答案质量、成本、耗时
- ⚠️ **B07**: UNKNOWN cost 显示（部分验证）
- ✅ **B13**: 刷新数据保持 - 刷新后内容不丢失
- ⚠️ **B17**: 可访问性标签（文本验证）

**覆盖率：** 5/18 (28%) - 但这 5 个是最核心的功能点

### 完整测试覆盖（方案 2）

使用 `Playwright` 验证的全部测试点：

- **B01**: 直接打开有效 URL，验证 header 显示短 run_id 和状态
- **B02**: loading 状态，验证不闪现零值
- **B03**: completed 状态，验证四类区域同屏可访问
- **B07**: all cost unknown，验证显示 UNKNOWN 而非 ¥0/$0
- **B13**: browser hard refresh，验证同一 run 恢复
- **B17**: keyboard/screen reader，验证状态非颜色唯一表达
- **B18**: mobile width，验证关键字段可读、无水平溢出

### 需要手动验证或特定运行状态

以下测试需要特定的运行状态或多 run 场景，标记为 SKIP，需要在实际环境中手动验证：

- **B04**: running 状态 + NOT_FINAL + 自动刷新
- **B05**: failed 状态 + 安全文案
- **B06**: interrupted 状态 + interruption 可见
- **B08**: partial cost（已知小计 + 未知调用数）
- **B09**: true zero cost（0 + currency + AVAILABLE）
- **B10**: mixed currency（不显示混币总额）
- **B11**: measurement partial/unknown + health warning
- **B12**: cache unsupported/disabled vs 0% miss 区分
- **B14**: run A→B 快速切换，验证无 stale overwrite
- **B15**: Viewer 角色权限
- **B16**: cross tenant 不泄露

## 结果查看

### 快速验证结果（方案 1）

```bash
# 查看验证报告
cat browser_evidence_quick/validation_report.md

# 查看截图
open browser_evidence_quick/screenshots/
```

结果文件：
```
browser_evidence_quick/
├── validation_report.md       # 验证报告
├── page_snapshot.txt          # 页面元素快照
├── page_text.txt              # 页面文本内容
└── screenshots/               # 截图目录
    └── *.png                  # 各个截图
```

### 完整测试结果（方案 2）

测试完成后，证据保存在：

```
status/evidence/task010/browser_evidence/
├── browser_test_summary.json    # 完整测试结果 JSON
├── browser_matrix.tsv           # TSV 格式报告
├── B01_direct_url_access.png    # 截图证据
├── B02_loading_state.png
├── B03_four_sections.png
└── ...
```

### 查看摘要

```bash
# JSON 格式
cat status/evidence/task010/browser_evidence/browser_test_summary.json | jq '.summary'

# TSV 格式（适合 Excel/表格工具）
cat status/evidence/task010/browser_evidence/browser_matrix.tsv
```

### 查看截图

```bash
# macOS
open status/evidence/task010/browser_evidence/B03_four_sections.png

# Linux
xdg-open status/evidence/task010/browser_evidence/B03_four_sections.png
```

## 故障排除

### 快速验证（方案 1）

查看 `QUICK_VALIDATE_GUIDE.md` 的故障排查部分，包括：
- agent-browser 未找到
- 页面加载超时
- 元素未找到
- 截图失败

### 完整测试（方案 2）

#### 问题 1：Playwright not installed

```bash
npm install -g playwright
npx playwright install chromium
```

### 问题 2：TEST_RUN_ID not set

确保你有一个有效的 run_id：
```bash
# 通过 API 查询最近的 run
curl -H "Authorization: Bearer $WEKNORA_AUTH_TOKEN" \
  http://localhost:8080/api/v1/evaluation/runs \
  | jq -r '.data[0].run_id'
```

### 问题 3：认证失败（401/403）

- 检查 token 是否有效
- 检查 token 格式（Bearer 前缀）
- 尝试重新登录获取新 token

### 问题 4：页面加载超时

- 检查前端服务是否运行：`curl http://localhost:80`
- 检查后端服务是否运行：`curl http://localhost:8080/health`
- 检查网络代理设置

### 问题 5：Chrome binary not found

```bash
# macOS: 确保 Chrome 已安装
ls "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

# Linux: 安装 chromium
apt-get install chromium-browser
# 或
yum install chromium
```

## 最佳实践

### 推荐工作流

1. **快速验证先行**（方案 1）
   - 开发完成后立即运行 `quick_validate_agentbrowser.sh`
   - 5 分钟验证核心功能
   - 通过后可先冻结 rc2

2. **完整测试补充**（方案 2）
   - 环境就绪后运行完整 Playwright 测试
   - 获得全部 18 个测试点的证据
   - 补充到最终文档

### 通用最佳实践

1. **使用 Docker Compose**：最简单、最可靠的部署方式
2. **保留测试数据**：至少保留一个稳定的 completed run 用于回归测试
3. **定期运行**：在重大 UI 变更后重新运行浏览器测试
4. **查看截图**：失败时先查看截图，了解实际渲染状态
5. **清理旧证据**：定期清理 `browser_evidence/` 避免占用空间

---

## 相关文档

- `QUICKSTART.md` - Playwright 快速开始指南
- `QUICK_VALIDATE_GUIDE.md` - agent-browser 快速验证指南 ⭐
- `ARCHITECTURE.md` - 系统架构设计（如果存在）
- `TESTING_PROTOCOL.md` - 测试协议详解（如果存在）
- `../../TODO_REMAINING_WORK.md` - Task010 剩余工作清单

---

**更新时间**: 2026-09-05 22:15 UTC+8  
**维护者**: Task010 Team


## 手动验证补充

对于 SKIP 的测试用例（B04-B06, B08-B12, B14-B16），请按以下步骤手动验证：

1. **B04 (running)**: 启动一个长时间运行的 Evaluation，立即打开 Run Detail 页面
2. **B05 (failed)**: 人为触发一个失败的 Evaluation（如无效参数）
3. **B14 (rapid switch)**: 在两个 run_id 之间快速切换浏览器标签
4. **B16 (cross tenant)**: 使用 tenant A 的 token 尝试访问 tenant B 的 run_id

## 下一步

完成浏览器测试后：
1. 确认所有 P0 测试 PASS（无 FAIL）
2. 更新 `authoritative_run.json` decision 为 `AC_R3_GO`
3. 冻结 rc2 身份
4. 请求 jhhh 独立复现并签字

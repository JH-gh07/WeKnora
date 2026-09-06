# Task010 Browser Tests - Quick Start

## 30 秒快速开始

```bash
# 1. 进入测试目录
cd scripts/tmpCheck/task010/browser_matrix

# 2. 安装依赖（仅首次）
npm install
npm run install-browsers

# 3. 检查环境
npm run check

# 4. 设置环境变量
export WEKNORA_BASE_URL=http://localhost:80
export WEKNORA_AUTH_TOKEN="<your-auth-token>"
export TEST_RUN_ID=completed-run-uuid

# 5. 运行测试
npm test
# 或
./run_browser_matrix.sh
```

## 获取 AUTH_TOKEN

### 方法 1: 从浏览器（最简单）
1. 打开 http://localhost:80 并登录
2. F12 打开开发者工具
3. Application → Local Storage 或 Cookies
4. 找到 `auth_token` 或 `token` 字段，复制值

### 方法 2: API 登录
```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"your-password"}' \
  | jq -r '.data.token')

echo $TOKEN
```

## 获取 TEST_RUN_ID

### 选项 A: 从已有 run
```bash
# 列出最近的 runs
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/api/v1/evaluation/runs \
  | jq -r '.data[0].run_id'
```

### 选项 B: 创建新 run
```bash
RUN_ID=$(curl -s -X POST http://localhost:8080/api/v1/evaluation \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "knowledge_base_id": "kb-test-001",
    "dataset_id": "ds-test-001",
    "question_count": 5
  }' | jq -r '.data.run_id')

echo $RUN_ID
```

## 查看结果

```bash
# TSV 报告
cat ../../../../status/evidence/task010/browser_evidence/browser_matrix.tsv

# JSON 摘要
cat ../../../../status/evidence/task010/browser_evidence/browser_test_summary.json | jq '.summary'

# 截图
ls -lh ../../../../status/evidence/task010/browser_evidence/*.png
```

## 常见问题

### Q: Playwright not found
```bash
npm install -g playwright
npx playwright install chromium
```

### Q: Chrome not found
**macOS**: 安装 Google Chrome 应用  
**Linux**: `apt-get install chromium-browser`

### Q: 认证失败 (401)
- 检查 token 是否有效
- 尝试重新登录获取新 token
- 确认 token 格式（某些 API 需要 `Bearer ` 前缀）

### Q: run_id not found (404)
- 确认 run 已完成（status = completed）
- 检查 tenant 匹配（token 对应的 tenant 要有权限访问该 run）

## 进阶用法

### 显示浏览器窗口（调试）
```bash
export HEADLESS=false
npm test
```

### 指定不同环境
```bash
export WEKNORA_BASE_URL=https://test.example.com
npm test
```

### 只运行环境检查
```bash
npm run check
```

## 更多信息

- 完整文档: `README.md`
- 实现细节: `../../../../status/evidence/task010/browser_matrix_implementation.md`
- 验收标准: `../../../../status/evidence/task010/task010_final_status.md`

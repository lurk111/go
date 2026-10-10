# bid_mon · 业务监测与诊断

二期提供配置驱动的独立 Go 监测服务：用只读 SQL 接入 MySQL/PostgreSQL，通过 CEL 判断实体或聚合异常，再通过受限 SSH Helper、诊断规则和可选 LLM 给出原因与人工处理建议。借鉴 Akmatori 的技术与设计，不依赖其服务或源码运行。

业务模式只需要 **Go 服务容器 + PostgreSQL**。React 页面嵌入 Go 二进制；宿主无需 Go、Node、Python。监测源、SQL、字段、触发/恢复条件、确认次数和 Webhook 均由配置管理。

当前以二期核心版本 `2.0.0-dev` 收口，后续生产能力作为三期规划，不在当前版本继续扩展。支持业务记录与 SQL 聚合监测；目前通用采集是有预算的完整快照，尚未实现通用增量分页。已完成的能力与未验证的范围分别记录在验收文档中。

第一次了解项目，按这个顺序阅读：

1. [项目导读与源码阅读路线](docs/project-guide.md)：业务问题、架构、数据模型、完整事件流程与关键设计。
2. [业务接入与 SSH 配置](docs/phase-2-onboarding.md)：如何配置 SQL、字段、规则、日志、诊断与渠道。
3. [部署、升级和备份说明](docs/deployment.md)：如何构建、启动和保留实例数据。
4. [二期验收与剩余范围](docs/phase-2-validation.md)：哪些经过验证，哪些留到后续。
5. [GitHub 源码上传说明](docs/github-publishing.md)：源码提交范围与上传步骤。

在一个新的源码目录首次启动：

```powershell
.\start.ps1 -Build -Mode business -Instance my-monitor -Port 18080
```

Linux/macOS 脚本入口为 `sh start.sh --build --mode business --instance my-monitor --port 18080`；当前实际宿主验收为 Windows Docker Desktop Linux/amd64，其他宿主和架构仍需实测。

业务模式首次启动没有检查，可在管理页面接入数据源。想先体验模拟业务，可在**另一个独立目录**执行 `start.ps1 -Build -Mode demo -Instance my-monitor-demo -Port 18081`。不同实例使用独立目录和端口；管理员凭据由初始化生成，保存在各安装目录的 `deploy/phase2/config/credentials.json`。勿共享或提交该文件。

给其他使用者分发时使用白名单部署包及预构建镜像：解压 `artifacts/bid-mon-phase2-deploy.zip`，运行 `docker load -i bid-mon-runtime-amd64.tar`，再运行包内 `start.ps1`。系统 PostgreSQL 镜像由 Docker 自动拉取；运行镜像当前未公开发布，不存在可直接拉取的公共地址。

- [部署、升级和备份说明](docs/deployment.md)
- [二期业务接入与 SSH 配置](docs/phase-2-onboarding.md)
- [二期验收与剩余范围](docs/phase-2-validation.md)
- [订单回调示例](examples/order-callback/README.md)

自动清理保持关闭，保留策略只做预览。模型默认关闭；通用检查需同时配置模型渠道、`diagnosis_mode: hybrid` 和 `allow_model: true` 才发送证据。使用者应自行确定允许发送的字段及数据范围；本项目既有模型验收只覆盖模拟脱敏数据。

下面保留首期售前示例的使用与历史验证说明。二期演示中的售前适配器仍可与订单检查一起运行。

## 首期售前示例

独立 Go 系统，解决售前招标解析任务“异常难以及时发现、原因需要手工查日志、处理过程缺少记录”的问题。借鉴 Akmatori 的 Go/GORM/PostgreSQL、React/TypeScript 和模块组织，运行时不依赖 Akmatori。

**自动发现异常 → 受限取证 → 规则诊断与可选 AI 证据分析 → 本地通知 → 页面人工关闭 → 跟踪业务恢复。**

## 打开使用

- **[售前任务观察台](http://127.0.0.1:18080)**：异常列表、诊断与证据、填写原因关闭、通知记录、运行状态。
- [模拟任务接口](http://127.0.0.1:18081/docs)：通过 POST /scenarios 创建合成任务，系统自动发现，无需登记任务 ID。
- [本地通知接收器](http://127.0.0.1:18082/docs)：实验通知，不代表飞书。

页面使用本机 `deploy/.env` 中的 `MON_ADMIN_TOKEN` 登录。凭据不应提交或发送到聊天。会话为 HttpOnly/SameSite Cookie，8 小时有效，浏览器不在 localStorage 保存凭据。管理入口只发布在本机 127.0.0.1。

## 启动与停止

在项目根目录执行，使用 Docker Desktop 的 Linux 容器引擎：

```powershell
# 初始化缺失文件；已有配置与密钥保持原样。
python scripts/init_lab.py
docker compose --env-file deploy/.env -f deploy/compose.go.yml up -d --build
docker compose --env-file deploy/.env -f deploy/compose.go.yml ps
```

```powershell
docker compose --env-file deploy/.env -f deploy/compose.go.yml stop
docker compose --env-file deploy/.env -f deploy/compose.go.yml up -d
```

停止保留数据。默认每 15 秒扫描，排队阈值 180 秒，停滞阈值 900 秒，结果宽限 30 秒；失败任务立即建立事件，其余候选通常需要两次观察。可在 deploy/.env 设置 MON_POLL_SECONDS、MON_QUEUE_SECONDS、MON_STALE_SECONDS、MON_RESULT_GRACE_SECONDS、MON_DIAGNOSIS_TIMEOUT_SECONDS。

当前核心为 Go API、扫描器、事件、取证客户端与 Helper、规则诊断、通知派发。Python 仅用于实验模拟业务、模拟 Worker、本地通知接收器及验收工具；React 构建后嵌入 Go 二进制，不需要单独运行前端或 Nginx。

## 功能与判断边界

- 监测 failed、排队超时、运行停滞、结果缺失、全空结果候选、结果结构错误和业务数据源故障。
- 结果关联 task_id + user_id；错误 JSON、结构缺失和重复结果不会被当作空结果。部分目标有内容不触发整体空结果候选。
- SSH 只允许指定任务和时间窗口，最多 200 行/64 KiB；固定主机密钥、签名绑定、forced-command、禁止端口转发，日志内容脱敏。
- 规则诊断识别实验供应商的明确额度不足、限流、超时、提取错误；心跳过期只支持“Worker 可能不可用”。未知供应商及证据不足不作确认结论。
- 诊断总预算默认 120 秒，包含排队，两个并发执行槽，每个作业最多 8 次工具调用，正常为快照加 4 次 SSH。证据失败不会阻塞初始异常通知。
- 人工关闭原因必填，记录为 closed_unresolved，不表示恢复。持续异常不重开；确认解除后再次发生才形成新 episode。迟到诊断可归档，不追加诊断通知。
- 扫描、事件、诊断、通知均保存状态；通知按有限预算重试，失败状态可见，不承诺网络严格一次送达。
- 可接入 OpenAI Responses 兼容中转站，真实模型补充证据分析和人工处理建议；规则分类、确定性和业务恢复状态不由模型改写。未接入飞书、通用 Agent 或 MCP Gateway。

## 模型配置

首期示例从本实例私密文件 `deploy/model.local.json` 读取模型配置。源码仓库不包含模型凭据，每位使用者独立配置；运行不需要 Akmatori 服务。

配置结构如下（示例不含真实密钥）：

```json
{"provider":"openai","model":"your-model","base_url":"https://your-relay.example/v1","api_key":"your-key"}
```

地址保留中转站实际 API 前缀，客户端在末尾补 `/responses`；不会自动猜测或切换供应商地址。仅允许 HTTPS，拒绝重定向。文件已被 Git 与 Docker 构建上下文忽略，通过只读挂载注入运行容器。首次初始化会创建空配置文件，默认不开启模型。

启用：在 `deploy/.env` 设置 `MON_MODEL_CONFIG_FILE=/run/secrets/model.json`，重新创建 monitor 容器。禁用：将该值设为空，再执行相同命令：

```powershell
docker compose --env-file deploy/.env -f deploy/compose.go.yml up -d --no-deps monitor
```

每个新异常最多请求模型一次；仅发送脱敏的任务快照、合成证据和规则报告，单次最多 60 秒、2400 输出 token，同时受诊断总预算约束。当前授权及验收仅覆盖模拟数据，不能据此直接接入真实客户文档。模型失败、超时、响应格式错误或证据引用不合法时保存规则报告并显示回退原因，不影响初始通知。规则检查建议与 AI 建议分开呈现，AI 文本仍需人工核对。

已有诊断报告不因启用模型自动重跑。创建新的模拟异常即可查看新报告，历史报告继续保留。

## 架构与目录

| 路径 | 用途 |
| --- | --- |
| cmd/bid-mon | 独立 Go 服务入口 |
| cmd/evidence-helper | Go 受限取证 Helper |
| internal/monitor | 扫描、规则、GORM 数据存取、事件、诊断、Outbox、HTTP 管理 |
| internal/monitor/migrations | 版本化 PostgreSQL SQL 迁移，兼容既有实验表 |
| internal/evidence | 取证签名、边界校验、固定路径读取、脱敏 |
| web | React + TypeScript + Vite + Tailwind 页面 |
| webui | 页面构建产物的 Go embed 入口 |
| deploy/compose.go.yml | 当前独立部署，不含 Akmatori 服务 |
| scripts | 初始化与自动验收 |
| src/presales_monitor | 仅保留模拟业务、Worker、本地通知、鉴权及模拟数据契约 |

独立 Go module 为 bidmon。数据库采用 PostgreSQL/GORM，业务 MySQL 通过 database/sql 只读访问。指纹、规则和状态机保留既有契约；没有复制 Akmatori 全部业务模型。来源与借鉴边界见 [技术借鉴说明](docs/akmatori-reference.md)。

## 验证

```powershell
docker compose --env-file deploy/.env -f deploy/compose.go.yml build go-tests
python scripts/run_go_tests.py
uv --cache-dir .uv-cache sync --locked
uv --cache-dir .uv-cache run python scripts/smoke_go.py
npm --prefix web ci
node scripts/browser_test.mjs
# 真实中转站验收会产生少量模型调用费用，仅使用合成任务。
uv --cache-dir .uv-cache run python scripts/accept_go_model.py --mode model
# 6 分钟实验规模持续投放：每 30 秒一个异常、两个正常/取消任务。
uv --cache-dir .uv-cache run python scripts/accept_go_model.py --mode soak
```

Go 测试使用专用 test-mysql 和实际 PostgreSQL/SSH；每组 PostgreSQL 测试建立独立 schema，保留供检查。run_go_tests.py 自动启动并在结束后停止测试 MySQL，避免测试种子任务进入主监测器并触发付费模型调用。端到端与持续投放脚本使用共享模拟业务库，页面会看到这些合成告警。端到端脚本短暂停止并恢复 SSH/Worker，重启监测器；不要和其他依赖同一 SSH/Worker 的测试同时运行。

浏览器测试默认使用 Windows 本机 Edge 的隔离无头会话，可设置 BROWSER_EXE 指向 Chrome/Chromium。凭据仅在本机读取，不写入测试记录。截图及结果保存在被 Git 忽略的 artifacts 下。

Docker 构建固定 Go/Node/Python/数据库基础镜像摘要，依赖通过 go.sum 和 web/package-lock.json 锁定；OpenSSH 软件包来自镜像对应发行版仓库，尚未固定 apt 仓库快照。证据 Helper 使用 Linux 文件打开限制，Go 全量测试以 Linux Docker 为基准。

## 代码清理与资料

当前只保留独立 Go 系统、React 页面、必要的 Python 模拟服务及验收工具。旧 Python 监测器、Akmatori 接入副本与旧部署链路已退役并清除；数据库、事件、密钥与 Docker 卷保持原样。清理清单和源码备份位置见 [清理记录](docs/code-cleanup.md)。

目标见 [独立 Go 计划](docs/go-independent-plan.md)，验证见 [Go 交付记录](docs/go-validation.md)，简历用项目描述见 [项目简历](docs/project-resume.md)。

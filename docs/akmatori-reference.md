# Akmatori 技术借鉴与独立实现边界

参考：本地 E:\亚信\项目\akmatori，核查提交 6eb1beffb608f7562d6c3e81842db98ee93a328b。该工作区有既存未提交修改，本项目未修改参考目录。

| 上游参考位置/能力 | bid_mon 的落地方式 |
| --- | --- |
| go.mod：Go、GORM、PostgreSQL 驱动 | 根目录独立 module bidmon，采用同类后端技术；不依赖 github.com/akmatori/akmatori 模块 |
| cmd/akmatori：net/http 服务与并发后台服务 | cmd/bid-mon 单个服务，分开执行扫描、诊断、通知，context 控制停止与预算 |
| handlers/services/database 分层 | internal/monitor 按采集、规则、事件、诊断、通知和管理接口分文件组织，按业务范围控制模块数量 |
| 规范化告警和指纹计算思路 | 使用来源、环境、任务、规则和 episode 定义业务事件；维持旧实验指纹兼容，不直接沿用上游主机告警身份 |
| 事件、报告及审计组织 | 自己的一套 Event/Job/Evidence/Audit，没有双重 incident 绑定 |
| 工具接口、诊断上下文与运行手册 | Go 受限 SSH 客户端、固定 Helper、报告证据引用；业务规则判断是否恢复 |
| web/package.json：React、TypeScript、Vite、Tailwind | 独立编写事件中心、详情、通知、运行状态页面；不复制 Akmatori 控制台 |
| Compose 部署方式 | 独立服务名、凭据及卷；沿用本项目实验数据，退出 Akmatori 接入服务 |
| agent-worker/src/oneshot-llm.ts：单次模型调用、超时、文本输出；agent-runner.ts：OpenAI Responses 协议选择 | Go 标准 HTTP 独立实现一次 Responses 调用、输入脱敏、响应/引用校验及规则回退；不引入 pi-ai 或 Agent Worker |

2026-09-28 用户授权复用当前活跃模型配置：只复制地址、模型名及 API Key 到本项目私密文件，用户另行确认允许将合成任务脱敏证据发送至原中转站。此后运行完全使用本项目配置，不从 Akmatori 数据库动态获取凭据，未修改原项目。

当前 Go 核心与页面为本项目独立实现，没有直接复制上游 handlers、Agent 或数据库业务模型。取证签名与 forced-command、事务 Outbox、业务状态机和增量扫描沿用本项目已验证的契约与规则；不能把这些新增实现说成上游原生能力。

早期实验复制过的上游文件已于 2026-09-29 按用户要求清除，原 Apache-2.0 LICENSE、固定提交和哈希清单随旧源码保存在 artifacts/legacy-source-before-cleanup-20260929.zip。当前源码树不包含该副本，运行和编译均不依赖 Akmatori。

不采用的上游范围：完整 Agent Worker/MCP Gateway、Slack、多供应商通用告警入口、跨事件 LLM 合并、完整管理控制台和整套数据库模型。未来只有明确业务需要才增加，不按上游功能清单扩张。

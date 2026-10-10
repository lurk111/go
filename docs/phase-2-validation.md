# 二期当前交付与验收

截至 2026-10-10，二期已形成可运行的通用监测核心及本机 Docker 部署交付。它能脱离 Akmatori 和售前业务运行；原售前适配器可选启用。完整计划中的公开镜像、多平台实测、通用增量分页及备份恢复演练仍未完成，不把本轮核心闭环标成整个发布计划全部完成。

## 已实现

| 能力 | 当前行为 |
| --- | --- |
| 独立 Go 核心 | Gin、sqlx、MySQL/pgx、GORM/PostgreSQL、CEL、Go SSH、Prometheus；React/TS/Vite 页面嵌入 Go |
| 配置接入 | 严格 JSON/YAML、来源/检查/字段/规则、SQL与字节预算、实体与聚合、预览与决策、检查启停 |
| 状态机 | 独立触发/恢复、可配置连续次数、unknown/消失/失败打断计数、修改定义重计、状态持久化 |
| 多来源调度 | 全局四并发，来源交错和轮换，查询独立超时；没有无限数据量或固定延迟保证 |
| 取证 | 多目标/profile、受限路径、JSONL/文本/心跳、尾部与轮转、签名/主机校验；可选只读详情SQL |
| 诊断 | 可配置错误规则、手册、证据编号、hybrid模型原因假设；模型失败回退、不能改规则事实或恢复业务 |
| 渠道 | 独立目标Outbox、Webhook/本地接收器、2xx/code=0确认、稳定幂等键、有限重试与测试消息 |
| 管理 | Cookie登录、加密凭据、配置版本/回退、连接检测、SQL/证据预览、异常查看、填写原因关闭 |
| 简易部署 | 业务/演示分离、Go初始化、Windows/POSIX脚本、白名单ZIP、预构建amd64镜像导出 |
| 运维边界 | /metrics与逐检查健康；保留策略只COUNT预览，按用户要求不执行自动删除 |

初始文件用于空安装引导；系统库中激活配置在重启时优先。配置导出只含凭据引用，系统库中的凭据使用 AES-GCM 加密；实例 master.key 和 .env 不进分享包。

## 最终验证

环境：Windows + Docker Desktop Linux/amd64，Docker 29.1.3，Compose 2.40.3；Go 1.25、MySQL 8、PostgreSQL 16。实际测试使用独立系统库、业务库和 UUID schema/对象，未修改 Akmatori 或首期运行实例。

| 验证 | 结果 | 证据 |
| --- | --- | --- |
| Go单元、真实数据库/SSH及竞态 | 78个顶层、含子测试185项通过，0失败、0跳过测试 | artifacts/phase2-go-tests-final.jsonl、phase2-go-summary.json |
| 首期数据库升级 | 001 → 004重复执行保持事件ID、指纹、episode、人工关闭状态/原因、证据与revision | TestIntegrationUpgradePreservesLegacyIncident |
| PostgreSQL真实只读业务 | 实体/聚合、零值、字段错、行/字节/时间预算、只读事务和SELECT-only账号 | TestIntegrationPostgresReadOnlyChecks及8子测试 |
| 配置与凭据 | 非法配置不激活；源/映射修改重计；nonce轮换、错误key/跨ID密文拒绝；重启能解密 | TestGeneralIntegrationHTTP* |
| 双业务Docker闭环 | 14项通过，原售前与订单实体/聚合；真实SSH、通知、手动关闭、恢复与复发 | artifacts/phase2-smoke-final.json |
| 页面 | 11项通过，登录/空配置/校验保存/非法配置/启停/预览/移动宽度/退出，无JS错误 | artifacts/phase2-browser.json、phase2-config.png |
| 简易部署 | 干净ZIP目录只用Docker启动Go+PG；10001身份；重复启动配置凭据保持 | artifacts/phase2-deployment.json |
| 模型边界 | 本地HTTP模拟验证契约、超时、坏JSON、伪造引用、权限升级拒绝、裸凭据脱敏与大整数精度 | TestModelAdapter、TestGenericModel*、TestModelInputRedactsBareCredentialAndPreservesNumbers |

本轮通用业务闭环没有向外部模型发送数据。首期曾获用户授权的真实中转站验收是历史结果，不用它冒充二期真实模型验收。

### 固定负载实验

独立 MySQL 只读来源、一个实体检查、200个对象、触发2次/恢复2次，共800次实际SQL观察。四次扫描通过测试主动推进资格，没有等待线上调度间隔；通知和模型关闭。异常两次扫描总计3667ms，恢复两次总计4315ms，200个事件全部建立并恢复，没有重复或遗漏。

这些数字包含本机Go状态持久化，来自一次容器实验；不代表端到端通知耗时、生产容量、不同机器的SLA或更大结果集性能。超限丢弃整批与故障渠道隔离另由集成测试验证。

## 分享和运行

个人完整备份包含运行凭据，不能用于发布。分享使用：

1. artifacts/bid-mon-phase2-deploy.zip：公开白名单部署文件、示例和说明，每项SHA256校验。
2. artifacts/bid-mon-runtime-amd64.tar：仅Go/前端预构建业务镜像，不含容器卷和运行凭据。

解压ZIP，在其目录执行 `docker load -i <镜像tar路径>`，然后 `start.ps1 -Instance my-monitor -Port 18080`（POSIX用start.sh）。系统PostgreSQL镜像需可访问Docker Hub；这个交付不是全离线包。演示还需独立模拟服务/SSH镜像，可从源码构建或后续发布。

本机18090为独立空业务实例；18091为双业务演示。各自使用各安装目录生成的管理员凭据。部署包只有配置，没有源码，不能在部署包中使用-Build。

## 尚未完成的计划项

- 镜像仓库尚未指定或公开发布；GHCR构建工作流已准备。不能承诺其他人现在可以直接pull一个公共bid_mon镜像。
- Linux/arm64、macOS与Linux宿主脚本尚未实启；当前本轮可交付的镜像tar是amd64。工作流的多架构声明不能作为验收。
- 通用SQL当前是受限完整快照，没有通用增量分页游标或晚到更新回补。首期专用游标保留；超过快照预算需缩小查询或继续开发分页。
- 已验证首期数据库升级，尚未执行跨版本pg_dump/pg_restore恢复演练；部署文档中的步骤不等同于实测恢复成功。
- 二期未知错误的真实LLM与真实企业Webhook渠道未做实际送达验收；目前模型接口/回退用本地模拟验证、通知用本地接收器验证。
- 首次接入由连接表单、模板与JSON/YAML编辑区完成；日志主机/profile需配置挂载文件，未提供完整远端自动安装或全表单向导。

自动清理不是未获许可的待执行工作：用户已明确选择关闭，只提供预览。自动修复、分布式Agent、完整权限体系、更多数据库和飞书均保持原定非首批范围。

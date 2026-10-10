# 无关代码清理记录

日期：2026-09-29。按用户要求清除已经退役、与当前独立 Go 运行链路无关的代码。

## 已清理

- 旧 Python 监测器、诊断编排、事件/通知实现和 Akmatori 转发接入模块。
- components 下的 Akmatori 实验副本、旧 Python/Akmatori 测试与 Alembic 迁移、alembic.ini。
- 旧 compose.lab.yml、Python SSH Helper 及对应 Dockerfile/sshd_config。
- 旧实验初始化、上游副本校验与 Python/Akmatori 验收脚本。
- 被当前 Go 计划、验证记录和 README 取代的阶段文档。
- 移除 Alembic、Paramiko、Psycopg、旧配置及旧测试依赖，同步更新 uv.lock 与 requirements.lock。

上述源码和文档合计 57 个文件已在删除前归档并检查压缩包完整性。备份为 artifacts/legacy-source-before-cleanup-20260929.zip，明细为 artifacts/cleanup-manifest.json；备份不包含 deploy/.env、模型密钥或业务数据库数据，artifacts 不进入版本控制或 Docker 构建上下文。

## 保留及调整

- 保留 cmd、internal、web、webui 中的 Go 核心、取证 Helper、数据库迁移、React 页面和对应 Go 测试。
- Python 仅保留模拟业务、Worker、本地通知接收器及鉴权；共用的模拟字段、时钟和任务 ID 校验迁至 lab_contract.py。
- 当前验收脚本的 HTTP 共用函数迁至 scripts/lab_client.py，不再导入旧验收脚本。
- 模拟服务 Dockerfile 默认启动模拟服务，移除对已删除迁移、测试及旧监测入口的引用；初始化脚本固定使用 Go Helper。
- 保留当前设计文档、计划、运行手册、验收记录及本轮新增项目简历。原始设计文档作为业务来源保留，不等同于当前实现的全部功能。
- .venv、.uv-cache、web/node_modules 是本地工具依赖，并非旧业务实现；不为清理源码而删除它们。

本次不删除数据库记录、Docker 数据卷、运行服务或现有密钥。源码清理后不再提供旧 Compose 的直接回退入口；如需历史排查，可从压缩包提取到独立目录，避免与当前服务同时写入监测库。

## 清理后验证

- 重新锁定依赖并离线同步成功，移除 16 个不再需要的直接或间接依赖。
- 模拟服务、通知服务及验收共用模块导入成功；Python 编译检查和 Ruff 静态检查通过。
- 精简后的模拟服务镜像构建成功，模拟业务、Worker、通知接收器及证据初始化服务已使用新镜像。
- 重新执行 scripts/smoke_go.py，28 项业务回归全部通过，记录保存在 artifacts/go-smoke.json；覆盖异常发现、诊断通知、空结果复核、人工关闭、重启、恢复跟踪及 SSH/Worker 故障，测试后已恢复服务。
- 本轮未修改 Go 与前端业务实现；简历中的 79 个 Go 测试通过项、11 项浏览器检查及 36 个合成任务实验引用已有验收记录，不表示本轮重新执行了这些检查。

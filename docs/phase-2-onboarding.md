# 二期业务接入

## 1. 新安装与凭据

业务模式在新目录只启动 Go 和系统 PostgreSQL，可以先登录再配置业务。每个安装目录维护一份 .env、master.key 与管理员凭据；同一电脑多实例需使用不同目录、实例名与端口。

已初始化目录再次执行启动不会改写既有凭据、运行模式和端口。需要演示时在另一个新目录选 `-Mode demo`，或使用本项目已初始化的 18091 演示实例。不得把个人全量备份当作发布包。

## 2. 添加只读连接

进入“监测配置”，选择 MySQL/PostgreSQL，填写来源 ID 与连接字符串。页面加密保存凭据，配置只留下 `{"file":"stored:source-main-db"}` 引用；GET 配置、版本历史和导出不返回凭据。master.key 丢失将无法解密，备份系统库时一并备份它。

MySQL 连接示意：

```text
reader:password@tcp(host.docker.internal:3306)/business?parseTime=true&loc=UTC&timeout=5s&readTimeout=5s
```

PostgreSQL 连接示意：

```text
postgres://reader:password@database-host:5432/business?sslmode=require
```

Docker Desktop 中访问宿主地址用 host.docker.internal；远端用实际可达主机。容器里的 localhost 是容器自身。连接成功不代表查询授权和映射正确，还需试运行。

必须由业务库维护者授予只读账号所需表的 SELECT 权限。系统的 SELECT 校验与只读事务是第二层限制，不能替代数据库授权。

## 3. 配置检查

添加业务检查模板，或复制 examples/order-callback 下对应数据库的 YAML。校验按钮严格验证结构、引用与 CEL，转换为 JSON；试运行不保存配置、不累计确认次数、不建事件和通知，返回样本、字段质量及规则决策。

实体检查用 identity 中的稳定字段确定对象；聚合检查可以省略 identity，此时一个检查只接受一条 aggregate 记录。例如 COUNT 返回零代表显式观察到零；完全没有结果不代表恢复。

CEL 使用 `fields`、`previous`、`now`、`entity_key`。例如：

```text
trigger: fields.status == 'failed' && fields.retries >= 3
recover: fields.status == 'delivered'
```

触发和恢复独立表达，允许确认次数大于 3。两者都真、都假、缺字段或类型不匹配，结果为 unknown，打断连续确认，不会恢复事件。previous 来自上次已脱敏并按配置恢复类型的快照；首次无 previous，试运行也无历史 previous。不要用业务敏感文本变化承担关键规则判断。

默认每次查询完整的受限快照。可用 `:window_start`、`:window_end` 绑定过去 24 小时时窗，也可不用时窗查询当前未完成对象。超出时间/行数/字节预算会拒绝整批，健康页显示失败。`after` 参数当前为空，二期尚未提供通用增量分页游标；不要把 SQL LIMIT 截断误当成已经扫描了全量对象。

来源预算默认 5 秒、1000 行、1 MiB；上限 60 秒、10000 行、8 MiB。最多四个检查并行，按来源交错且轮换首个来源。用索引和明确过滤缩小查询，观察运行状态中的耗时和结果行数。整体扫描仍有有限预算，不能据此承诺任意大库无延迟。

`detail_query` 可选，只在异常诊断时查询补充记录，绑定 `:entity_key` 或映射字段名（例如 `:id`）。详情 SQL 和来源引用随事件冻结；SQL 与映射仍须符合只读限制、预算。凭据本身轮换后仍使用其引用的新值。

保存并启用后开始正式扫描。暂停按钮先改编辑区，保存才生效；暂停不关闭活动事件。修改来源/字段/规则会重新累计确认；历史报告保留冻结定义。将一个 check ID 改去监测另一个业务前，先处理其活动事件，并为不同业务创建新的 check ID。

配置保存在系统库，启动时已激活版本优先于初始配置文件。页面版本回退重新激活完整历史配置；不是数据库数据恢复，也不会重跑旧诊断。

## 4. 通知与人工处理

配置 channels（webhook 或 local_sink）、确认协议 `http_status`（任意 2xx）或 `code_zero`（2xx 且 JSON code=0），再把渠道 ID 填入检查的 notification_routes。令牌使用 env/file/stored 引用，不放在 URL、SQL、手册或正文。

“发送合成测试消息”只由管理员显式操作，发送无业务数据的测试消息，不创建正式异常。接收端应按 Idempotency-Key 去重；实际投递存在有限重试与未知确认状态，不承诺严格一次送达。各渠道的版本顺序独立，单个渠道失败不挡住其他渠道后续版本。

异常页面查看快照、证据、原因与建议，再填写原因关闭。`closed_unresolved` 表示人工关闭，业务仍可能异常；持续异常不会反复新建。确认业务恢复后再次失败，才产生新 episode。

保留策略只预览：页面“查看 30 天保留预览”执行 COUNT，不删除证据、通知、快照或事件。没有自动清理作业。

## 5. SSH Helper 与日志

监测容器无需 Docker socket，也不执行日志中的 Shell 命令。用户在目标 Linux 服务器自行安装本项目 Go Helper、建立受限账号并授予指定日志只读权限。演示 SSH 镜像只用于本地示例，不能代替目标端安装。

从对应架构的 SSH 镜像可提取 `/opt/bid-mon-helper`，或在 Go 构建容器中编译 cmd/evidence-helper。配置专用 SSH 公钥与主机指纹，known_hosts 中的主机/端口必须匹配实际目标。

目标 authorized_keys 示意（实际安装路径可固定调整）：

```text
restrict,command="/opt/bid-mon-helper" ssh-ed25519 <monitor-public-key>
```

Helper 默认读取 `/etc/bid-mon-helper/profiles.json`，签名密钥默认 `/ssh-target/binding.key`。SSH 会话不保证继承容器或 sshd 的环境变量，使用默认固定路径或受控启动包装文件。保留原 sshd 设置，不由应用自动安装或重启远端服务。

客户端配置示例 `deploy/phase2/evidence.template.json` 含 targets 与 profiles；远端文件示例 `helper-profiles.template.json` 只含该目标的 profiles。对应 profile_id、target_id、service_id、check_ids 和绑定密钥必须一致。目标端本地绝对 root 与相对 files 是受限白名单，不接收任意远端路径。

支持 JSONL 的关联键、时间键，纯文本的关联/时间正则，受限轮转文件以及心跳格式。尾部读取有字节/行数/单行预算，标记截断。路径模板 `{resource_id}` 仅接受短安全路径字符；普通 JSONL 内容匹配可用最长 512 字节对象键。签名请求绑定事件、检查、目标、服务、profile、对象与时窗，拒绝越界、未授权检查、过期签名及主机密钥变化。

管理 API 提供 `POST /internal/checks/{id}/evidence-preview`，正文 `{"entity_key":"object-id"}`。使用本机管理员会话/API鉴权，返回一次受限证据，不建立正式事件。尚未配置 SSH 时仍可监测数据库，报告会显示 logs_not_configured。

## 6. 规则与模型诊断

diagnostic_rules 按取证内容匹配已知错误，明确给出规则支持的原因假设与建议；多类别冲突保留证据不足。runbook 可直接写简短说明；读取挂载文件使用 `file:/etc/bid-mon/runbook.md` 前缀，读取与输出有限额。

模型关闭时仍有规则报告和初始通知。开启需把自己的模型配置 JSON 放进当前安装的 config 目录，设置 .env 中 `MON_MODEL_CONFIG_FILE=/etc/bid-mon/model.json` 并重建 monitor 容器；同时在特定检查配置 hybrid 与 allow_model=true。模型 API 要求 HTTPS Responses 兼容接口，文件格式见 README 首期模型示例。

模型仅获得脱敏证据、规则报告、手册和允许引用编号，无工具或写权限。未知错误可给出原因假设，但不能改变规则事实、确定性或业务恢复状态；格式错误、伪造引用、超时会回退规则报告。本次通用场景真实闭环未调用外部模型，模型 HTTP 契约和回退以本地模拟服务验证。真实业务证据需另行明确授权其发送范围与目的地。

指标 `GET /metrics` 需要管理员 API 鉴权，包含 Go/process 指标、配置检查数、最新失败数及检查耗时。就绪表示系统可管理，不保证所有业务连接成功；逐检查健康在页面和 /internal/config 中查看。

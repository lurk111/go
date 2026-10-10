# 二期部署与分发

当前二期开发版本为 `2.0.0-dev`。运行环境只需要 Docker 与 Docker Compose；Go、Node 和 Python 构建/演示运行都在容器中完成。当前没有公开发布的镜像，源码用户先使用 `-Build` / `--build`。正式发布后指定已发布的固定镜像即可跳过本机编译。

## 1. 启动

在项目根目录执行。Windows 使用 Docker Desktop 的 Linux 容器；Linux/macOS 安装带 Compose 插件的 Docker。

### 业务模式：Go + PostgreSQL

Windows PowerShell：

```powershell
.\start.ps1 -Build -Instance bid-mon -Port 18080
```

Linux/macOS：

```sh
sh start.sh --build --instance bid-mon --port 18080
```

打开 `http://127.0.0.1:18080`，从 `deploy/phase2/config/credentials.json` 读取本实例的 `admin_token` 登录。初始化、服务日志和脚本不打印密钥。

第一次启动时没有活动检查，业务数据库、SSH 或模型均不是启动依赖。配置文件为 `deploy/phase2/config/monitor.json`；为空时系统仍应就绪。业务库连接使用只读账号，与系统 PostgreSQL 分开。

### 演示模式：额外启用模拟业务、日志和通知

```powershell
.\start.ps1 -Build -Mode demo -Instance bid-mon-demo -Port 18080
```

```sh
sh start.sh --build --mode demo --instance bid-mon-demo --port 18080
```

演示使用 `deploy/phase2/config/demo.json`，从无凭据的 `deploy/phase2/demo.template.json` 首次复制。MySQL、Worker、SSH 目标和通知接收器只属于演示模式。演示 SSH 身份由 Go `init-evidence` 生成，不需要宿主 Python，也不复用首期实验密钥。

同一目录的 `.env`、端口和项目名在重复初始化时保留。运行多个实例请分别使用独立安装目录，首次指定不同的 `-Instance` / `--instance` 与端口；Docker 会为每个项目隔离网络和命名卷。改变端口或项目名时，修改本实例 `.env` 并重新启动；重新指定 init 参数不会覆盖已有安装。

演示切换为业务模式时，使用原演示 Compose 执行 `down` 后以业务模式启动，保留卷；避免遗留 Worker 在后台运行。已有首期 `deploy/compose.go.yml` 与二期根目录 Compose 是独立环境，不能共用数据库卷来替代迁移步骤。

## 2. 预构建发布镜像

维护者尚未发布此开发版本。以下 `ghcr.io/<owner>/<repository>:v2.x.y` 是地址形式示例，必须替换成真实、可访问且已经发布的版本：

```powershell
.\start.ps1 -Image 'ghcr.io/<owner>/<repository>:v2.x.y'
```

```sh
sh start.sh --image 'ghcr.io/<owner>/<repository>:v2.x.y'
```

演示发布包同时指定 `-DemoImage` / `--demo-image` 和 `-SSHImage` / `--ssh-image`。CI 默认标签约定为主服务 `<repository>:v2.x.y`、模拟服务 `<repository>-demo:v2.x.y` 和 SSH 目标 `<repository>-ssh:v2.x.y`。镜像仓库需允许使用者拉取，私有仓库先 `docker login`。

`.github/workflows/images.yml` 在维护者推送 `v2.*` 标签时构建并发布到其 GitHub 仓库对应的 GHCR 路径；手动工作流只构建。此开发过程没有执行发布。Dockerfile 支持 `TARGETOS` / `TARGETARCH`，CI 声明 `linux/amd64` 与 `linux/arm64`；实际架构启动验收结果另行记录，配置声明不等于运行验证。

发布包应包含 Compose、启动脚本、配置模板、本文和明确版本清单。发布包排除 `.env`、`deploy/phase2/config`、原 `deploy/.env`、模型凭据、缓存、测试证据和 Docker 卷。各实例初始化自己的凭据。

## 3. 初始化、配置与检查

`compose.bootstrap.yaml` 不依赖必填 `.env` 或业务数据库，可在空安装目录使用预构建镜像初始化，避免“先有凭据才能生成凭据”的循环：

```sh
docker compose -f compose.bootstrap.yaml run --rm -T initializer init --dir /workspace --mode business --instance bid-mon --port 18080
```

源码开发者需先 `docker build -f Dockerfile.go --target runtime -t bid-mon:2.0.0-dev .`。普通发布包使用已有镜像，无此步骤。

初始化只创建缺失文件，使用安全随机数生成管理员和数据库凭据；重复执行保留既有 `.env`、配置、模型设置和 SSH 私钥。既有文件损坏或 SSH 身份/签名密钥不一致时返回错误，不通过替换密钥掩盖问题。

检查安装文件（包含数据库加密主密钥 master.key）：

```sh
docker compose -f compose.bootstrap.yaml run --rm -T initializer doctor --dir /workspace
```

检查应用就绪与容器状态：

```sh
docker compose --env-file .env -f compose.yaml ps
docker compose --env-file .env -f compose.yaml exec -T monitor /bid-mon health
```

演示环境的服务操作加上 `-f compose.demo.yaml`。安装文件检查只验证文件可访问；完整规则与查询验证由配置校验/试运行功能完成，不创建正式事件。部署 Doctor 不主动发送通知或调用付费模型。

严格校验配置结构、只读查询和 CEL 规则，或者检查本地 secret 引用：

```sh
docker compose --env-file .env -f compose.yaml exec -T monitor /bid-mon config validate --file /etc/bid-mon/monitor.json
docker compose --env-file .env -f compose.yaml exec -T monitor /bid-mon doctor --file /etc/bid-mon/monitor.json
```

需要实际连接数据库时显式追加 `--connections`，每个连接检查受 5 秒总预算限制，不执行监测查询或产生事件。`stored:source-name` 引用由运行中的系统数据库加密存储解析；独立 CLI 不读取系统库时会明确跳过，实际连接测试通过管理 API 进行，不把未验证引用标为连接成功。

页面接入的业务 DSN 使用加密存储，`deploy/phase2/config/master.key` 是本实例加密主密钥，初始化随机生成且重复运行保留。该文件丢失后无法恢复数据库里的密文凭据，务必与系统库一起备份。手工配置也可使用 `SecretRef.file`，放在本机权限受限文件中，例如 `/etc/bid-mon/source-dsn.txt` 对应 `deploy/phase2/config/source-dsn.txt`。若用环境变量引用，需在本实例 Compose 的 monitor.environment 中显式注入，不把 DSN 明文写入可分发模板。

外部业务位于宿主机时，配置中的 `localhost` 指容器自身。可使用 `host.docker.internal` 访问宿主机（Compose 已提供 host-gateway）；远程业务填写其实际域名/IP并配置网络与只读账号。容器网络不自动赋予数据库权限。

模型默认不启用。自行配置 `model.json` 后在 `.env` 设置 `MON_MODEL_CONFIG_FILE=/etc/bid-mon/model.json`，只发送明确允许的证据。无模型时规则报告继续可用。

## 4. SSH 取证部署

演示目标和 Go 初始化命令采用既有卷契约：客户端 `id_ed25519`、`known_hosts`、`binding.key` 位于 `/ssh-client`；目标端 host_key、authorized_keys、binding.key 位于 `/ssh-target`；证据位于 `/evidence`。不同实例各自生成密钥。

真实服务器必须由有权限的管理员安装对应架构的 Go Helper、受限账号、forced-command、服务日志 profile 和签名密钥。真实主机指纹从可信渠道登记，不用演示 known_hosts。Helper 只读授权日志，不提供远程任意命令、修改业务或自动修复。

Linux/macOS 启动脚本让业务容器使用安装者 UID/GID，生成文件模式 `0600`；以 root 安装时容器默认使用 `10001`。演示的 SSH 目标 diag 用户保持 `10001`，客户端密钥所有者与监测服务一致。Windows Docker Desktop 的文件映射不等同 POSIX 文件权限，应保留本机目录访问限制。

## 5. 停止、更新与备份恢复

停止服务保留数据：

```sh
docker compose --env-file .env -f compose.yaml down
```

不要在正常停止/升级中加入 `-v`；该选项会删除本实例命名卷。

升级前记录当前镜像版本，备份 `.env`、`deploy/phase2/config`（包括 master.key、业务/模型配置）及系统 PostgreSQL。可用数据库容器创建二进制备份，再 `docker compose cp` 导出，避免旧版 PowerShell 重定向损坏二进制内容：

```sh
docker compose --env-file .env -f compose.yaml exec -T postgres pg_dump -U monitor -d bid_mon -Fc -f /tmp/bid-mon-backup.dump
docker compose --env-file .env -f compose.yaml cp postgres:/tmp/bid-mon-backup.dump ./bid-mon-backup.dump
```

更新 `.env` 中的固定 `BID_MON_IMAGE`，执行 `docker compose pull` 与 `up -d --wait`，查看迁移/就绪日志。不要仅更换旧镜像就宣称完成回滚；若数据库 schema 不兼容，应恢复与旧版本匹配的配置和备份。

恢复时先停止 monitor，确保没有后台扫描/投递，并在准备好的空系统库中导入备份。导入示例：

```sh
docker compose --env-file .env -f compose.yaml stop monitor
docker compose --env-file .env -f compose.yaml cp ./bid-mon-backup.dump postgres:/tmp/bid-mon-backup.dump
docker compose --env-file .env -f compose.yaml exec -T postgres pg_restore -U monitor -d bid_mon --no-owner --exit-on-error /tmp/bid-mon-backup.dump
```

上例不会自动清库；向已有库直接恢复可能冲突，应先明确恢复目标并按版本演练。恢复旧备份可能重试已送达但未记录的通知，渠道需要支持稳定幂等键。数据恢复、跨版本迁移及跨架构启动均需独立验收，本文不把尚未执行的演练标为已通过。

## 6. 故障排查

- Docker daemon 不可用：启动 Docker Desktop 或 Docker 服务，再运行脚本。
- 提示开发镜像不存在：当前没有公开镜像，先用 `-Build` / `--build`，或传真实已发布镜像。
- 镜像拉取失败：确认仓库、固定版本、网络和私有仓库登录；初始化失败不会删除已有设置。
- 端口占用：修改本实例 `.env` 的 `BID_MON_HTTP_PORT`，再启动。
- 首次 `.env` 不完整：按错误提示补缺失配置；初始化不会重置已有凭据。
- 登录失败：使用本实例 credentials.json 的 token，确认它与 `.env` 中 MON_ADMIN_TOKEN 一致；不要复制其他实例文件。
- 数据源不通：检查宿主/容器地址、网络、账号与只读权限；管理员页面应保持可登录。
- SSH 身份校验失败：检查绑定密钥、主机指纹、forced-command 与目标端 profile，保留原密钥进行排查。

已在 Windows + Docker Desktop Linux/amd64 上验证：从安全部署包解压到独立目录，使用已构建镜像和 start.ps1 启动业务模式；仅 PostgreSQL 与 Go 两服务，容器 UID/GID 为 10001，零业务来源/检查时就绪成功；严格配置 CLI 和 Doctor 通过；重复初始化后 `.env`、管理员凭据、加密主密钥与监测配置内容完全保留。测试实例 `bid-mon-v2-test` 使用 18090，未改原环境 18080。

新增初始化/CLI 有 9 项 Go 竞态测试通过，Windows PowerShell 与 POSIX shell 脚本通过语法检查。arm64、macOS 和 Linux 宿主脚本实启尚未验证；演示完整业务和 SSH 协议结果由二期验收记录另外列明，不把配置声明当作平台验收。事实记录见 `artifacts/phase2-deployment.json`。

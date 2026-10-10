# bid_mon 二期部署包

此包只包含部署配置、初始化模板、示例业务与使用说明，不包含本机配置、密钥、缓存、源码或 Docker 数据卷。

当前版本：`2.0.0-dev`，开发镜像尚未推送到公开仓库。使用此开发包前，在源码目录构建镜像，或提供可访问的固定发布镜像。不要把 `bid-mon:2.0.0-dev` 当作已公开可拉取的地址。

源码构建主服务（仅维护者/开发者）：

```sh
docker build -f Dockerfile.go --target runtime -t bid-mon:2.0.0-dev .
```

准备好镜像后，解压部署包到独立目录，在该目录运行：

```powershell
.\start.ps1 -Instance my-monitor -Port 18080
```

```sh
sh start.sh --instance my-monitor --port 18080
```

正式发布镜像可用后，启动时传 `-Image` / `--image` 指向真实仓库与固定标签；不需要宿主 Go、Node 或 Python。`-Build` / `--build` 是源码目录的开发入口，此部署包没有源码，因此不使用该选项。

业务模式只启动 Go 与系统 PostgreSQL，不要求先配置业务数据库。打开脚本显示的地址，从本实例生成的 `deploy/phase2/config/credentials.json` 读取 token 登录并接入业务。

演示模式另外需要模拟服务与 SSH 目标镜像，本地开发默认 `bid-mon-lab:2.0.0-dev`、`bid-mon-go-ssh:2.0.0-dev`。维护者在源码目录构建，或者为用户发布镜像并用 `-DemoImage`/`-SSHImage`（POSIX 对应 `--demo-image`/`--ssh-image`）指定。使用 `-Mode demo` / `--mode demo` 启用演示。

随项目交付的 `bid-mon-runtime-amd64.tar` 是本地预构建业务镜像，可用 `docker load -i bid-mon-runtime-amd64.tar` 导入，再执行启动脚本，无需本地编译。系统 PostgreSQL 镜像仍需从 Docker Hub 拉取；该 tar 不是全离线镜像包，适用于 Linux/amd64 容器宿主。

完整操作、权限、配置校验、SSH 安装边界、升级与备份恢复见 `docs/deployment.md`。`deploy/phase2/images.json` 记录开发镜像清单与发布状态；`manifest.json` 记录包内文件 SHA256。备份系统数据库时同时保留本实例的加密主密钥 `deploy/phase2/config/master.key`。

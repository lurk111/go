# GitHub 源码上传说明

二期核心版本为 `2.0.0-dev`。源码仓库用于保存实现、测试、配置模板和说明；运行实例的凭据、数据库卷和本地验收证据留在安装环境。

## 1. 上传内容

提交 cmd、internal、web、webui/embed.go、示例、运行手册、Docker/Compose、启动及验收脚本、依赖锁文件和项目文档。webui/dist/index.html 是使未构建的 Go embed 目录存在的提示页；正式 Docker 构建生成真实前端，生成的 assets 不提交。

保留首期售前适配器和模拟服务，它们仍属于已验证示例。原始本地业务设计文档默认不提交，用项目导读说明通用实现。

.gitignore 排除 .env、deploy/phase2/config、模型本地配置、SSH 私钥、主密钥、artifacts、tmp、依赖缓存、镜像导出和完整备份。被忽略文件仍留在本机，不删除。共享的是模板，不是自己已经初始化的 config 目录。

## 2. 检查待提交内容

在项目根目录执行：

```powershell
git status --short
git add .
python scripts/check_publish.py
git diff --cached --stat
git diff --cached --check
```

检查脚本读取 Git 索引中的实际待提交内容，而非只检查工作区；遇到运行文件、私钥、常见令牌、过大的文件或已知本机凭据泄露时退出失败。输出只含路径、规则及位置，不显示匹配的秘密。它提供发布检查，不能证明所有信息都适合公开，仍应阅读首次提交内容。

不要使用 git add -f 绕过运行目录的忽略规则。上传前检查 git status 及脚本结果。

## 3. 新建空仓库后上传

使用自己确定的账号/组织、仓库名称及公开或私有设置创建仓库。项目尚未单独选择开源许可证；公开可见和授权他人使用是不同事项，许可证应由维护者明确选择。

远端为空时可执行：

```powershell
git commit -m "Initial phase 2 business monitoring system"
git remote add origin https://github.com/YOUR_ACCOUNT/bid_mon.git
git push -u origin main
```

上面的 URL 是占位示例，必须替换成真实仓库。Git 使用本机凭据管理器或已配置 SSH 身份，不把个人访问令牌写入命令、remote URL 或源码。登录应通过 GitHub 或凭据管理器完成，不在聊天中发送令牌。

如果远端已有 README 或其他提交，先读取远端历史并选择保留方式；不要用 force push 覆盖已有内容。

## 4. 上传后的验收

确认远端提交和文件树，检查运行目录及凭据没有进入仓库。从新目录拉取并按 README 构建，验证源码不依赖本机 artifacts、node_modules 或已有 config。

上传源码与发布镜像是两个动作。.github/workflows/images.yml 只在 v2.* 标签推送时发布镜像；推送 main 不触发该镜像发布流程。本轮只准备源码上传，不创建发布标签。

实际二期功能验收结果见 [phase-2-validation.md](phase-2-validation.md)。原始验收产物在本机 artifacts 中，文档说明测试条件和边界；仓库不携带本地运行数据。

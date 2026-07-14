# Kiro-Go

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

将 Kiro 账号转换为 OpenAI / Anthropic 兼容的 API 服务。

[English](README.md) | 中文

如果这个项目帮到了你，欢迎点个 Star 支持一下。

## 功能特性

- Anthropic `/v1/messages` 与 OpenAI `/v1/chat/completions`
- 多账号池轮询负载均衡
- 自动 Token 刷新、SSE 流式输出、Web 管理面板
- 多种认证方式：AWS Builder ID、IAM Identity Center (企业 SSO)、SSO Token、本地缓存、凭证 JSON
- 用量追踪、账号导入导出、中英双语
- 支持设置出站代理（SOCKS5 / HTTP）

## 快速开始

### Docker Compose（推荐）

```bash
git clone https://github.com/Quorinex/Kiro-Go.git
cd Kiro-Go
mkdir -p data
docker-compose up -d
```

### Docker 运行

```bash
docker run -d \
  --name kiro-go \
  -p 8080:8080 \
  -e ADMIN_PASSWORD=your_secure_password \
  -v /path/to/data:/app/data \
  --restart unless-stopped \
  ghcr.io/quorinex/kiro-go:latest
```

### 源码编译

```bash
git clone https://github.com/Quorinex/Kiro-Go.git
cd Kiro-Go
go build -o kiro-go .
./kiro-go
```

### 部署到 Zeabur

仓库已包含 `Dockerfile`，可直接在 Zeabur 上构建运行。

**方式一：面板一键部署**

1. Fork 本仓库到你的 GitHub 账号。
2. 在 Zeabur 新建服务，选择 **Deploy from GitHub**，绑定刚才 fork 的仓库。
3. Zeabur 自动识别 `Dockerfile` 并完成构建。
4. 在 **Networking** 标签暴露端口 `8080` 并绑定域名。
5. 在 **Variables** 标签至少设置 `ADMIN_PASSWORD`（管理面板密码）。
6. 如需持久化账号 / 配置，挂载 Volume 到 `/app/data`。

**方式二：CLI 部署**

```bash
npm i -g zeabur
zeabur auth login
zeabur deploy
```

> 命令需在项目根目录执行。CLI 会生成 `.zeabur/context.json` 记录目标 project / service，包含个人 ID，请勿提交。

部署完成后访问 `https://<你的域名>/admin` 登录管理面板。

首次运行会在 `data/config.json` 自动生成配置，挂载 `/app/data` 以持久化。默认管理密码为 `changeme`，生产环境请务必通过 `ADMIN_PASSWORD` 环境变量或在管理面板中修改。

## 使用方法

访问 `http://localhost:8080/admin` 登录、添加账号，然后调用 API：

```bash
# Claude
curl http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"claude-sonnet-4.5","max_tokens":1024,"messages":[{"role":"user","content":"你好！"}]}'

# OpenAI
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer any" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"你好！"}]}'
```

## 思考模式

在模型名后加后缀（默认 `-thinking`）即可启用，例如 `claude-sonnet-4.5-thinking`。Claude 兼容请求如果带有顶层 `thinking` 配置，例如 `{"type":"enabled","budget_tokens":2048}` 或 `{"type":"adaptive"}`，也会自动启用 thinking 模式。输出格式可在管理面板「设置 - Thinking 模式」中配置。

### 透传客户端 thinking 预算/强度

「设置 - Thinking 模式」中的**透传客户端 thinking 预算/强度**开关控制如何处理客户端请求的预算/强度，**默认关闭**。

- **关闭（默认）：** 向后兼容。只要启用了思考（通过后缀或 `thinking` 请求），就使用固定指令 `max_thinking_length=200000`，并忽略客户端的任何 effort 字段。
- **开启：** 代理会渲染等效的 Kiro 系统指令而非固定提示，以保留客户端意图。它会读取：
  - Claude Messages 的 `thinking.type`（`enabled`/`adaptive`/`disabled`）与手动 `thinking.budget_tokens`
  - Claude Messages 的 `output_config.effort`
  - OpenAI Chat Completions 的 `reasoning_effort`
  - OpenAI Responses 的 `reasoning.effort`

  可接受的 effort 值：`low`、`medium`、`high`、`xhigh`、`max`。开启时的优先级规则：
  - 客户端显式配置优先于模型后缀。
  - Claude 显式 `thinking.type=disabled` 会关闭思考，即使带有 `-thinking` 后缀。
  - 具体的手动 `budget_tokens` 优先于 effort 信号。
  - 当没有显式的客户端 thinking/effort 时，触发后缀仍作为回退（保留固定的 `200000` 预算）。
  - 显式提供的无效 effort 会按协议标准 400 错误拒绝，而不会被静默降级。

请求示例（开启）：

```jsonc
// Claude Messages —— 精确保留手动预算
{"model":"claude-sonnet-4.5","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[...]}

// Claude Messages —— 自适应强度
{"model":"claude-sonnet-4.5","thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[...]}

// OpenAI Chat Completions
{"model":"claude-sonnet-4.5","reasoning_effort":"medium","messages":[...]}

// OpenAI Responses
{"model":"claude-sonnet-4.5","reasoning":{"effort":"high"},"input":[...]}
```

这是**通过 Kiro 兼容系统指令实现的语义透传**，而非向 Kiro 原生推理字段的原始 JSON 转发。Anthropic 原生 effort 可能影响更广泛的生成与工具行为；代理会通过 Kiro 中可用的指令保留请求的信号，但不保证等效的上游强制执行。

## 出站代理

可在管理面板「设置 - 出站代理设置」中配置代理。支持 SOCKS5 和 HTTP 代理。

设置保存后即时生效，无需重启服务。

## 环境变量

| 变量 | 说明 | 默认值 |
|-----|------|-------|
| `CONFIG_PATH` | 配置文件路径 | `data/config.json` |
| `ADMIN_PASSWORD` | 管理面板密码（覆盖配置文件） | - |

## 参与贡献

欢迎友好交流。遇到问题时，建议先让 Claude Code、Codex 等工具帮忙排查一下，大部分问题都能自己解决。如果能直接提个 PR 就更好了。

## 联系方式

Telegram：[@tutua16888](https://t.me/tutua16888)

## 友情链接

- [LINUX DO](https://linux.do)

## 免责声明

本项目仅供学习和研究目的使用，与 Amazon、AWS 或 Kiro 没有任何关联。用户需自行确保使用行为符合所有适用的服务条款和法律法规，使用风险自负。

## 许可证

[MIT](LICENSE)

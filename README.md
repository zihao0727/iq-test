# SEVNX 模型质量实验室

Go 标准库实现，前端使用 `go:embed` 嵌入程序。Linux 上无需 Node.js、Go 运行时或外部数据库。后台默认巡检与访客自定义检测独立运行，请求模型与思考强度统一由后端固定。

## 访客自定义检测

右侧工作台允许用户填写自己的 HTTPS API URL 和 API Key，选择双项检测、糖果题或鹈鹕动画。无需管理员令牌，也不依赖后台是否配置 `API_KEY`。

- 自测调用只使用访客提供的密钥，绝不回退到后台密钥，不修改默认巡检设置。
- 同样使用 `gpt-6-astra`、`low`、`/v1/responses`；用户确认上游费用后提交。
- 结果只在内存保留一小时，通过 HttpOnly、SameSite=Strict 会话 Cookie 校验归属。不会公开列出，不写入巡检记录，不计入通过率。
- 页面刷新后本次会话列表清空；Key 不保存到 localStorage、sessionStorage、文件或日志，成功提交后清空输入框。
- 仅允许 HTTPS 公网上游，阻止内网、回环、链路本地与保留 IP。连接时解析并逐项校验 DNS，直接连接校验后的 IP，禁用重定向和环境代理。
- 全局最多 4 个自测任务，同时每个直连来源最多 2 个，提交间隔至少 3 秒。内存最多保留 64 次自测任务。反向代理后的访客默认共享代理来源限额；高流量部署请配合网关限流，不信任访客可伪造的转发头。
- 服务重启会清空自测结果；不提供真实 API Key 时，可以用 mock 上游测试，避免产生费用。

## 已固定的检测参数

- 上游默认：`https://www.sevnx.lol/v1/responses`
- 模型：`gpt-6-astra`
- 思考强度：两项均为 `low`
- 启动后立即并发请求糖果题与鹈鹕动画，之后每二十分钟一轮；不允许重叠执行。
- 无联网工具，非流式 Responses 请求；每个请求超时 240 秒，不自动重试，避免重复计费。
- 糖果题输出额度 8192 tokens；动画 16000 tokens，包含推理消耗。
- 糖果题答错或请求异常后最多重试 3 次（含首次最多 4 次请求），通过或任务取消后停止；默认巡检与访客自测均适用。每题保存最终结果，耗时与输出 tokens 累计所有尝试；鹈鹕动画不重试。
- 模型名按需求原样发送；是否支持由你的 API 服务决定。

## 配置文件

程序启动目录必须存在 `config.yaml`，程序不会再读取环境变量。复制 `config.example.yaml` 为 `config.yaml`，填写 `api_key` 和其他参数；生产环境建议 `chmod 600 config.yaml`。修改配置后重启程序生效。

## Linux 单文件启动

根据服务器架构选择 `dist/sevnx-lab-linux-amd64` 或 `dist/sevnx-lab-linux-arm64`。

```bash
chmod +x ./sevnx-lab-linux-amd64
./sevnx-lab-linux-amd64
```

默认仅监听本机的 `127.0.0.1:8090`。通过 HTTPS 反向代理公开访问；临时局域网访问可设 `listen_addr: "0.0.0.0:8090"`，并配置防火墙。不要在公共明文 HTTP 页面提交 API Key 或管理员令牌。

### config.yaml 参数

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `api_key` | 空 | 后台默认上游密钥；缺省不运行默认巡检，访客自测仍可用 |
| `api_base_url` | `https://www.sevnx.lol` | 接受根地址、`/v1` 或完整 `/v1/responses` |
| `admin_token` | 空 | 手动检测的独立管理员令牌；缺省禁用手动检测 |
| `listen_addr` | `127.0.0.1:8090` | 监听地址 |
| `data_dir` | `./data` | 历史记录持久化目录 |

前端不能修改后台默认上游密钥或模型，右侧表单仅用于访客自测。管理员令牌仅用于顶部默认巡检的手动触发，不保存在浏览器存储中。

## systemd 部署

```bash
sudo install -d /opt/sevnx-lab
sudo install -m 755 dist/sevnx-lab-linux-amd64 /opt/sevnx-lab/sevnx-lab
sudo install -m 600 deploy/sevnx-lab.env.example /etc/sevnx-lab.env
sudoedit /etc/sevnx-lab.env
sudo install -m 644 deploy/sevnx-lab.service /etc/systemd/system/sevnx-lab.service
sudo systemctl daemon-reload
sudo systemctl enable --now sevnx-lab
sudo journalctl -u sevnx-lab -f
```

务必将示例密钥和管理员令牌替换为真实值，或者删除 `ADMIN_TOKEN` 来禁用手动运行。

`deploy/nginx.conf.example` 提供反向代理片段。建议将检测页放在独立域名，例如 `lab.sevnx.lol`，不要覆盖现有 `www.sevnx.lol` 上游 API。域名、DNS、TLS 和线上部署需要你服务器的相应配置；本项目不会修改现有站点。

## 判定与安全边界

糖果题按可凭手感选择形状判定，答案 **21**。固定取 9 颗圆形、12 颗五角星形一定成功，测试使用穷举验证所有形状配额的最小保证数量。模型回复文本中只要出现数字 `21` 即判为通过，否则判为未通过。失败请求不计入通过率分母。时间线只显示默认巡检糖果题，每个二十分钟时段显示最新一条；统计包括管理员手动巡检，不包括访客自测。

动画仅验证包含 HTML、SVG，**不表示视觉质量或动画行为合格**。预览使用 iframe sandbox 和 CSP，禁止外部请求、同源权限、表单、嵌入其他页面。模型输出始终视为不可信。

检测记录总计最多保留 200 条，其中成功生成的动画作品最多保留 100 条，超出时优先清理最旧记录。每次保存及启动加载已有数据时均执行清理，原子写入 `records.json`，启动恢复中断状态。单实例运行，不要多个进程共用数据目录。未配置 Key 时不会填充演示记录。页面示意插画明确标记为非检测结果。

默认巡检记录和原始模型输出为公开信息，访客自测结果受会话保护。后台 Key 不返回到前端，所有 Key 不写入日志或记录；返回文本中的原样密钥会脱敏，HTTP 错误仅公开状态码。默认 API 地址会公开，勿在其路径中放置敏感信息。默认密钥由进程环境提供，需要保护服务器账户及配置文件权限。不要向提示词添加机密内容。

## 开发与构建

Go 1.24 或更新版本。没有第三方 Go 依赖。

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/sevnx-lab-linux-amd64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/sevnx-lab-linux-arm64 .
go run .
```

PowerShell 交叉编译：

```powershell
$env:CGO_ENABLED='0'
$env:GOOS='linux'
$env:GOARCH='amd64'
go build -trimpath -ldflags="-s -w" -o dist/sevnx-lab-linux-amd64 .
$env:GOARCH='arm64'
go build -trimpath -ldflags="-s -w" -o dist/sevnx-lab-linux-arm64 .
Remove-Item Env:GOOS, Env:GOARCH
```

测试通过 mock 上游验证协议、认证、密钥脱敏、并发检测、错误响应、存储恢复和隔离策略，不产生真实 API 费用。

界面图标基于 Lucide，许可证见 `LICENSE-icons.txt`。示意鹈鹕插画为本项目制作。

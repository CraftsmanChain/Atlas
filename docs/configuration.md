# Atlas 配置与启动说明

本文档说明 Atlas 的配置文件、端口、自定义启动方式，以及 macOS 本地启动与 Linux amd64 交叉编译方法。

## 1. 默认配置文件

默认配置文件路径：

```text
configs/config.yaml
```

当前示例：

```yaml
gateway:
  port: ":8080"
  webhook_token: ""
  feishu_webhook_token: ""
storage:
  dsn: "atlas.db"
feishu:
  bots:
    - enabled: false
      webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/YOUR_WEBHOOK_URL"
      enable_signature: false
      secret: ""
logging:
  dir: "logs"
```

## 2. 配置项说明

### `gateway.port`

- 服务监听端口
- 示例：`:8080`

### `gateway.webhook_token`

- Atlas 原生告警接口 `/api/v1/webhook/alert` 的请求头 token
- 当非空时，需要请求头：

```text
X-Webhook-Token: <token>
```

### `gateway.feishu_webhook_token`

- 飞书兼容接收接口 `/open-apis/bot/v2/hook/{token}` 使用的 path token
- 当非空时，路径中的 `{token}` 必须与该配置一致

### `storage.dsn`

- SQLite 数据库文件路径
- 默认：`atlas.db`

### `feishu.bots`

- Atlas 处理后向飞书机器人发送通知时使用
- 与“飞书兼容告警接收接口”不是同一个方向

### `logging.dir`

- 服务日志目录
- 默认：`logs`

## 3. 自定义配置文件路径

Atlas 服务端支持通过以下方式指定配置文件：

### 方式 A：命令行参数

```bash
go run ./cmd/server --config /path/to/custom-config.yaml
```

### 方式 B：环境变量

```bash
ATLAS_CONFIG=/path/to/custom-config.yaml go run ./cmd/server
```

## 4. 运行时端口覆盖

如果你不想改配置文件，也可以通过环境变量覆盖监听端口：

```bash
ATLAS_PORT=18080 go run ./cmd/server --config configs/config.yaml
```

说明：

- `ATLAS_PORT=18080` 会自动规范成监听地址 `:18080`
- 该覆盖优先级高于配置文件中的 `gateway.port`

## 5. macOS 本地启动脚本

### 启动后端

```bash
bash scripts/start_backend_mac.sh
```

使用自定义配置：

```bash
bash scripts/start_backend_mac.sh /path/to/custom-config.yaml
```

使用环境变量覆盖端口：

```bash
ATLAS_PORT=18080 bash scripts/start_backend_mac.sh
```

### 启动前端

```bash
bash scripts/start_frontend_mac.sh
```

自定义前端端口：

```bash
ATLAS_WEB_PORT=4174 bash scripts/start_frontend_mac.sh
```

自定义前端代理后端地址：

```bash
ATLAS_API_TARGET=http://127.0.0.1:18080 bash scripts/start_frontend_mac.sh
```

说明：

- 前端脚本会把 `ATLAS_API_TARGET` 注入 Vite 开发代理
- 这样在本地调试时，前后端端口不必固定写死

## 6. Linux amd64 交叉编译

脚本：

```bash
bash scripts/build_linux_amd64.sh
```

输出目录默认：

```text
bin/linux-amd64
```

产物：

- `atlas-server`
- `atlas-agent`

## 7. 交叉编译注意事项

Atlas 当前使用 SQLite 驱动 `mattn/go-sqlite3`，该驱动依赖 CGO。

因此从 macOS 交叉编译到 Linux amd64 时，需要可用的 Linux C 交叉编译器，脚本会优先尝试：

- `zig`
- `x86_64-linux-gnu-gcc`

如果两者都不存在，脚本会直接退出并提示安装。

## 8. 推荐本地开发方式

### 后端

- 使用 `configs/config.yaml` 或单独准备一份本地配置
- 推荐设置：
  - `storage.dsn` 指向本地测试数据库
  - `logging.dir` 指向本地日志目录

### 前端

- 默认使用 `scripts/start_frontend_mac.sh`
- 如果后端端口被改为 `18080`，则：

```bash
ATLAS_API_TARGET=http://127.0.0.1:18080 bash scripts/start_frontend_mac.sh
```

## 9. 推荐配置拆分

建议后续准备以下配置文件：

- `configs/config.yaml`
  - 默认示例配置
- `configs/config.local.yaml`
  - macOS 本地调试
- `configs/config.prod.yaml`
  - Linux 生产部署

然后通过 `--config` 或 `ATLAS_CONFIG` 选择不同环境。

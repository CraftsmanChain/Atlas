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
web:
  static_dir: "web/dist"
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

### `web.static_dir`

- 前端静态产物目录
- 当目录下存在 `index.html` 时，`atlas-server` 会直接托管 Web 页面
- 默认：`web/dist`
- 适合生产环境将前端打包后与后端一起部署

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

前端静态目录也支持环境变量覆盖：

```bash
ATLAS_WEB_DIR=/ops/atlas/web/dist ./atlas-server --config /ops/atlas/configs/config.yaml
```

说明：

- `ATLAS_WEB_DIR` 优先级高于配置文件中的 `web.static_dir`
- 当目录不存在或缺少 `index.html` 时，根路径 `/` 会退回简单文本页

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

## 6. 生产环境部署静态页面

推荐方式是让 `atlas-server` 直接托管前端打包后的静态产物，无需额外 `nginx` 代理。

### 1. 本地构建前端

```bash
cd web
npm install
npm run build
```

构建完成后会生成：

```text
web/dist
```

### 2. 上传服务器

建议至少上传这些内容：

```text
atlas-server
configs/config.yaml
web/dist
```

### 3. 配置静态目录

```yaml
web:
  static_dir: "/ops/atlas/web/dist"
```

或启动时覆盖：

```bash
ATLAS_WEB_DIR=/ops/atlas/web/dist ./atlas-server --config /ops/atlas/configs/config.yaml
```

### 4. 访问方式

- Web 页面：`http://<server>:7077/`
- 健康检查：`http://<server>:7077/health`
- API 状态：`http://<server>:7077/api/v1/status`

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

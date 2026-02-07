# 面板 WebSocket 接驳协议（用于替代部分 HTTP 拉取/上报）

本文档描述 **V2bX 节点程序** 与 **面板（Panel）** 之间的 WebSocket（WS）接驳方式。

本协议来自本仓库 `api/panel/ws_transport.go`、`api/panel/panel.go` 的实现约定：节点会优先尝试通过 WS 发起一次“类 HTTP”的请求；若失败则降级为普通 HTTP 请求。

## 1. 目的与使用范围

- **目的**：用 WS 承载面板 API 的请求/响应，减少某些场景下的 HTTP 开销或方便在特定网络环境中传输。
- **当前覆盖接口**（节点侧已使用的 HTTP 方法范围）：
  - `GET`
  - `POST`

注意：节点侧代码目前只会通过该传输层发起 `GET` / `POST`，其它方法会被视为不支持。

## 2. 连接地址与参数

### 2.1 可选 WS 地址配置（推荐）

节点支持在配置中显式指定 WS 连接信息（非必填；配置存在就会尝试）。对应 `ApiConfig`：

- `WsURL`：完整 WS 地址（优先级最高），例如 `wss://panel.example.com:51821` 或 `ws://127.0.0.1:51821/ws`
- `WsScheme`：`ws` 或 `wss`
- `WsHost`：WS host（不含端口）
- `WsPort`：WS 端口

选择规则：

- 若配置了 `WsURL`：使用 `WsURL` 作为 base（并保留其中的 `scheme/host`；若 `WsURL` 含 path 前缀，则会与请求 path 进行拼接）
- 否则：在默认推导逻辑基础上，按需使用 `WsScheme/WsHost/WsPort` 覆盖其中的 scheme/host/port

注意：无论采用哪种方式，节点都会把 `node_type/node_id/token` 追加到 WS URL 的 query 上。

### 2.2 Scheme（未显式配置时的推导）

在未配置 `WsScheme` 且未配置 `WsURL` 时，节点会根据 `ApiHost` 的 scheme 推导：

- `ApiHost` 以 `https://` 开头 => 使用 `wss`
- 否则 => 使用 `ws`

### 2.3 Host / 端口

- **默认端口为 `51821`**（未配置 `WsPort` 且未配置 `WsURL` 时生效）。
- 主机名取自 `ApiHost` 的 hostname（不取 `ApiHost` 原端口）。

例如：

- `ApiHost = https://panel.example.com` => `wss://panel.example.com:51821`
- `ApiHost = http://1.2.3.4:1234` => `ws://1.2.3.4:51821`

### 2.4 Path

WS URL 的 `Path` 与面板 HTTP API 的路径一致（节点会把真实请求 path 透传进来），例如：

- `/api/v1/server/UniProxy/config`
- `/api/v1/server/UniProxy/user`
- `/api/v1/server/UniProxy/push`

面板侧 WS 服务端**需要接受任意这些 path 的 WS Upgrade**。

### 2.5 Query 参数（鉴权/识别）

节点会在 WS URL 上附带与 HTTP 相同的一组 query 参数（见 `api/panel/panel.go`）：

- `node_type`: 节点类型（如 `vmess`/`vless`/`trojan` 等）
- `node_id`: 节点 ID
- `token`: ApiKey

示例：

```
ws(s)://panel.example.com:51821/api/v1/server/UniProxy/config?node_type=vless&node_id=1&token=xxxxx
```

面板侧应按自身既有 HTTP 鉴权逻辑校验这些参数。

## 3. 消息协议（请求/响应）

WS 连接建立后，节点会以 **TextMessage** 发送 JSON。

### 3.1 请求（wsRequest）

节点发送 JSON：

```json
{
  "method": "GET",
  "path": "/api/v1/server/UniProxy/config",
  "headers": {
    "Accept": ["application/json"],
    "If-None-Match": ["...optional..."]
  },
  "body": "..."
}
```

字段说明（对应 `wsRequest`）：

- `method`：HTTP 方法字符串（当前仅 `GET`/`POST`）
- `path`：目标 HTTP 路径（与 WS URL path 理论上相同；节点也会在消息体内再带一遍）
- `headers`：请求头（`map[string][]string`），值为数组
- `body`：原始请求 body（字节数组）
  - **注意**：这是 JSON 中的 `[]byte`，会以 base64 形式编码（Go 标准 JSON 行为）。

### 3.2 响应（wsResponse）

面板返回 JSON：

```json
{
  "status": 200,
  "headers": {
    "Content-Type": ["application/json"],
    "ETag": ["..."]
  },
  "body": "..."
}
```

字段说明（对应 `wsResponse`）：

- `status`：HTTP 状态码
- `headers`：响应头（`map[string][]string`）
- `body`：响应 body（字节数组，JSON 中为 base64）

### 3.3 一问一答约束

节点侧实现是严格的“**写一次请求 -> 读一次响应**”，并且在单个 `Client` 内通过互斥锁串行化。

因此面板侧必须遵守：

- **每收到 1 条 `wsRequest`，必须回 1 条 `wsResponse`**
- 响应顺序与请求顺序一致
- 不需要支持并发乱序（节点侧不会并发发多条请求）

## 4. 面板侧实现建议（服务端）

### 4.1 WS 服务端行为

面板侧需要提供一个 WS 服务端：

- 在 `:51821` 上监听（或由反向代理转发到面板应用）
- 对上述 API path 执行 WS Upgrade
- 对每条 `wsRequest`：
  - 按 `method/path/headers/body` 组装一次“内部 HTTP 调用”或直接调用面板现有 handler
  - 得到状态码、响应头、响应体
  - 回写 `wsResponse`
- 连接应保持打开状态，允许同一连接重复请求

### 4.2 鉴权/权限

节点侧不会在 WS message 里额外带鉴权字段；鉴权参数来自 URL query：`node_type/node_id/token`。

面板侧建议：

- 在 WS 握手阶段解析 query 并完成鉴权
- 鉴权失败：可以直接拒绝握手，或握手成功后返回 `wsResponse{status: 401/403}`

> 不确定点（需结合面板项目确认）：
> - 面板是否期望 token 同时出现在 Header（如 `Authorization`）或只接受 query。
> - 面板 HTTP 接口的鉴权失败响应格式（当前节点只关心 `status>=400` 时把 `body` 当错误字符串）。

## 5. 节点侧连接与容错行为（面板需要知道的约束）

### 5.1 长连接复用

节点侧会在一个 `Client` 实例内复用单条 WS 连接（见 `Client.wsConn`）：

- 连接存在则复用
- 读写失败会关闭连接并置空

### 5.2 断线重连与重试

当一次请求通过 WS 发送失败或读取响应失败：

- 节点会立即关闭该连接
- **重连一次并重试同一请求一次**
- 若仍失败：
  - 标记 WS 不可用（进入冷却）
  - 本次请求降级走 HTTP（`resty`）

### 5.3 WS 不可用冷却（60 秒）

当 WS 被判定不可用后：

- 在 60 秒内，节点不会再尝试 WS
- 60 秒后会恢复为“可尝试”状态

## 6. 反向代理/部署注意事项

- 需要在面板部署环境中开放或转发 `51821` 端口
- 需要允许 WS Upgrade（`Connection: Upgrade` / `Upgrade: websocket`）
- `wss` 场景下需要正确配置 TLS（节点仅要求 TLS >= 1.2）

> 不确定点（需结合面板部署确认）：
> - 面板是否与 HTTP 共用证书/域名，还是 51821 独立证书。
> - 是否由 nginx/traefik 等将 `:51821` 转发到实际面板后端。

## 7. 最小示例（伪代码）

面板侧处理循环（伪代码，仅表达逻辑）：

```pseudo
onWebSocketConnection(conn, query):
  auth(query.node_type, query.node_id, query.token)

  loop:
    msg = conn.readText()
    req = json.parse(msg)  // wsRequest

    // 将 wsRequest 转成一次面板内部的 HTTP 调用
    status, headers, body = handleHttpLike(req.method, req.path, req.headers, req.body)

    resp = { status: status, headers: headers, body: body }
    conn.writeText(json.stringify(resp))
```

---

## 变更记录

- 节点侧 WS 传输实现位置：`api/panel/ws_transport.go`
- query 参数来源：`api/panel/panel.go`

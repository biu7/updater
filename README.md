# updater

运行在 Docker Compose 中的内部服务：接收 HTTP 更新请求，对指定服务执行 `docker compose pull`；仅在能够明确确认已拉取到新镜像时才执行
`docker compose up -d`。若判断为无更新，或结果不确定，则跳过重启。任务异步执行，可通过任务 ID 查询状态。

## 环境变量

| 变量                    | 说明                                | 默认                           |
|-----------------------|-----------------------------------|------------------------------|
| `PORT`                | 监听地址，可为 `8080` 或 `127.0.0.1:8080` | `8080`                       |
| `COMPOSE_PROJECT_DIR` | compose 项目根目录（工作目录）               | `/workspace/compose-project` |
| `COMPOSE_FILE`        | 可选，compose 文件路径，多个用逗号分隔           | 无（使用项目默认）                    |
| `ALLOWED_SERVICES`    | 可选，允许更新的服务名白名单，逗号分隔               | 无（不限制）                       |
| `UPDATE_TIMEOUT`      | 单次任务超时，Go duration 格式             | `10m`                        |

## HTTP 接口

- `GET /health` — 健康检查
- `POST /update` — 请求体 `{"service":"服务名"}`，成功返回 `200`，响应中 `data.job_id` 为任务 ID
- `POST /restart` — 请求体 `{"service":"服务名"}`，异步重启指定服务，成功返回 `200`，响应中 `data.job_id` 为任务 ID
- `GET /jobs/:id` — 查询任务；`message` 为结果说明，日志放在 `data.log_tail`，`status` 可能为 `pending`、`running`、`skipped`、`succeeded`、`failed`

同一 Compose **服务名**在任意时刻只允许存在一个进行中的任务（`pending` 或 `running`）。冲突时返回 `409`，响应中带
`data.existing_job_id`。

常见返回示例如下：

```json
{
  "code": 200,
  "message": "未检测到需要更新的版本，已跳过本次更新",
  "data": {
    "id": "51bdb72b-2600-4240-970b-20d74c19dfa9",
    "service": "transfer",
    "action": "update",
    "status": "skipped",
    "log_tail": "[updater] 从 compose 解析到镜像引用: repo/app:latest\n..."
  }
}
```

创建任务时的返回示例：

```json
{
  "code": 200,
  "message": "更新任务已创建，正在后台执行",
  "data": {
    "job_id": "51bdb72b-2600-4240-970b-20d74c19dfa9",
    "service": "transfer",
    "action": "update"
  }
}
```

创建重启任务时的返回示例：

```json
{
  "code": 200,
  "message": "重启任务已创建，正在后台执行",
  "data": {
    "job_id": "51bdb72b-2600-4240-970b-20d74c19dfa9",
    "service": "transfer",
    "action": "restart"
  }
}
```

## Go SDK

客户端位于**独立子模块** `github.com/biu7/updater/pkg/client`（目录 [`pkg/client/go.mod`](pkg/client/go.mod)），**仅依赖标准库**，接入方 `go get` 时不会把服务端用的 Gin 等依赖拉进你的模块图。

服务端主模块仍为 `github.com/biu7/updater`（根目录 [`go.mod`](go.mod)）。若你 fork，请同步修改根目录与 `pkg/client` 两处 `module` 路径。

本地只测 SDK：`cd pkg/client && go test ./...`。若希望在仓库根目录同时编辑主模块与子模块，可将 [`go.work.example`](go.work.example) 复制为 `go.work`（该文件已被 `.gitignore` 忽略，勿提交）。

```bash
go get github.com/biu7/updater/pkg/client@latest
```

单独为 SDK 发版时，请使用**子模块 tag**（与根目录 `v1.2.3` 可并存），例如：`pkg/client/v0.1.0`。

```go
import (
    "context"
    "errors"
    "time"

    "github.com/biu7/updater/pkg/client"
)

func example() error {
    c, err := client.NewWithBaseURL("http://127.0.0.1:8080")
    if err != nil {
        return err
    }
    ctx := context.Background()

    h, err := c.Health(ctx)
    if err != nil {
        return err
    }
    if !h.OK() {
        return errors.New("健康检查未通过")
    }

    created, err := c.Update(ctx, "transfer")
    if err != nil {
        return err
    }
    if created.Conflict() {
        // 使用 created.ExistingJobID 继续查询或等待
        _ = created.ExistingJobID
        return nil
    }
    if !created.Created() {
        return nil // 按需处理参数错误、403 等
    }

    // 轮询直到终态；失败时 HTTP 可能仍为 200，但 res.Failed() / res.Code 会反映业务失败
    res, err := c.WaitJob(ctx, created.JobID, 500*time.Millisecond)
    if err != nil {
        return err
    }
    switch {
    case res.Succeeded():
    case res.Skipped():
    case res.Failed():
    }

    restarted, err := c.Restart(ctx, "transfer")
    if err != nil {
        return err
    }
    if restarted.Created() {
        _, err = c.WaitJob(ctx, restarted.JobID, 500*time.Millisecond)
        if err != nil {
            return err
        }
    }
    return nil
}
```

`Job` / `JobResult` 提供 `Succeeded()`、`Failed()`、`Skipped()`、`Pending()`、`Running()`、`Done()`、`InProgress()` 等方法，避免手写状态字符串判断；`Job.Action` 可用于区分 `update` 与 `restart`。任务执行失败时服务端可能返回 HTTP 200 且 JSON `code` 为 `50010`，SDK 仍以 `res.Failed()` 与 `res.Code == client.CodeJobExecutionError` 表示。

## 构建与本地运行

```bash
go build -o updater ./cmd/updater
PORT=8080 COMPOSE_PROJECT_DIR=/path/to/compose ./updater
```

## Docker 镜像

```bash
docker build -t updater:local .
```

部署时需挂载：

- `/var/run/docker.sock`
- 目标 compose 项目目录（与 `COMPOSE_PROJECT_DIR` 一致）

参考 [docker-compose.example.yml](docker-compose.example.yml)。

## 关于「未拉取新镜像」与是否跳过 `up -d`

Docker Compose 在终端里经常对「已是最新」的镜像仍显示 **Pulled**，这与是否下载新层无关，因此**不能**可靠依赖这类表格输出。

当前逻辑是：

1. 执行 `docker compose config --format json`，读取该服务的 **`image:` 字符串**；若是仅 `build`、无 `image` 的服务，则仅参考
   `pull` 输出中那些能明确说明“无需拉取”的场景，例如 `no image to be pulled / must be built from source`。
2. **pull 前**、**pull 后**各执行一次 `docker image inspect <该引用> -f '{{.Id}}'`。
3. 只有在能够**明确确认镜像已变化**时，才会执行 `up -d`。若两次 ID 相同，或因 `compose config` / `image inspect` / `pull`
   输出无法可靠判断结果，则一律**跳过重启**。

**与 `latest` / 固定 tag 的关系**：输出格式通常一样；区别在远端 digest 是否变化——固定版本 tag（如 `v0.0.65`）不变时，pull 后
ID 一般不变，可稳定跳过；`latest` 在远端更新后 pull 会得到新 ID，会正常执行 `up -d`。

若无法解析 config、无 `image` 字段，或 `image inspect`
无法给出稳定结论，仍会参考 [internal/updater/pull_parse.go](internal/updater/pull_parse.go) 中对 **Skipped / up to date**
文案的补充判定；如果这些证据仍不足以确认镜像已更新，则默认跳过重启，避免误操作。

## 说明

- 任务状态仅保存在内存中，进程重启后丢失。
- 长时间运行会累积历史任务记录，如需可后续增加容量上限或过期清理。

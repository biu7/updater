# updater

运行在 Docker Compose 中的内部服务：接收 HTTP 请求，对**指定服务**执行版本检查、拉取或更新。请求中的服务必须属于 compose 中的服务，且在配置了
`ALLOWED_SERVICES` 时属于白名单，否则返回 400。更新只对“需要 up”的服务执行 `docker compose up -d --no-deps <服务…>`，不会连带重建依赖服务；
判定不确定的服务一律不重启。任务异步执行，可通过任务 ID 或服务名查询结果。

服务本身不做认证：请只把它放在仅与调用方共享的内部网络中，不要发布端口（它持有 `docker.sock`，等同宿主 root）。

## 环境变量

| 变量                    | 说明                                | 默认                           |
|-----------------------|-----------------------------------|------------------------------|
| `PORT`                | 监听地址，可为 `8080` 或 `127.0.0.1:8080` | `8080`                       |
| `COMPOSE_PROJECT_DIR` | compose 项目根目录（工作目录，`.env` 也从这里读取）；推荐与宿主路径相同，见下文“同路径挂载” | `/workspace/compose-project` |
| `COMPOSE_PROJECT_DIRECTORY` | 可选，透传到 `docker compose --project-directory`；同路径挂载时不需要 | 无（不传该参数） |
| `COMPOSE_FILE`        | 可选，compose 文件路径，多个用逗号分隔。该变量同时会被 docker compose 自身读取，一般不要设置，使用项目目录下的默认文件名 | 无（使用项目默认）                    |
| `ALLOWED_SERVICES`    | 可选，允许操作的服务名白名单，逗号分隔               | 无（不限制）                       |
| `UPDATE_TIMEOUT`      | 单次 pull/update/restart 任务超时，Go duration 格式 | `10m`                        |

## HTTP 接口

所有响应为统一信封 `{"code":…, "message":…, "detail"?:…, "data"?:…}`；`message` 为中文说明，调用方应以 `code` 与 `data` 中的结构化字段判断结果。

| 接口 | 说明 |
| --- | --- |
| `GET /health` | 健康检查 |
| `POST /update` | `{"services":[…], "pull"?: bool}`，异步更新；`pull` 默认 `true` |
| `POST /pull` | `{"services":[…]}`，异步拉取（pull-only），不重启任何容器 |
| `POST /check` | `{"services":[…]}`，同步检查是否有可用更新；整体期限 60 秒，不占用任务锁 |
| `POST /restart` | 重启 compose 服务 ∩ `ALLOWED_SERVICES` 的全部服务（忽略请求中的 `services`），执行 `docker compose restart` |
| `GET /jobs/:id` | 按 ID 查询任务 |
| `GET /jobs/latest?service=<svc>` | 返回包含该服务的最近一个任务；没有时 404（code `40401`） |

`services` 必填、去重后保持顺序；为空、名称非法或不在“compose 服务 ∩ `ALLOWED_SERVICES`”中时返回 400（code `40002`），请求体不是合法 JSON 时返回 400（code `40001`）。
同一时刻只允许一个进行中的 `update`／`pull`／`restart` 任务（`pending` 或 `running`），冲突返回 409（code `40901`，`data.existing_job_id` 等）。
任务只保存在内存中，保留最近 20 条，进程重启后丢失。

### `needs_up` 判据

对每个请求的服务：

- 从 `docker compose config --format json` 取该服务的 `image` 引用；
- 若服务没有运行中的容器（`docker compose ps -q <svc>` 为空），或任一运行容器的 `.Image` 不等于 `docker image inspect <image> -f '{{.Id}}'`（本地 tag 当前指向的镜像），则需要 up（`needs_up`）；
- 无法得出结论（compose config 失败、仅 `build` 无 `image`、本地无该镜像或 inspect 失败、读取运行容器失败）的服务进入 `uncertain`，不会被 up。

pull 前后的本地镜像 ID 对比只写入日志，不参与判定，因此“已 pull 但上次未 up（或 up 失败）”的服务再次 `update` 时仍会被 up。

### `POST /update`

`pull: true`（默认）先执行 `docker compose pull <services…>`，然后按判据对 `needs_up` 中的服务执行 `docker compose up -d --no-deps <needs_up…>`；`pull: false` 跳过拉取，只按判据 up。

```json
{"services": ["trader"], "pull": false}
```

创建成功：

```json
{
  "code": 200,
  "message": "更新任务已创建，正在后台执行",
  "data": {"job_id": "51bdb72b-…", "services": ["trader"], "action": "update"}
}
```

任务结果（`GET /jobs/:id` 或 `GET /jobs/latest?service=trader`）：

```json
{
  "code": 200,
  "message": "更新已完成",
  "data": {
    "id": "51bdb72b-…",
    "services": ["trader"],
    "action": "update",
    "status": "succeeded",
    "needs_up": ["trader"],
    "upped": ["trader"],
    "uncertain": [],
    "log_tail": "[updater] 本次更新目标服务: trader（pull=false）\n…",
    "created_at": "2026-11-05T08:00:00Z",
    "started_at": "2026-11-05T08:00:00Z",
    "finished_at": "2026-11-05T08:00:12Z"
  }
}
```

- `status`：`pending`、`running`、`succeeded`（`upped` 非空）、`skipped`（没有服务被 up：无需更新或全部 `uncertain`）、`failed`（命令失败或超时，`code` 为 `50010`，`data.error` 为错误摘要）。
- `needs_up`／`upped`／`uncertain` 总是数组；失败任务中保留失败前已得出的判定。

### `POST /pull`

只执行 `docker compose pull <services…>` 并按判据返回 `needs_up`、`uncertain`（`upped` 恒为空），任务 `action` 为 `pull`，成功时 `status` 为 `succeeded`。
调用方可据 `needs_up` 决定是否随后调用 `POST /update {"services":[…], "pull": false}`。

### `POST /check`

同步返回，不创建任务、不占用任务锁，可与进行中的任务并行：

```json
{"services": ["trader", "ib-gateway"]}
```

```json
{
  "code": 200,
  "message": "检查完成",
  "data": {
    "results": [
      {"service": "trader", "image": "ghcr.io/owner/trader:latest", "current_version": "v1.2.0", "remote_version": "v1.3.0", "update_available": true},
      {"service": "ib-gateway", "image": "ghcr.io/gnzsnz/ib-gateway:stable", "current_version": "10.50.1f", "remote_version": "", "update_available": null, "error_code": "remote_timeout"}
    ]
  }
}
```

- `update_available` 比较的是**运行中容器**所用镜像与远程 digest（`docker buildx imagetools inspect <image>` 的 index／manifest digest）：经典镜像存储看本地镜像的 `RepoDigests`，containerd image store 下本地镜像 ID 即 index digest，两者都支持。服务有多个容器时，任一容器不是远程 digest 即为 `true`。
- 无法判定时 `update_available` 为 `null` 并给出 `error_code`，不表示“无更新”。稳定错误码：`compose_config_failed`、`image_ref_missing`、`not_running`（无运行容器）、`local_inspect_failed`、`remote_timeout`、`remote_auth_failed`、`remote_not_found`、`remote_unavailable`、`remote_invalid_response`。本地与远程都失败时优先报告本地错误码。
- `current_version`／`remote_version`：镜像 env 中有 `IB_GATEWAY_VERSION` 时取它，否则取 OCI label `org.opencontainers.image.version`；取不到时为空字符串。只读取这两个白名单字段，不返回镜像 Env 或其他配置。多平台镜像的远程版本取与 updater 相同平台（如 `linux/amd64`）的配置。

### `POST /restart`

行为不变：请求体可为空或 `{}`，`services` 字段被忽略；目标为 compose 服务 ∩ `ALLOWED_SERVICES` 的全部服务，白名单与 compose 无交集时返回 403（code `40301`）。

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

    created, err := c.Update(ctx, []string{"api"})
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

    restarted, err := c.Restart(ctx)
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

`Job` / `JobResult` 提供 `Succeeded()`、`Failed()`、`Skipped()`、`Pending()`、`Running()`、`Done()`、`InProgress()` 等方法，避免手写状态字符串判断；`Job.Action` 可用于区分 `update`、`pull` 与 `restart`；`Job.NeedsUp`／`Upped`／`Uncertain` 为结构化结果。SDK 目前只覆盖 `/health`、`/update`、`/restart`、`/jobs/:id`。任务执行失败时服务端可能返回 HTTP 200 且 JSON `code` 为 `50010`，SDK 仍以 `res.Failed()` 与 `res.Code == client.CodeJobExecutionError` 表示。

## 构建与本地运行

```bash
go build -o updater ./cmd/updater
PORT=8080 COMPOSE_PROJECT_DIR=/path/to/compose ./updater
```

## Docker 镜像与部署

```bash
docker build -t updater:local .
```

镜像基于 `docker:29-cli`（含 compose 与 buildx 插件），以 root 运行。参考 [docker-compose.example.yml](docker-compose.example.yml)。

### 同路径挂载

把部署目录以**宿主上的同一绝对路径**只读挂入 updater，并令 `COMPOSE_PROJECT_DIR` 等于该路径：

```yaml
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ${DEPLOY_DIR:?}:${DEPLOY_DIR}:ro
    environment:
      COMPOSE_PROJECT_DIR: ${DEPLOY_DIR}
```

compose 文件中的相对路径（bind 卷、`secrets` 文件、`env_file`）由 updater 内的 docker compose 解析后交给宿主 dockerd，只有容器内路径与宿主路径一致时才能指向正确的宿主文件。
项目名由 compose 文件的 `name:` 或目录名决定，同路径挂载时与宿主上直接运行 `docker compose` 一致。

注意：

- updater 内运行的 docker compose 会用 updater 进程的环境变量做插值，且优先于项目目录下的 `.env`。因此被管理的 compose 文件**不得**引用与 updater 自身环境变量同名的变量：`PORT`、`COMPOSE_PROJECT_DIR`、`COMPOSE_PROJECT_DIRECTORY`、`ALLOWED_SERVICES`、`UPDATE_TIMEOUT`，以及 `docker:cli` 镜像内置的变量（`DOCKER_` 开头的变量如 `DOCKER_VERSION`、`DOCKER_TLS_CERTDIR`，以及 `PATH`、`HOME`）。
- 不要在 updater 中设置 `COMPOSE_FILE`、`COMPOSE_PROJECT_NAME`：它们会被 docker compose 直接读取，导致与宿主上操作的项目不一致。

### 私有 registry

updater 的 `pull` 与 `check` 使用 `/root/.docker/config.json` 中的凭据：

- 在宿主以 root 执行 `docker login <registry>`（例如 GHCR 只读 PAT：`read:packages`，`--password-stdin`），确认 `/root/.docker/config.json` 中有 `auths["<registry>"].auth`，且**没有** `credsStore`／`credHelpers`（updater 容器内没有对应的凭据助手）。
- 只把这个**单文件**只读挂入：`/root/.docker/config.json:/root/.docker/config.json:ro`。不要挂整个 `/root/.docker` 目录，buildx 需要在其中写入状态。
- 单文件 bind mount 绑定的是挂载时的 inode；`docker login` 会替换该文件，因此重新 login 后需执行 `docker compose up -d --force-recreate updater` 让新凭据生效。

## 说明

- 任务状态仅保存在内存中（最近 20 条），进程重启后丢失。
- `/check` 的远程查询需要 updater 能访问 registry；失败时结果为 `null` 而不是“无更新”。

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
- `POST /update` — 请求体 `{"service":"服务名"}`，成功返回 `202`，响应中含 `job_id`
- `GET /jobs/:id` — 查询任务；`message` 字段说明结果（例如已跳过重启）

同一 Compose **服务名**在任意时刻只允许存在一个进行中的任务（`pending` 或 `running`）。冲突时返回 `409`，响应中带
`existing_job_id`。

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

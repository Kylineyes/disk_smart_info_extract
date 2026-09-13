# 开发分支：`feature/cli-observability`

## 目标

在 NVMe 解析器与 PostgreSQL Store 完成后，实现最终命令行程序、`slog` 日志组件、超时管理和端到端错误处理。

目标调用：

文件模式：

```sh
smart-log-importer -c db.yaml -f /path/to/20260831.log
```

设备模式：

```sh
smart-log-importer -c db.yaml -d /dev/nvme1n1
```

## 参数

使用 Go 标准库 `flag`，不引入 Cobra。支持：

| 参数 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-c` | 是 | 无 | `db.yaml` 的路径。 |
| `-f` | 与 `-d` 二选一 | 无 | 单个 smartctl NVMe 文本日志文件路径，扩展名不受限制。 |
| `-d` | 与 `-f` 二选一 | 无 | NVMe 设备路径；程序直接调用 `smartctl --all <device>` 并解析标准输出。 |
| `-log-level` | 否 | `info` | `debug`、`info`、`warn`、`error`。 |
| `-log-format` | 否 | `text` | `text` 或 `json`。 |

`-f` 和 `-d` 必须且只能提供一个。两者同时提供或都未提供时，向 `stderr` 输出用法和错误并立即结束，不读取配置、不调用 `smartctl`、不连接数据库。设备模式要求运行主机安装 `smartctl`，当前用户对目标设备具有读取权限；命令输出仅在内存中传给解析器。

缺少必填参数、出现未知参数或提供非法日志级别/格式时，向 `stderr` 输出简洁用法与错误，并以非零状态结束。不可将用法错误误报为数据库失败。

## `internal/applog` 实现

在 `internal/applog/logger.go` 提供明确、可测试的构造函数，例如：

```go
func New(out io.Writer, level string, format string) (*slog.Logger, error)
func SerialSuffix(serial string) string
```

规范：

- `text` 使用 `slog.NewTextHandler`，`json` 使用 `slog.NewJSONHandler`；
- 默认向 `os.Stderr` 写日志；
- 不配置或修改全局默认 `slog` logger；
- `SerialSuffix` 仅返回后四位；序列号长度少于四位时按原样返回；
- 单元测试应覆盖非法级别、非法格式、JSON 可解码性、Text 输出和序列号脱敏。

## 主程序流程

建议将逻辑放在独立 `run(args []string, stdout, stderr io.Writer) error` 函数中，`main` 仅调用 `run` 并处理退出码。这样无需启动子进程即可测试参数与输出。

流程：

```text
解析参数
  ↓
初始化 slog logger
  ↓
记录导入开始（不记录敏感配置）
  ↓
读取/校验 db.yaml
  ↓
选择输入来源
  ├─ -f：读取 smartctl NVMe 日志文件
  └─ -d：在有超时的 context 下直接执行 smartctl --all <device>
  ↓
解析 NVMe smartctl 文本为 model.SmartLog
  ↓
建立 Store
  ↓
Initialize（建表和索引）
  ↓
Upsert 一条快照
  ↓
关闭 Store
  ↓
stdout 输出成功摘要，返回 nil
```

使用 `context.WithTimeout` 为连接、建表和单条写入设置合理的总体超时；在退出前总是关闭 Store。错误路径必须保留根因，并由 `main` 返回非零退出码。

## 输出规范

成功摘要写 `stdout`，只含非敏感字段：

```text
imported smart log: date=2026-08-31 model="aigo NVMe SSD P7000Z 4TB" serial="...1356"
```

正常过程与错误写 `stderr`。建议记录的事件：

- `import started`；
- `database config loaded`；
- `smart log parsed`；
- `database connected`；
- `database initialized`；
- `smart log upserted`；
- `import failed`；
- 包含总耗时的结束事件。

每条事件携带匹配场景的结构化字段：`component`、`file`、`table`、`snapshot_date`、`model`、`serial_suffix`、`duration`。

严禁输出：密码、数据库 DSN 或连接 URL、完整 YAML、原始 SMART 文本、完整序列号、或所有 SQL 参数。

## 验收标准

```sh
gofmt -w cmd internal/applog
go test ./cmd/smart-log-importer/... ./internal/applog/... ./...
git diff --check
```

至少测试：

- `-c` / `-f` / `-d` 参数组合错误时返回非零退出码，且不会开始导入；`-d` 与 `-f` 同时出现时必须明确提示二者互斥；
- 合法文件参数和合法设备参数都能正确进入编排流程；
- 设备模式调用 `smartctl --all`，命令失败或超时返回非零错误且不继续访问数据库；
- 设备模式无日期文件名时能从 `Local Time is:` 得到快照日期；
- 成功结果只写 stdout；
- 日志写 stderr；
- 配置、解析、Store 初始化或 UPSERT 失败时返回非零错误且不输出成功摘要；
- 任何输出中都不含配置密码、完整序列号或完整原始 SMART 文本。

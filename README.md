# disk_smart_info_extract

`disk_smart_info_extract` 是一个 Go 编写的命令行工具，用于将 NVMe SSD 的 `smartctl` 文本日志转换为结构化 SMART 数据，并保存到数据库中。

项目面向周期性采集场景，例如通过 cron 或 systemd 定期执行 `smartctl --all /dev/nvme0`，然后将每次采集的健康快照导入 PostgreSQL，供后续查询、告警和趋势分析使用。

> 当前工程已完成基础结构、领域数据模型、配置模板和依赖准备；SMART 解析、命令行编排、数据库写入等功能正在开发中，当前版本尚不能实际导入数据。

## 当前支持范围

首期目标为解析 NVMe 的 `smartctl --all` 或 `smartctl -a` 文本输出，日志中可提取：

- 型号、序列号、固件版本、容量和 NVMe 版本；
- SMART 总体健康状态和 Critical Warning；
- 温度、备用空间、寿命消耗百分比；
- 累计读写数据单位、主机读写命令、控制器忙碌时间；
- 通电次数、通电小时数、异常断电次数；
- 介质/数据完整性错误、错误日志条目及温度传感器信息；
- 原始 `smartctl` 文本，供审计和重新解析。

**不支持** ATA/SATA HDD/SSD 的 SMART 属性表。

## 依赖

- Go `1.26.8` 或兼容版本；
- PostgreSQL（当前目标数据库）；
- `smartctl`（由 smartmontools 提供，用于在采集主机生成 SMART 日志）；
- Go 依赖：
  - `github.com/jackc/pgx/v5`；
  - `gopkg.in/yaml.v3`。

## 配置数据库

复制配置模板：

```sh
cp db.yaml.example db.yaml
chmod 600 db.yaml
```

编辑 `db.yaml`：

```yaml
type: postgres
host: <host>
port: <port>
user: <user>
password: <password>
database: <database>
table: <table>
```

## 使用方式

功能完成后，程序的调用方式如下：

```sh
go run ./cmd/smart-log-importer \
  -c ./db.yaml \
  -f /path/to/19260817.log
```

参数说明：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `-c` | 是 | 数据库 YAML 配置文件路径。 |
| `-f` | 是 | 单个 `smartctl` NVMe 文本日志路径；扩展名不限。 |
| `-log-level` | 否 | `debug`、`info`、`warn`、`error`；默认 `info`。 |
| `-log-format` | 否 | `text` 或 `json`；默认 `text`。 |

日志的生成示例：

```sh
sudo smartctl --all /dev/nvme0 > "./nvme/$(date +%Y%m%d).log"
```

## 开发

下载模块并检查工程：

```sh
go mod download
gofmt -w cmd internal
go test ./...
```

## 项目结构

```text
.
├── cmd/smart-log-importer/       # 命令行入口
├── internal/
│   ├── applog/                   # slog 日志组件
│   ├── config/                   # db.yaml 读取与校验
│   ├── model/                    # SMART 领域结构体
│   ├── parser/                   # smartctl 文本解析器
│   └── store/                    # PostgreSQL / MySQL 存储抽象和实现
├── testdata/                     # 脱敏 SMART 日志测试夹具
├── db.yaml.example               # 可提交的数据库配置示例
├── CLAUDE.md                     # 面向项目开发者和 AI 的长期约定
└── go.mod
```

## 许可证

本项目使用 [MIT License](LICENSE)。

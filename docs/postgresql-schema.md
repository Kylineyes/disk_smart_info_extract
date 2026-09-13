# PostgreSQL 数据表设计

本文件定义 PostgreSQL 后端创建的逻辑表结构。实际实现会在校验 `db.yaml.table` 为简单标识符后安全引用该表名；下方以 `smart_log` 作为示例。

## 建表 SQL

```sql
CREATE TABLE IF NOT EXISTS smart_log (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, -- 记录主键，由数据库自动递增生成。
    snapshot_date DATE NOT NULL,                        -- 本次 SMART 快照所属日期；优先从 YYYYMMDD 文件名解析。
    source_file TEXT NOT NULL,                          -- 导入时的源日志文件路径，仅用于追溯，不参与唯一性判断。
    local_time_raw TEXT,                                -- smartctl 输出的原始 "Local Time is" 内容，保留设备主机时区文本。
    smartctl_version TEXT,                              -- 日志第一行中的 smartctl 版本、构建与运行平台信息。

    model VARCHAR(512) NOT NULL,                        -- NVMe Model Number，设备型号。
    serial VARCHAR(255) NOT NULL,                       -- NVMe Serial Number，设备序列号。
    firmware_version TEXT,                              -- NVMe Firmware Version，设备固件版本。
    pci_vendor_subsystem_id TEXT,                       -- PCI Vendor/Subsystem ID，例如 0x1e4b。
    ieee_oui_identifier TEXT,                           -- IEEE OUI Identifier，控制器厂商标识。
    total_nvm_capacity_bytes NUMERIC(39, 0),            -- Total NVM Capacity，设备总容量，单位：字节。
    nvm_version TEXT,                                   -- NVMe 协议版本，例如 2.0。
    namespace_count INTEGER,                            -- Number of Namespaces，命名空间数量。
    namespace_capacity_bytes NUMERIC(39, 0),            -- Namespace 1 Size/Capacity，首个命名空间容量，单位：字节。
    formatted_lba_bytes INTEGER,                        -- Namespace 1 Formatted LBA Size，逻辑块大小，单位：字节。
    namespace_eui64 TEXT,                               -- Namespace 1 IEEE EUI-64，命名空间唯一标识。

    overall_health TEXT NOT NULL,                        -- SMART 总体自评结果，例如 PASSED 或 FAILED。
    critical_warning TEXT,                              -- NVMe Critical Warning 位图原文，例如 0x00。
    temperature_c SMALLINT,                             -- 复合温度（Temperature），单位：摄氏度。
    available_spare_percent SMALLINT,                   -- Available Spare，剩余可用备用空间百分比。
    available_spare_threshold_percent SMALLINT,         -- Available Spare Threshold，备用空间告警阈值，百分比。
    percentage_used SMALLINT,                           -- Percentage Used，设备寿命消耗百分比；可能超过 100。
    data_units_read NUMERIC(39, 0),                     -- Data Units Read，累计读取的 NVMe 数据单位数（每单位 512,000 字节）。
    data_units_written NUMERIC(39, 0),                  -- Data Units Written，累计写入的 NVMe 数据单位数（每单位 512,000 字节）。
    host_read_commands NUMERIC(39, 0),                  -- Host Read Commands，主机累计读命令数。
    host_write_commands NUMERIC(39, 0),                 -- Host Write Commands，主机累计写命令数。
    controller_busy_time_minutes NUMERIC(39, 0),        -- Controller Busy Time，控制器忙碌累计时间，单位：分钟。
    power_cycles NUMERIC(39, 0),                        -- Power Cycles，累计上电/断电循环次数。
    power_on_hours NUMERIC(39, 0),                      -- Power On Hours，累计通电时长，单位：小时。
    unsafe_shutdowns NUMERIC(39, 0),                    -- Unsafe Shutdowns，未按正常流程断电的累计次数。
    media_data_integrity_errors NUMERIC(39, 0),         -- Media and Data Integrity Errors，介质或数据完整性累计错误数。
    error_information_log_entries NUMERIC(39, 0),       -- Error Information Log Entries，错误信息日志累计条目数。
    warning_composite_temp_time NUMERIC(39, 0),         -- Warning Comp. Temperature Time，复合温度处于告警区间的累计时间（控制器报告单位）。
    critical_composite_temp_time NUMERIC(39, 0),        -- Critical Comp. Temperature Time，复合温度处于临界区间的累计时间（控制器报告单位）。
    temperature_sensor_1_c SMALLINT,                    -- Temperature Sensor 1，传感器 1 温度，单位：摄氏度。
    temperature_sensor_2_c SMALLINT,                    -- Temperature Sensor 2，传感器 2 温度，单位：摄氏度；设备未报告时为 NULL。
    thermal_temp_1_transition_count NUMERIC(39, 0),     -- Thermal Temp. 1 Transition Count，温度管理阈值 1 转换累计次数。
    thermal_temp_1_total_time NUMERIC(39, 0),           -- Thermal Temp. 1 Total Time，温度管理阈值 1 生效累计时间（控制器报告单位）。
    no_errors_logged BOOLEAN NOT NULL DEFAULT FALSE,    -- Error Information 日志是否显示 "No Errors Logged"。

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 首次插入记录的数据库时间。
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP  -- 最近一次 UPSERT 更新的数据库时间。
);
```

## 索引

```sql
-- 主键自动创建 id 的唯一 B-tree 索引。
-- 同一盘在同一快照日期只能保留一条记录。
CREATE UNIQUE INDEX IF NOT EXISTS uq_smart_log_date_model_serial
    ON smart_log (snapshot_date, model, serial);
```

不要额外创建 `(id, snapshot_date, model, serial)` 联合索引：`id` 已经唯一，把它作为联合索引首列会让后续三列无法有效支持按日期、型号和序列号查询。主键索引加上述三列唯一索引，才是正确且高效的结构。

## 幂等写入语义

存储层使用参数化 SQL 写入。冲突键为 `(snapshot_date, model, serial)`：

```sql
INSERT INTO smart_log (...)
VALUES (...)
ON CONFLICT (snapshot_date, model, serial)
DO UPDATE SET
    source_file = EXCLUDED.source_file,
    local_time_raw = EXCLUDED.local_time_raw,
    smartctl_version = EXCLUDED.smartctl_version,
    -- 其余设备字段与 SMART 字段均同步更新
    updated_at = CURRENT_TIMESTAMP;
```

首次导入插入新记录，重复导入同一盘同一天的快照则更新已有记录。`created_at` 保持初次插入的值，`updated_at` 每次更新。

## 数据约束说明

- `NUMERIC(39,0)` 用于全部可能超过有符号 `BIGINT` 范围的 NVMe 累计计数器；传参时使用 `big.Int.String()` 的十进制文本。
- 可选数值字段未出现在输入日志中时写 `NULL`，不能以 `0` 代替未知值。
- `model` 和 `serial` 采用 `VARCHAR(512)` / `VARCHAR(255)`，在跨 PostgreSQL/MySQL 支持时可建立完全相同的唯一键。
- `SmartLog.RawLog` 仅在当前进程内保留原始日志，不写入 PostgreSQL；应用日志同样不得输出该字段。

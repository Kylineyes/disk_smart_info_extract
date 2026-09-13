// Package model contains the domain types used by the importer.
package model

import (
	"math/big"
	"time"
)

// SmartLog is one parsed NVMe SMART snapshot.
type SmartLog struct {
	ID           int64
	SnapshotDate time.Time
	Device       DeviceInfo
	Health       HealthInfo
}

// DeviceInfo contains stable NVMe identity and namespace fields.
type DeviceInfo struct {
	Model                  string
	Serial                 string
	FirmwareVersion        string
	IEEOUIIdentifier       string
	TotalNVMCapacityBytes  *big.Int
	NVMVersion             string
	NamespaceCount         int
	NamespaceCapacityBytes *big.Int
	FormattedLBABytes      int
	NamespaceEUI64         string
}

// HealthInfo contains the NVMe SMART/Health log fields.
type HealthInfo struct {
	OverallHealth                  string
	CriticalWarning                string
	TemperatureC                   *int
	AvailableSparePercent          *int
	AvailableSpareThresholdPercent *int
	PercentageUsed                 *int
	DataUnitsRead                  *big.Int
	DataUnitsWritten               *big.Int
	HostReadCommands               *big.Int
	HostWriteCommands              *big.Int
	ControllerBusyTimeMinutes      *big.Int
	PowerCycles                    *big.Int
	PowerOnHours                   *big.Int
	UnsafeShutdowns                *big.Int
	MediaDataIntegrityErrors       *big.Int
	ErrorInformationLogEntries     *big.Int
	WarningCompositeTempTime       *big.Int
	CriticalCompositeTempTime      *big.Int
	TemperatureSensor1C            *int
	TemperatureSensor2C            *int
	ThermalTemp1TransitionCount    *big.Int
	ThermalTemp1TotalTime          *big.Int
}

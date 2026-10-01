package platform

import (
	"context"
	"net"
	"time"
)

// 本文件提供各平台共用的"未实现"占位后端。
//
// M0 阶段三平台统一使用它们，保证任何时刻 go build ./... 都能通过；
// 随着里程碑推进，各平台在自己的文件中把这些占位逐个替换为真实实现。
// 详见 docs/PLAN.md 中 R2 的处置方式。

// ---------------------------------------------------------------------------
// Firewall
// ---------------------------------------------------------------------------

type unsupportedFirewall struct{ Unsupported }

func newUnsupportedFirewall(reason string) Firewall {
	return &unsupportedFirewall{Unsupported{Name: "firewall", Reason: reason}}
}

func (f *unsupportedFirewall) Inspect(context.Context) ([]Rule, error) {
	return nil, unimplemented("firewall.Inspect")
}

func (f *unsupportedFirewall) Plan(context.Context, []Rule) (Change, error) {
	return Change{}, unimplemented("firewall.Plan")
}

func (f *unsupportedFirewall) Apply(context.Context, Change) error {
	return unimplemented("firewall.Apply")
}

func (f *unsupportedFirewall) Rollback(context.Context, Change) error {
	return unimplemented("firewall.Rollback")
}

// ---------------------------------------------------------------------------
// ServiceManager
// ---------------------------------------------------------------------------

type unsupportedServiceManager struct{ Unsupported }

func newUnsupportedServiceManager(reason string) ServiceManager {
	return &unsupportedServiceManager{Unsupported{Name: "service_manager", Reason: reason}}
}

func (s *unsupportedServiceManager) Install(context.Context, ServiceConfig) error {
	return unimplemented("service_manager.Install")
}

func (s *unsupportedServiceManager) Uninstall(context.Context) error {
	return unimplemented("service_manager.Uninstall")
}

func (s *unsupportedServiceManager) Status(context.Context) (ServiceStatus, error) {
	return ServiceUnknown, unimplemented("service_manager.Status")
}

func (s *unsupportedServiceManager) Start(context.Context) error {
	return unimplemented("service_manager.Start")
}

func (s *unsupportedServiceManager) Stop(context.Context) error {
	return unimplemented("service_manager.Stop")
}

// ---------------------------------------------------------------------------
// IPMonitor
// ---------------------------------------------------------------------------

type unsupportedIPMonitor struct{ Unsupported }

func newUnsupportedIPMonitor(reason string) IPMonitor {
	return &unsupportedIPMonitor{Unsupported{Name: "ip_monitor", Reason: reason}}
}

func (m *unsupportedIPMonitor) Snapshot(context.Context) ([]InterfaceAddrs, error) {
	return nil, unimplemented("ip_monitor.Snapshot")
}

func (m *unsupportedIPMonitor) Watch(context.Context) (<-chan AddrEvent, error) {
	return nil, unimplemented("ip_monitor.Watch")
}

// ---------------------------------------------------------------------------
// SecretStore
// ---------------------------------------------------------------------------

type unsupportedSecretStore struct{ Unsupported }

func newUnsupportedSecretStore(reason string) SecretStore {
	return &unsupportedSecretStore{Unsupported{Name: "secret_store", Reason: reason}}
}

func (s *unsupportedSecretStore) Encrypt(context.Context, []byte) ([]byte, error) {
	return nil, unimplemented("secret_store.Encrypt")
}

func (s *unsupportedSecretStore) Decrypt(context.Context, []byte) ([]byte, error) {
	return nil, unimplemented("secret_store.Decrypt")
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

// unsupportedTransport 是一个不提供任何本地通道的实现。
//
// 生产代码在各平台都已具备真实传输（见 transport_windows.go / transport_unix.go），
// 保留它有两个用途：作为"传输层彻底不可用"的测试替身，
// 以及为将来可能出现的、既无命名管道也无 Unix socket 的平台兜底。
type unsupportedTransport struct{ Unsupported }

func newUnsupportedTransport(reason string) Transport {
	return &unsupportedTransport{Unsupported{Name: "transport", Reason: reason}}
}

func (t *unsupportedTransport) Listen(context.Context, string) (net.Listener, error) {
	return nil, unimplemented("transport.Listen")
}

// ---------------------------------------------------------------------------
// LowPortBinder
// ---------------------------------------------------------------------------

// permissiveLowPortBinder 用于 Windows / macOS —— 这两个平台不限制普通进程绑定低端口。
type permissiveLowPortBinder struct {
	backend string
}

func (b permissiveLowPortBinder) CanBindLowPorts() bool { return true }

func (b permissiveLowPortBinder) Describe() ImplState {
	return ImplState{Available: true, Backend: b.backend}
}

// restrictedLowPortBinder 用于 Linux —— 需要 CAP_NET_BIND_SERVICE。
type restrictedLowPortBinder struct {
	bindLow bool
	note    string
}

func (b restrictedLowPortBinder) CanBindLowPorts() bool { return b.bindLow }

func (b restrictedLowPortBinder) Describe() ImplState {
	return ImplState{Available: true, Backend: "cap_net_bind_service", Note: b.note}
}

// ---------------------------------------------------------------------------
// 公共小工具
// ---------------------------------------------------------------------------

// nowFunc 便于测试替换时间源。
var nowFunc = time.Now

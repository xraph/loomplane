//go:build linux

package firecracker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/containernetworking/cni/libcni"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/firecracker-microvm/firecracker-go-sdk/vsock"
	"golang.org/x/sys/unix"
)

type linuxDriver struct {
	config   HostConfig
	store    *Store
	key      []byte
	machines sync.Map
}
type managedMachine struct {
	machine    *fc.Machine
	shutdown   sync.Once
	done       chan struct{}
	cleanupErr error
}

type runtimeReceipt struct {
	ID, IP, Tap, NetNS string
	PID                int
	StartTime          string
}

func NewDriver(cfg HostConfig, store *Store, key []byte) (MachineDriver, error) {
	if os.Geteuid() != 0 || len(key) != 32 || !identifier.MatchString(cfg.HostID) || cfg.UIDBase < 10000 || cfg.UIDBase > 2147479551 || cfg.Limits.VMs < 1 || cfg.Limits.VMs > 4096 {
		return nil, ErrInvalid
	}
	subnet, e := netip.ParsePrefix(cfg.Subnet)
	if e != nil || !subnet.Addr().Is4() || !subnet.Addr().IsPrivate() || subnet.Bits() < 16 || subnet.Bits() > 28 {
		return nil, ErrInvalid
	}
	for _, p := range []string{cfg.RuntimeDir, cfg.CNIBinDir, cfg.CNICacheDir} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return nil, ErrInvalid
		}
	}
	if checkErr := os.MkdirAll(cfg.RuntimeDir, 0o700); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := prepareStateDirectory(cfg.CNICacheDir); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := rootOwnedPath(cfg.RuntimeDir); checkErr != nil {
		return nil, checkErr
	}
	if cfg.Limits.VCPUs < 1 || cfg.Limits.MemoryMiB < 256 || cfg.Limits.DiskMiB < 256 || len(cfg.DNS) == 0 || len(cfg.DNS) > 4 || len(cfg.DeniedCIDRs) == 0 {
		return nil, ErrInvalid
	}
	for _, v := range cfg.DNS {
		ip, e := netip.ParseAddr(v)
		if e != nil || !ip.Is4() || ip.IsPrivate() || !ip.IsGlobalUnicast() {
			return nil, ErrInvalid
		}
	}
	if _, e := networkPolicy("tap0", "10.0.0.2", nil, cfg.EgressCIDRs, nil, cfg.DeniedCIDRs...); e != nil {
		return nil, e
	}
	d := &linuxDriver{config: cfg, store: store, key: key}
	if _, err := d.Check(context.Background()); err != nil {
		return nil, err
	}
	return d, nil
}
func checkAsset(a Asset) error {
	if !filepath.IsAbs(a.Path) || len(a.SHA256) != 64 {
		return ErrInvalid
	}
	if checkErr := rootOwnedPath(a.Path); checkErr != nil {
		return checkErr
	}
	f, err := os.Open(a.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o022 != 0 {
		return ErrInvalid
	}
	sum := sha256.New()
	if _, err = io.Copy(sum, f); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != a.SHA256 {
		return ErrUnavailable
	}
	return nil
}
func (d *linuxDriver) Check(ctx context.Context) (Capabilities, error) {
	if checkErr := ctx.Err(); checkErr != nil {
		return Capabilities{}, checkErr
	}
	kvm, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return Capabilities{}, ErrUnavailable
	}
	defer kvm.Close()
	version, _, errno := unix.Syscall(unix.SYS_IOCTL, kvm.Fd(), 0xAE00, 0)
	if errno != 0 || version != 12 {
		return Capabilities{}, ErrUnavailable
	}
	for _, a := range []Asset{d.config.Firecracker, d.config.Jailer, d.config.Kernel, d.config.RootFS} {
		if checkErr := checkAsset(a); checkErr != nil {
			return Capabilities{}, checkErr
		}
	}
	for _, name := range []string{"ptp", "host-local", "tc-redirect-tap"} {
		if checkErr := checkAsset(Asset{Path: filepath.Join(d.config.CNIBinDir, name), SHA256: d.config.CNIHashes[name]}); checkErr != nil {
			return Capabilities{}, checkErr
		}
	}
	for _, p := range []string{"/sys/fs/cgroup/cgroup.controllers"} {
		if _, err = os.Stat(p); err != nil {
			return Capabilities{}, ErrUnavailable
		}
	}
	for _, bin := range []string{"nft", "ip", "mkfs.ext4"} {
		path, e := exec.LookPath(bin)
		if e != nil {
			return Capabilities{}, ErrUnavailable
		}
		resolved, e := filepath.EvalSymlinks(path)
		if e != nil {
			return Capabilities{}, e
		}
		if err = rootOwnedPath(resolved); err != nil {
			return Capabilities{}, ErrUnavailable
		}
	}
	raw, err := json.Marshal(struct {
		Assets              []Asset
		CNI                 map[string]string
		Subnet              string
		DNS, Egress, Denied []string
		UID                 int
		Limits              Limits
	}{[]Asset{d.config.Firecracker, d.config.Jailer, d.config.Kernel, d.config.RootFS}, d.config.CNIHashes, d.config.Subnet, d.config.DNS, d.config.EgressCIDRs, d.config.DeniedCIDRs, d.config.UIDBase, d.config.Limits})
	if err != nil {
		return Capabilities{}, err
	}
	sum := sha256.Sum256(raw)
	identity := hex.EncodeToString(sum[:])
	ingress := ""
	if d.config.HTTPSListen != "" && d.config.HTTPListen != "" {
		ingress = d.config.AdvertiseHost
	}
	return Capabilities{IngressAddress: ingress, Version: ProtocolVersion, HostID: d.config.HostID, Architecture: runtime.GOARCH, RuntimeIdentity: identity, Isolation: true, Limits: d.config.Limits}, nil
}
func (d *linuxDriver) dir(vm VM) string {
	return filepath.Join(d.config.RuntimeDir, filepath.Base(d.config.Firecracker.Path), vm.ID, "root")
}
func (d *linuxDriver) socket(vm VM) string { return filepath.Join(d.dir(vm), "api.socket") }
func (d *linuxDriver) receiptPath(vm VM) string {
	return filepath.Join(d.config.RuntimeDir, "receipts", vm.ID+".json")
}
func (d *linuxDriver) readReceipt(vm VM) (runtimeReceipt, error) {
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(vm.ID) || vm.Slot < 1 || vm.Slot > 4096 {
		return runtimeReceipt{}, ErrInvalid
	}
	var out runtimeReceipt
	raw, err := os.ReadFile(d.receiptPath(vm)) //nolint:gosec // VM IDs are validated 32-character hex ownership keys; paths stay in the administrator-owned runtime directory.
	if err != nil {
		return out, err
	}
	if checkErr := Decode(bytes.NewReader(raw), &out, 4096); checkErr != nil {
		return out, checkErr
	}
	if out.ID != vm.ID {
		return out, ErrConflict
	}
	return out, nil
}
func processStart(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	_, tail, ok := strings.Cut(string(raw), ") ")
	if !ok {
		return "", ErrConflict
	}
	fields := strings.Fields(tail)
	if len(fields) < 20 {
		return "", ErrConflict
	}
	if fields[0] == "Z" {
		return "", os.ErrNotExist
	}
	return fields[19], nil
}
func (d *linuxDriver) Inspect(ctx context.Context, vm VM) (bool, error) {
	receipt, err := d.readReceipt(vm)
	if errors.Is(err, os.ErrNotExist) {
		if _, e := os.Stat(d.socket(vm)); e == nil { //nolint:gosec // VM IDs are validated 32-character hex ownership keys; paths stay in the administrator-owned runtime directory.
			return false, ErrConflict
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if receipt.PID == 0 {
		procs, e := os.ReadFile(filepath.Join(string(filepath.Separator), "sys", "fs", "cgroup", "loomplane", vm.ID, "cgroup.procs"))
		if errors.Is(e, os.ErrNotExist) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		ids := strings.Fields(string(procs))
		if len(ids) == 0 {
			return false, nil
		}
		if len(ids) != 1 {
			return false, ErrConflict
		}
		receipt.PID, e = strconv.Atoi(ids[0])
		if e != nil {
			return false, e
		}
		receipt.StartTime, e = processStart(receipt.PID)
		if e != nil {
			return false, e
		}
	}
	start, err := processStart(receipt.PID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || start != receipt.StartTime {
		return false, ErrConflict
	}
	var info struct {
		ID         string `json:"id"`
		State      string `json:"state"`
		VMMVersion string `json:"vmm_version"`
		AppName    string `json:"app_name"`
	}
	if checkErr := d.api(ctx, vm, http.MethodGet, "/", nil, &info); checkErr != nil {
		return false, checkErr
	}
	if info.ID != vm.ID {
		return false, ErrConflict
	}
	after, e := processStart(receipt.PID)
	if e != nil || after != receipt.StartTime {
		return false, ErrConflict
	}
	raw, e := json.Marshal(receipt)
	if e != nil {
		return false, e
	}
	if e := atomicFile(d.receiptPath(vm), raw); e != nil {
		return false, e
	}
	return true, nil
}
func (d *linuxDriver) api(ctx context.Context, vm VM, method, path string, input, out any) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", d.socket(vm))
	}}
	defer transport.CloseIdleConnections()
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, body)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrUnavailable
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, MaxControlBytes)).Decode(out)
}
func (d *linuxDriver) cni() (*libcni.NetworkConfigList, error) {
	raw, err := json.Marshal(map[string]any{"cniVersion": "1.0.0", "name": "loomplane-firecracker", "plugins": []any{map[string]any{"type": "ptp", "ipMasq": true, "ipam": map[string]any{"type": "host-local", "subnet": d.config.Subnet}, "dns": map[string]any{"nameservers": d.config.DNS}}, map[string]any{"type": "tc-redirect-tap"}}})
	if err != nil {
		return nil, err
	}
	return libcni.ConfListFromBytes(raw)
}
func (d *linuxDriver) Start(ctx context.Context, vm VM, s Spec) error {
	live, err := d.Inspect(ctx, vm)
	if err != nil {
		return err
	}
	if live {
		var status struct {
			State string `json:"state"`
		}
		if checkErr := d.api(ctx, vm, http.MethodGet, "/", nil, &status); checkErr != nil {
			return checkErr
		}
		if status.State == "Running" {
			return nil
		}
		if checkErr := d.Stop(ctx, vm); checkErr != nil {
			return checkErr
		}
	}
	if _, err = os.Stat(d.receiptPath(vm)); err == nil { //nolint:gosec // VM IDs are validated 32-character hex ownership keys; paths stay in the administrator-owned runtime directory.
		if checkErr := d.removeRuntime(ctx, vm, false); checkErr != nil {
			return checkErr
		}
	}
	if _, err = d.Check(ctx); err != nil {
		return err
	}
	assets := filepath.Join(d.config.RuntimeDir, "assets", vm.ID)
	if checkErr := os.MkdirAll(assets, 0o700); checkErr != nil {
		return checkErr
	}
	scratch := filepath.Join(assets, "scratch.ext4")
	_, diskErr := os.Stat(scratch)
	if diskErr != nil && !errors.Is(diskErr, os.ErrNotExist) {
		return diskErr
	}
	if errors.Is(diskErr, os.ErrNotExist) {
		f, fileErr := os.OpenFile(scratch, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if fileErr != nil {
			return fileErr
		}
		if err = f.Truncate(int64(s.DiskMiB) << 20); err != nil {
			_ = f.Close()
			return err
		}
		if checkErr := f.Close(); checkErr != nil {
			return checkErr
		}
		// Only an agent-created, empty disk is formatted on the host.
		if formatErr := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", scratch).Run(); formatErr != nil { //nolint:gosec // Only the agent-created empty scratch file is formatted; no shell is used.
			return formatErr
		}
	}
	cni, err := d.cni()
	if err != nil {
		return err
	}
	uid, node := d.config.UIDBase+vm.Slot, 0
	if checkErr := os.Chown(scratch, uid, uid); checkErr != nil {
		return checkErr
	}
	if checkErr := os.Chmod(scratch, 0o600); checkErr != nil {
		return checkErr
	}
	cpu, mem := int64(s.VCPUs), int64(s.MemoryMiB)
	yes, no := true, false
	rootID, dataID := "root", "scratch"
	cfg := fc.Config{Seccomp: fc.SeccompConfig{Enabled: true}, NetNS: filepath.Join(string(filepath.Separator), "var", "run", "netns", "loomplane-"+vm.ID), SocketPath: "api.socket", VMID: vm.ID, KernelImagePath: d.config.Kernel.Path, KernelArgs: "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda ro init=/sbin/init", Drives: []models.Drive{{DriveID: &rootID, PathOnHost: &d.config.RootFS.Path, IsRootDevice: &yes, IsReadOnly: &yes}, {DriveID: &dataID, PathOnHost: &scratch, IsRootDevice: &no, IsReadOnly: &no}}, MachineCfg: models.MachineConfiguration{VcpuCount: &cpu, MemSizeMib: &mem}, VsockDevices: []fc.VsockDevice{{ID: "guest", Path: "guest.vsock", CID: uint32(vm.Slot + 3)}}, NetworkInterfaces: fc.NetworkInterfaces{{CNIConfiguration: &fc.CNIConfiguration{NetworkConfig: cni, IfName: "veth0", BinPath: []string{d.config.CNIBinDir}, Args: [][2]string{{"IgnoreUnknown", "1"}, {"TC_REDIRECT_TAP_UID", strconv.Itoa(uid)}, {"TC_REDIRECT_TAP_GID", strconv.Itoa(uid)}}, CacheDir: d.config.CNICacheDir}}}, JailerCfg: &fc.JailerConfig{UID: &uid, GID: &uid, NumaNode: &node, ID: vm.ID, ExecFile: d.config.Firecracker.Path, JailerBinary: d.config.Jailer.Path, ChrootBaseDir: d.config.RuntimeDir, Daemonize: true, ChrootStrategy: fc.NewNaiveChrootStrategy(d.config.Kernel.Path), CgroupVersion: "2", ParentCgroup: "loomplane", CgroupArgs: []string{fmt.Sprintf("memory.max=%d", int64(s.MemoryMiB+128)<<20), fmt.Sprintf("cpu.max=%d 100000", s.VCPUs*100000), "pids.max=128"}}} //nolint:gosec // Inspect validates the slot in [1,4096] before this uint32 conversion.
	machine, err := fc.NewMachine(context.Background(), cfg)
	if err != nil {
		return err
	}
	receipt := runtimeReceipt{ID: vm.ID, NetNS: filepath.Join(string(filepath.Separator), "var", "run", "netns", "loomplane-"+vm.ID)}
	if mkdirErr := os.MkdirAll(filepath.Dir(d.receiptPath(vm)), 0o700); mkdirErr != nil { //nolint:gosec // Receipt paths use validated VM IDs in the root-owned runtime directory.
		return mkdirErr
	}
	intent, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if checkErr := atomicFile(d.receiptPath(vm), intent); checkErr != nil {
		return checkErr
	}
	if nsErr := exec.CommandContext(ctx, "ip", "netns", "add", "loomplane-"+vm.ID).Run(); nsErr != nil { //nolint:gosec // Command arguments use the validated VM ID, never tenant command text.
		return nsErr
	}
	machine.Handlers.FcInit = machine.Handlers.FcInit.AppendAfter(fc.SetupNetworkHandlerName, fc.Handler{Name: "loomplane.NetworkPolicy", Fn: func(call context.Context, m *fc.Machine) error {
		nic := m.Cfg.NetworkInterfaces[0].StaticConfiguration
		if nic == nil || nic.IPConfiguration == nil {
			return ErrUnavailable
		}
		receipt = runtimeReceipt{ID: vm.ID, IP: nic.IPConfiguration.IPAddr.IP.String(), Tap: nic.HostDevName, NetNS: m.Cfg.NetNS}
		if e := d.installPolicy(call, vm, receipt); e != nil {
			return e
		}
		raw, e := json.Marshal(receipt)
		if e != nil {
			return e
		}
		return atomicFile(d.receiptPath(vm), raw)
	}})
	// Daemonized jailer retains VMs across host-agent exits. No request context owns VM lifetime.
	if checkErr := machine.Start(context.Background()); checkErr != nil {
		return checkErr
	}
	procs, err := os.ReadFile(filepath.Join(string(filepath.Separator), "sys", "fs", "cgroup", "loomplane", vm.ID, "cgroup.procs"))
	if err != nil {
		return err
	}
	ids := strings.Fields(string(procs))
	if len(ids) != 1 {
		return ErrConflict
	}
	receipt.PID, err = strconv.Atoi(ids[0])
	if err != nil {
		return err
	}
	receipt.StartTime, err = processStart(receipt.PID)
	if err != nil {
		return err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(d.receiptPath(vm)), 0o700); mkdirErr != nil { //nolint:gosec // Receipt paths use validated VM IDs in the root-owned runtime directory.
		return mkdirErr
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if checkErr := atomicFile(d.receiptPath(vm), raw); checkErr != nil {
		return checkErr
	}
	d.machines.Store(vm.ID, &managedMachine{machine: machine, done: make(chan struct{})})
	return d.refreshPeers(ctx, vm, receipt)
}
func atomicFile(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer func() { _ = os.Remove(temporary) }() //nolint:gosec // Temporary receipt was created by this function in the root-owned directory.
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if renameErr := os.Rename(temporary, path); renameErr != nil { //nolint:gosec // Both paths are agent-owned receipt files within the root-owned runtime directory.
		return renameErr
	}
	dir, err := os.Open(filepath.Dir(path)) //nolint:gosec // Sync the administrator-owned receipt directory after atomic replacement.
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (d *linuxDriver) Stop(ctx context.Context, vm VM) (err error) {
	if checkErr := d.refreshPeers(ctx, vm, runtimeReceipt{}, vm.ID); checkErr != nil {
		return checkErr
	}
	stopped := false
	defer func() {
		if !stopped {
			return
		}
		if value, ok := d.machines.Load(vm.ID); ok {
			managed, owned := value.(*managedMachine)
			if !owned {
				err = errors.Join(err, ErrConflict)
				return
			}
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			managed.shutdown.Do(func() {
				go func() {
					_ = managed.machine.Shutdown(cleanup) //nolint:errcheck // The process has exited; Wait captures SDK cleanup results.
					managed.cleanupErr = managed.machine.Wait(context.Background())
					close(managed.done)
				}()
			})
			select {
			case <-managed.done:
				// SDK cleanup runs once. A failed completed cleanup must allow the
				// durable journal's explicit CNI removal to retry on the next call.
				d.machines.Delete(vm.ID)
				err = errors.Join(err, managed.cleanupErr)
			case <-cleanup.Done():
				err = errors.Join(err, cleanup.Err())
			}
		}
	}()

	r, err := d.readReceipt(vm)
	if errors.Is(err, os.ErrNotExist) {
		stopped = true
		return nil
	}
	if err != nil {
		return err
	}
	if r.PID == 0 {
		live, e := d.Inspect(ctx, vm)
		if e != nil {
			return e
		}
		if !live {
			stopped = true
			return nil
		}
		r, err = d.readReceipt(vm)
		if err != nil {
			return err
		}
	}
	before, e := processStart(r.PID)
	if errors.Is(e, os.ErrNotExist) {
		stopped = true
		return nil
	}
	if e != nil || before != r.StartTime {
		return ErrConflict
	}
	fd, err := unix.PidfdOpen(r.PID, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	start, err := processStart(r.PID)
	if err != nil || start != r.StartTime {
		return ErrConflict
	}
	if err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrUnavailable
		case <-tick.C:
			if _, err = processStart(r.PID); errors.Is(err, os.ErrNotExist) {
				stopped = true
				return nil
			}
		}
	}
}
func (d *linuxDriver) Remove(ctx context.Context, vm VM) error { return d.removeRuntime(ctx, vm, true) }
func (d *linuxDriver) removeRuntime(ctx context.Context, vm VM, disk bool) error {
	live, err := d.Inspect(ctx, vm)
	if err != nil {
		return err
	}
	if live {
		return ErrConflict
	}
	r, err := d.readReceipt(vm)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		table := "loomplane_" + vm.ID
		if exec.CommandContext(ctx, "nft", "list", "table", "netdev", table).Run() == nil { //nolint:gosec // Command arguments use the validated VM ID, never tenant command text.
			if e := exec.CommandContext(ctx, "nft", "delete", "table", "netdev", table).Run(); e != nil { //nolint:gosec // Command arguments use the validated VM ID, never tenant command text.
				return e
			}
		}
		conf, e := d.cni()
		if e != nil {
			return e
		}
		cni := libcni.NewCNIConfigWithCacheDir([]string{d.config.CNIBinDir}, d.config.CNICacheDir, nil)
		if delErr := cni.DelNetworkList(ctx, conf, &libcni.RuntimeConf{ContainerID: vm.ID, NetNS: r.NetNS, IfName: "veth0", Args: [][2]string{{"IgnoreUnknown", "1"}, {"TC_REDIRECT_TAP_UID", strconv.Itoa(d.config.UIDBase + vm.Slot)}, {"TC_REDIRECT_TAP_GID", strconv.Itoa(d.config.UIDBase + vm.Slot)}}}); delErr != nil {
			return delErr
		}
		if e = unix.Unmount(r.NetNS, unix.MNT_DETACH); e != nil && !errors.Is(e, unix.EINVAL) && !errors.Is(e, unix.ENOENT) {
			return e
		}
		if e = os.Remove(r.NetNS); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	paths := []string{filepath.Dir(d.dir(vm))}
	if disk {
		paths = append(paths, filepath.Join(d.config.RuntimeDir, "assets", vm.ID))
	}
	for _, path := range paths {
		if removeErr := os.RemoveAll(path); removeErr != nil { //nolint:gosec // Paths are derived from validated VM IDs under the administrator-owned runtime directory.
			return removeErr
		}
	}
	if err = os.Remove(d.receiptPath(vm)); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // Receipt identity was validated by Inspect before cleanup.
		return err
	}
	return d.refreshPeers(ctx, vm, r)
}
func (d *linuxDriver) guestHTTP(vm VM) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return vsock.DialContext(ctx, filepath.Join(d.dir(vm), "guest.vsock"), GuestPort, vsock.WithRetryTimeout(30*time.Second))
	}}, Timeout: 31 * time.Minute}
}
func (d *linuxDriver) guestCall(ctx context.Context, vm VM, path string, body io.Reader) (GuestStatus, error) {
	client := d.guestHTTP(vm)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://guest/"+path, body)
	if err != nil {
		return GuestStatus{}, err
	}
	req.Header.Set("Authorization", "Bearer "+d.config.token(d.key, vm))
	req.Header.Set("Loomplane-VM", vm.ID)
	resp, err := client.Do(req)
	if err != nil {
		return GuestStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return GuestStatus{}, ErrUnavailable
	}
	var out GuestStatus
	err = Decode(resp.Body, &out, MaxControlBytes)
	return out, err
}
func (d *linuxDriver) Configure(ctx context.Context, vm VM, q Request) (GuestStatus, error) {
	network, e := d.networkValues(ctx, vm)
	if e != nil {
		return GuestStatus{}, e
	}
	q.Network = network
	raw, err := json.Marshal(q)
	if err != nil {
		return GuestStatus{}, err
	}
	return d.guestCall(ctx, vm, "configure", bytes.NewReader(raw))
}
func (d *linuxDriver) Guest(ctx context.Context, vm VM, path string, body io.Reader) (GuestStatus, error) {
	return d.guestCall(ctx, vm, path, body)
}

func (d *linuxDriver) DialVM(ctx context.Context, vm VM) (net.Conn, error) {
	conn, err := vsock.DialContext(ctx, filepath.Join(d.dir(vm), "guest.vsock"), GuestPort)
	if err != nil {
		return nil, err
	}
	if err = conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://guest/tunnel", http.NoBody)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.config.token(d.key, vm))
	req.Header.Set("Loomplane-VM", vm.ID)
	if err = req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, ErrUnavailable
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (c HostConfig) token(key []byte, vm VM) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("guest/" + vm.ID))
	return hex.EncodeToString(mac.Sum(nil))
}

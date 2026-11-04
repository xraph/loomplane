//go:build linux

package firecracker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
)

// installMeter creates the VM's meter table and returns the counter ID to
// store in its receipt. A table found at Start is a leftover from a crash, so
// it is replaced. refreshPeers never calls this, which keeps counts across
// peer changes.
func (d *linuxDriver) installMeter(ctx context.Context, vm VM, r runtimeReceipt) (string, error) {
	device, err := hostVeth(r)
	if err != nil {
		return "", err
	}
	policy, err := meterPolicy(device, vm.ID)
	if err != nil {
		return "", err
	}
	table := meterTable(vm.ID)
	check := exec.CommandContext(ctx, "nft", "list", "table", "netdev", table) //nolint:gosec // Table name is built from the validated VM identity, with no shell.
	check.Stdout = io.Discard
	check.Stderr = io.Discard
	if check.Run() == nil {
		policy = "delete table netdev " + table + "\n" + policy
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(policy)
	if err = cmd.Run(); err != nil {
		return "", err
	}
	return hex.EncodeToString(id), nil
}

func (d *linuxDriver) Usage(ctx context.Context, vms []VM) ([]UsageSample, error) {
	out := make([]UsageSample, 0, len(vms))
	for _, vm := range vms {
		receipt, err := d.readReceipt(vm)
		if errors.Is(err, os.ErrNotExist) || (err == nil && receipt.CounterID == "") {
			continue
		}
		if err != nil {
			return nil, err
		}
		var stdout bytes.Buffer
		cmd := exec.CommandContext(ctx, "nft", "-j", "list", "table", "netdev", meterTable(vm.ID)) //nolint:gosec // Table name is built from the validated VM identity, with no shell.
		cmd.Stdout = &stdout
		if cmd.Run() != nil {
			log.Printf("Firecracker egress meter missing for VM %s", vm.ID)
			continue
		}
		after, err := d.readReceipt(vm)
		if err != nil {
			log.Printf("Firecracker egress meter receipt unreadable for VM %s", vm.ID)
			continue
		}
		sample, err := meterSample(vm.ID, receipt.CounterID, after.CounterID, stdout.Bytes())
		if errors.Is(err, errMeterReset) {
			continue
		}
		if err != nil {
			log.Printf("Firecracker egress meter unreadable for VM %s", vm.ID)
			continue
		}
		out = append(out, sample)
	}
	return out, nil
}

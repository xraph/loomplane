package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

var meterID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// UsageSample is a VM's cumulative egress. CounterID changes only when the
// meter table is recreated, so a consumer can tell a reset from real traffic.
type UsageSample struct {
	Scope           Scope
	VMID, CounterID string
	Bytes, Packets  uint64
}

// UsageReader is implemented by drivers that meter VM egress. Samples are keyed
// by VMID; Host.Usage fills Scope.
type UsageReader interface {
	Usage(context.Context, []VM) ([]UsageSample, error)
}

func meterTable(vmID string) string { return "loomplane_meter_" + vmID }

// meterPolicy counts everything a VM sends, after the VM's own policy at
// priority -500, so frames that policy drops are not counted. It lives in its own table so policy refreshes never reset it.
func meterPolicy(device, vmID string) (string, error) {
	if !deviceName.MatchString(device) || !meterID.MatchString(vmID) {
		return "", ErrInvalid
	}
	return fmt.Sprintf("table netdev %s {\n counter egress { }\n chain meter { type filter hook ingress device \"%s\" priority -400; policy accept;\n counter name \"egress\"\n }\n}\n", meterTable(vmID), device), nil
}

// parseMeter reads the egress counter from `nft -j list table`.
func parseMeter(raw []byte) (bytes, packets uint64, err error) {
	var out struct {
		Nftables []struct {
			Counter *struct {
				Name           string
				Bytes, Packets uint64
			}
		}
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return 0, 0, err
	}
	for _, item := range out.Nftables {
		if item.Counter != nil && item.Counter.Name == "egress" {
			return item.Counter.Bytes, item.Counter.Packets, nil
		}
	}
	return 0, 0, ErrMissing
}

var errMeterReset = errors.New("meter table was recreated during the read")

// meterSample builds one VM's sample from nft output. before and after are the
// receipt's CounterID read either side of the nft call; a change means the
// table was replaced, so the bytes belong to a different counter and are dropped.
func meterSample(vmID, before, after string, raw []byte) (UsageSample, error) {
	if before != after {
		return UsageSample{}, errMeterReset
	}
	b, p, err := parseMeter(raw)
	if err != nil {
		return UsageSample{}, err
	}
	return UsageSample{VMID: vmID, CounterID: before, Bytes: b, Packets: p}, nil
}

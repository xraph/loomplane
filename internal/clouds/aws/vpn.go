package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/xraph/loomplane/internal/clouds/progress"
)

// The AWS side of a link: a virtual private gateway on the default VPC,
// propagating the routes BGP learns into its main route table, and a
// customer gateway and VPN connection for each of the other cloud's two
// endpoints. Everything is found again by its Name tag.

// VPNActions are what a link needs an AWS key to be allowed, beyond what
// deploying services and databases does. They aren't part of a
// connection's check, since a link is opt in.
var VPNActions = []string{
	"ec2:CreateVpnGateway", "ec2:AttachVpnGateway", "ec2:DetachVpnGateway", "ec2:DeleteVpnGateway",
	"ec2:DescribeVpnGateways", "ec2:CreateCustomerGateway", "ec2:DeleteCustomerGateway",
	"ec2:CreateVpnConnection", "ec2:DeleteVpnConnection", "ec2:DescribeVpnConnections",
	"ec2:DescribeRouteTables", "ec2:EnableVgwRoutePropagation", "ec2:CreateTags",
}

func nameTag(kind ec2types.ResourceType, name string) []ec2types.TagSpecification {
	return []ec2types.TagSpecification{{ResourceType: kind, Tags: []ec2types.Tag{{Key: sdk.String("Name"), Value: sdk.String(name)}}}}
}

func byName(name string) []ec2types.Filter {
	return []ec2types.Filter{{Name: sdk.String("tag:Name"), Values: []string{name}}}
}

// EnsureVPNGateway makes a virtual private gateway with an ASN of its
// own, attaches it to vpc, and has the VPC's main route table learn the
// routes it hears. It returns the gateway's ID.
func (s *Session) EnsureVPNGateway(ctx context.Context, name string, asn int64, vpc string) (string, error) {
	cli := ec2.NewFromConfig(s.cfg)
	found, err := cli.DescribeVpnGateways(ctx, &ec2.DescribeVpnGatewaysInput{Filters: append(byName(name),
		ec2types.Filter{Name: sdk.String("state"), Values: []string{"pending", "available"}})})
	if err != nil {
		return "", fmt.Errorf("find vpn gateway %s: %w", name, err)
	}
	var gw ec2types.VpnGateway
	if len(found.VpnGateways) > 0 {
		gw = found.VpnGateways[0]
	} else {
		out, cerr := cli.CreateVpnGateway(ctx, &ec2.CreateVpnGatewayInput{
			Type: ec2types.GatewayTypeIpsec1, AmazonSideAsn: sdk.Int64(asn),
			TagSpecifications: nameTag(ec2types.ResourceTypeVpnGateway, name),
		})
		if cerr != nil {
			return "", fmt.Errorf("create vpn gateway %s: %w", name, cerr)
		}
		gw = *out.VpnGateway
	}
	id := sdk.ToString(gw.VpnGatewayId)
	attached := false
	for _, a := range gw.VpcAttachments {
		if sdk.ToString(a.VpcId) == vpc && (a.State == ec2types.AttachmentStatusAttached || a.State == ec2types.AttachmentStatusAttaching) {
			attached = true
		}
	}
	if !attached {
		if _, aerr := cli.AttachVpnGateway(ctx, &ec2.AttachVpnGatewayInput{VpnGatewayId: sdk.String(id), VpcId: sdk.String(vpc)}); aerr != nil && !IsExists(aerr) {
			return "", fmt.Errorf("attach vpn gateway %s: %w", name, aerr)
		}
	}
	tables, err := cli.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: []ec2types.Filter{
		{Name: sdk.String("vpc-id"), Values: []string{vpc}}, {Name: sdk.String("association.main"), Values: []string{"true"}},
	}})
	if err != nil {
		return "", fmt.Errorf("find the vpc's main route table: %w", err)
	}
	if len(tables.RouteTables) == 0 {
		return "", errors.New("the vpc has no main route table")
	}
	table := sdk.ToString(tables.RouteTables[0].RouteTableId)
	for {
		_, perr := cli.EnableVgwRoutePropagation(ctx, &ec2.EnableVgwRoutePropagationInput{GatewayId: sdk.String(id), RouteTableId: sdk.String(table)})
		if perr == nil {
			return id, nil
		}
		// Propagation is refused until the gateway is attached, which
		// takes a minute or so.
		if !IsNotFound(perr) && !isState(perr) {
			return "", fmt.Errorf("propagate the vpn's routes: %w", perr)
		}
		progress.Report(ctx, "Waiting for the VPN gateway to attach")
		if err := s.wait(ctx); err != nil {
			return "", err
		}
	}
}

// EnsureCustomerGateway describes one of the other cloud's VPN endpoints
// to AWS. AWS gives back the existing gateway for the same address and
// ASN.
func (s *Session) EnsureCustomerGateway(ctx context.Context, name, ip string, asn int32) (string, error) {
	out, err := ec2.NewFromConfig(s.cfg).CreateCustomerGateway(ctx, &ec2.CreateCustomerGatewayInput{
		Type: ec2types.GatewayTypeIpsec1, IpAddress: sdk.String(ip), BgpAsn: sdk.Int32(asn),
		TagSpecifications: nameTag(ec2types.ResourceTypeCustomerGateway, name),
	})
	if err != nil {
		return "", fmt.Errorf("create customer gateway %s: %w", name, err)
	}
	return sdk.ToString(out.CustomerGateway.CustomerGatewayId), nil
}

// TunnelSpec is what one of a VPN connection's two tunnels is made with.
type TunnelSpec struct {
	InsideCIDR string // a /30 in 169.254.0.0/16
	Secret     string
}

// VPNTunnel is one of a VPN connection's tunnels as AWS made it.
type VPNTunnel struct {
	Outside    string
	InsideCIDR string
}

// EnsureVPNConnection makes a dynamically routed VPN connection between a
// virtual private gateway and a customer gateway, with its two tunnels as
// specs say, and returns its tunnels' endpoints.
func (s *Session) EnsureVPNConnection(ctx context.Context, name, customer, gateway string, specs [2]TunnelSpec) ([]VPNTunnel, error) {
	cli := ec2.NewFromConfig(s.cfg)
	conn, err := s.findVPNConnection(ctx, name)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		opts := make([]ec2types.VpnTunnelOptionsSpecification, 0, 2)
		for _, t := range specs {
			opts = append(opts, ec2types.VpnTunnelOptionsSpecification{TunnelInsideCidr: sdk.String(t.InsideCIDR), PreSharedKey: sdk.String(t.Secret)})
		}
		out, cerr := cli.CreateVpnConnection(ctx, &ec2.CreateVpnConnectionInput{
			Type: sdk.String("ipsec.1"), CustomerGatewayId: sdk.String(customer), VpnGatewayId: sdk.String(gateway),
			Options:           &ec2types.VpnConnectionOptionsSpecification{StaticRoutesOnly: sdk.Bool(false), TunnelOptions: opts},
			TagSpecifications: nameTag(ec2types.ResourceTypeVpnConnection, name),
		})
		if cerr != nil {
			return nil, fmt.Errorf("create vpn connection %s: %w", name, cerr)
		}
		conn = out.VpnConnection
	}
	var tunnels []VPNTunnel
	if conn.Options != nil {
		for _, t := range conn.Options.TunnelOptions {
			tunnels = append(tunnels, VPNTunnel{Outside: sdk.ToString(t.OutsideIpAddress), InsideCIDR: sdk.ToString(t.TunnelInsideCidr)})
		}
	}
	if len(tunnels) != 2 {
		return nil, fmt.Errorf("vpn connection %s has %d tunnels, not two", name, len(tunnels))
	}
	return tunnels, nil
}

func (s *Session) findVPNConnection(ctx context.Context, name string) (*ec2types.VpnConnection, error) {
	found, err := ec2.NewFromConfig(s.cfg).DescribeVpnConnections(ctx, &ec2.DescribeVpnConnectionsInput{Filters: append(byName(name),
		ec2types.Filter{Name: sdk.String("state"), Values: []string{"pending", "available"}})})
	if err != nil {
		return nil, fmt.Errorf("find vpn connection %s: %w", name, err)
	}
	if len(found.VpnConnections) == 0 {
		return nil, nil
	}
	c := found.VpnConnections[0]
	return &c, nil
}

// AllowFromCIDR lets everything from a block of addresses into a security
// group, on every TCP port: the other cloud's network, over a link.
func (s *Session) AllowFromCIDR(ctx context.Context, group, cidr string) error {
	_, err := ec2.NewFromConfig(s.cfg).AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sdk.String(group), IpPermissions: []ec2types.IpPermission{{
			IpProtocol: sdk.String("tcp"), FromPort: sdk.Int32(0), ToPort: sdk.Int32(65535),
			IpRanges: []ec2types.IpRange{{CidrIp: sdk.String(cidr), Description: sdk.String("Loomplane link")}},
		}},
	})
	if err != nil && !IsExists(err) {
		return fmt.Errorf("let %s into %s: %w", cidr, group, err)
	}
	return nil
}

// DeleteVPN deletes a link's AWS side, by name: its VPN connections, its
// customer gateways, and the virtual private gateway, detached first.
// What's already gone is skipped.
func (s *Session) DeleteVPN(ctx context.Context, gateway string, connections []string, vpc string) error {
	cli := ec2.NewFromConfig(s.cfg)
	for _, name := range connections {
		conn, err := s.findVPNConnection(ctx, name)
		if err != nil {
			return err
		}
		if conn == nil {
			continue
		}
		if _, err := cli.DeleteVpnConnection(ctx, &ec2.DeleteVpnConnectionInput{VpnConnectionId: conn.VpnConnectionId}); err != nil && !IsNotFound(err) {
			return fmt.Errorf("delete vpn connection %s: %w", name, err)
		}
		if conn.CustomerGatewayId != nil {
			if _, err := cli.DeleteCustomerGateway(ctx, &ec2.DeleteCustomerGatewayInput{CustomerGatewayId: conn.CustomerGatewayId}); err != nil && !IsNotFound(err) && !isState(err) {
				return fmt.Errorf("delete customer gateway for %s: %w", name, err)
			}
		}
	}
	found, err := cli.DescribeVpnGateways(ctx, &ec2.DescribeVpnGatewaysInput{Filters: append(byName(gateway),
		ec2types.Filter{Name: sdk.String("state"), Values: []string{"pending", "available"}})})
	if err != nil {
		return fmt.Errorf("find vpn gateway %s: %w", gateway, err)
	}
	for _, gw := range found.VpnGateways {
		if _, err := cli.DetachVpnGateway(ctx, &ec2.DetachVpnGatewayInput{VpnGatewayId: gw.VpnGatewayId, VpcId: sdk.String(vpc)}); err != nil && !IsNotFound(err) && !isState(err) {
			return fmt.Errorf("detach vpn gateway %s: %w", gateway, err)
		}
		for {
			_, err := cli.DeleteVpnGateway(ctx, &ec2.DeleteVpnGatewayInput{VpnGatewayId: gw.VpnGatewayId})
			if err == nil || IsNotFound(err) {
				break
			}
			// It can't go while it's still detaching.
			if !isState(err) {
				return fmt.Errorf("delete vpn gateway %s: %w", gateway, err)
			}
			progress.Report(ctx, "Waiting for the VPN gateway to detach")
			if err := s.wait(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// isState is AWS refusing something because of what state a resource is
// in, which waiting fixes.
func isState(err error) bool {
	return hasCode(err, "IncorrectState") || hasCode(err, "InvalidVpnGatewayAttachment.NotFound") || hasCode(err, "DependencyViolation")
}

func hasCode(err error, code string) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && strings.Contains(ae.ErrorCode(), code)
}

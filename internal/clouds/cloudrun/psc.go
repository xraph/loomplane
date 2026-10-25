package cloudrun

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	strictjson "sigs.k8s.io/json"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/pkg/provider"
)

// LabelCloudSQLPSC carries a reviewed PSCTransport as JSON.
const LabelCloudSQLPSC = "loomplane-cloudsql-psc"

const (
	PSCSocketDirectory = "/cloudsql-psc"
	pscContainer       = "cloudsql-psc"
	pscProxyImage      = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.26.0@sha256:86e4f3cc266020e7ee07740df78d7511e2c340c274b56f84d80b7251913af9ad"
)

// PSCTransport binds canonical instance names to an explicit application route.
// Provider validation does not establish endpoint ownership or IAM permission.
type PSCTransport struct {
	Network   provider.CloudSQLApplicationNetwork `json:"network"`
	Instances []string                            `json:"instances"`
}

var pscInstanceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,97}$`)

func applyPSCTransport(template *gcp.RunTemplate, labels map[string]string, project, region string) error {
	raw, present := labels[LabelCloudSQLPSC]
	if !present {
		return nil
	}
	var transport PSCTransport
	strictErrors, err := strictjson.UnmarshalStrict([]byte(raw), &transport)
	if err != nil {
		return fmt.Errorf("decode cloud sql PSC transport: %w", err)
	}
	if len(strictErrors) > 0 {
		return fmt.Errorf("cloud sql PSC transport has unknown or duplicate fields")
	}
	if !transport.Network.Valid(region) || len(transport.Instances) == 0 {
		return fmt.Errorf("cloud sql PSC transport requires a canonical VPC, regional subnet and instances")
	}
	if labels[LabelVPC] != "" {
		return fmt.Errorf("cloud sql PSC transport conflicts with the service VPC label")
	}
	if cpu := labels[LabelCPU]; cpu != "" {
		value, err := strconv.Atoi(cpu)
		if err != nil || value < 1 {
			return fmt.Errorf("cloud sql PSC sidecar requires whole application CPUs")
		}
	}
	legacy := strings.Split(labels[LabelCloudSQL], ",")
	seen := make(map[string]bool, len(transport.Instances))
	for _, instance := range transport.Instances {
		parts := strings.Split(instance, ":")
		if len(parts) != 3 || parts[0] != project || parts[1] != region || !pscInstanceName.MatchString(parts[2]) {
			return fmt.Errorf("cloud sql PSC instance must be a canonical connection name in the service project and region")
		}
		if seen[instance] || slices.Contains(legacy, instance) {
			return fmt.Errorf("cloud sql instance has duplicate or conflicting socket transports")
		}
		// Linux reserves one byte of sockaddr_un's 108-byte path for NUL.
		if len(PSCSocketDirectory+"/"+instance+"/.s.PGSQL.5432") >= 108 {
			return fmt.Errorf("cloud sql PSC PostgreSQL socket path exceeds the Unix socket limit")
		}
		seen[instance] = true
	}
	app := &template.Containers[0]
	if len(app.Ports) == 0 {
		app.Ports = []gcp.RunPort{{ContainerPort: 8080}}
	}
	healthPort := 9090
	if len(app.Ports) > 0 && app.Ports[0].ContainerPort == healthPort {
		healthPort++
	}
	app.Name = "main"
	app.DependsOn = []string{pscContainer}
	mount := gcp.RunVolumeMount{Name: pscContainer, MountPath: PSCSocketDirectory}
	app.Mounts = append(app.Mounts, mount)
	template.Volumes = append(template.Volumes, gcp.RunVolume{Name: pscContainer, EmptyDir: &gcp.RunEmptyDir{Medium: "MEMORY", SizeLimit: "1Mi"}})
	template.VPCAccess = &gcp.RunVPCAccess{
		NetworkInterfaces: []gcp.RunNetworkInterface{{Network: transport.Network.Network, Subnetwork: transport.Network.Subnetwork}},
		Egress:            "PRIVATE_RANGES_ONLY",
	}
	// The readiness handler alone only checks local proxy state. The connection
	// test must pass before the proxy reports that it has started.
	args := make([]string, 0, 8+len(transport.Instances))
	args = append(args, "--psc", "--unix-socket="+PSCSocketDirectory, "--health-check", "--run-connection-test", "--http-address=0.0.0.0", "--http-port="+strconv.Itoa(healthPort), "--lazy-refresh", "--structured-logs")
	if len(args)+len(transport.Instances) > 1000 {
		return fmt.Errorf("cloud sql PSC transport exceeds Cloud Run's 1000 argument limit")
	}
	template.Containers = append(template.Containers, gcp.RunContainer{
		Name: pscContainer, Image: pscProxyImage,
		Args: append(args, transport.Instances...), Mounts: []gcp.RunVolumeMount{mount},
		Resources:    &gcp.RunResources{Limits: map[string]string{"cpu": "1", "memory": "256Mi"}, CPUIdle: true},
		StartupProbe: &gcp.RunProbe{HTTPGet: gcp.RunHTTPGet{Path: "/readiness", Port: healthPort}, TimeoutSeconds: 4, PeriodSeconds: 5, FailureThreshold: 48},
	})
	return nil
}

// Package providers is Loomplane's catalog of deployment targets: the
// clouds and platforms a plan can name, described with pkg/provider.
//
// Nothing here talks to a cloud: internal/deployments has the adapters.
// A target without one, or one paused in the configuration, is described
// but not available, and plans that use it are for review only.
package providers

import (
	"slices"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

// Region lists keep to well-established regions. A manifest can name any
// region; the planner warns about one that isn't listed, since a typo
// there would only surface when a deployment fails.
var (
	gcpRegions = []string{
		"us-central1", "us-east1", "us-east4", "us-west1", "northamerica-northeast1", "southamerica-east1",
		"europe-west1", "europe-west2", "europe-west3", "europe-west4", "asia-east1", "asia-northeast1",
		"asia-south1", "asia-southeast1", "australia-southeast1",
	}
	awsRegions = []string{
		"us-east-1", "us-east-2", "us-west-2", "ca-central-1", "sa-east-1", "eu-west-1", "eu-west-2",
		"eu-central-1", "eu-north-1", "ap-south-1", "ap-southeast-1", "ap-southeast-2", "ap-northeast-1",
	}
	renderRegions = []string{"oregon", "ohio", "virginia", "frankfurt", "singapore"}
	// App Platform's regions. Managed Databases go in each one's
	// datacenter.
	digitalOceanRegions = []string{"nyc", "sfo", "tor", "atl", "ams", "fra", "lon", "blr", "sgp", "syd"}
	// Regions with Container Apps and ACR Tasks.
	azureRegions = []string{
		"eastus", "eastus2", "centralus", "westus2", "westus3", "canadacentral", "brazilsouth",
		"northeurope", "westeurope", "uksouth", "francecentral", "germanywestcentral", "swedencentral",
		"japaneast", "koreacentral", "southeastasia", "centralindia", "australiaeast",
	}
)

// builtinKinds are the engines whose provisions are listed literally for
// the server and Kubernetes targets. Every other registered kind is added
// after them.
var builtinKinds = []string{"postgres", "mysql", "mariadb", "redis", "valkey"}

// Catalog returns the built-in targets, in the order a person choosing one
// should see them. The server and Kubernetes targets also offer every kind
// registered in internal/recipes that runs on them.
func Catalog() []provider.Provider {
	return catalogWith(recipes.Default())
}

func catalogWith(kinds *recipes.Registry) []provider.Provider {
	all := []provider.Provider{
		{
			Name: "vercel", Title: "Vercel", Cloud: "vercel",
			BuildLogs:   "streamed",
			Description: "Builds web apps and static sites into immutable deployments, served from Vercel's network with server rendering in functions.",
			Model:       provider.Build,
			Runs: []provider.Runs{
				{Workload: provider.WebApp, Execution: provider.Serverless, Creates: "Vercel project"},
				{Workload: provider.StaticSite, Execution: provider.Static, Creates: "Vercel project"},
			},
			// Uploaded and built by Vercel, through a Loomplane adapter
			// (internal/deployments).
			Available: true,
		},
		{
			Name: "cloud-run", Title: "Google Cloud Run", Cloud: "gcp",
			BuildLogs: "polled", LogNote: "Requires Cloud Logging read permission.",
			Description: "Runs containers that serve requests, scaling them with traffic, down to zero if you let it.",
			Model:       provider.Instance,
			Runs: []provider.Runs{
				{Workload: provider.HTTPService, Execution: provider.Container, Creates: "Cloud Run service"},
				{Workload: provider.WebApp, Execution: provider.Container, Creates: "Cloud Run service"},
			},
			Regions: gcpRegions, Scaling: true,
			// Built with Cloud Build, run as a Ctrl Plane instance
			// (internal/deployments, internal/clouds/cloudrun).
			Available: true,
		},
		{
			Name: "ecs-fargate", Title: "AWS ECS on Fargate", Cloud: "aws",
			BuildLogs: "polled", LogNote: "Requires CloudWatch GetLogEvents permission.",
			Description: "Runs containers as ECS services on Fargate, so there are no servers to manage. Services that serve HTTP sit behind a load balancer.",
			Model:       provider.Instance,
			Runs: []provider.Runs{
				{Workload: provider.HTTPService, Execution: provider.Container, Creates: "ECS service on Fargate, behind a load balancer"},
				{Workload: provider.WebApp, Execution: provider.Container, Creates: "ECS service on Fargate, behind a load balancer"},
				{Workload: provider.Worker, Execution: provider.Container, Creates: "ECS service on Fargate"},
			},
			Regions: awsRegions, Scaling: true,
			// Built with CodeBuild, run as a Ctrl Plane instance behind
			// the environment's load balancer (internal/deployments,
			// internal/clouds/ecs).
			Available: true,
		},
		{
			Name: "render", Title: "Render", Cloud: "render",
			BuildLogs: "unavailable", LogNote: "Render's build-log API cannot isolate one deployment; progress remains available.",
			Description: "Builds and runs web services, private services, background workers and static sites from your repository, and provisions Postgres and Key Value alongside them.",
			Model:       provider.Build,
			Runs: []provider.Runs{
				{Workload: provider.HTTPService, Execution: provider.Container, Creates: "Render web service"},
				{Workload: provider.WebApp, Execution: provider.Container, Creates: "Render web service"},
				{Workload: provider.Worker, Execution: provider.Container, Creates: "Render background worker"},
				{Workload: provider.StaticSite, Execution: provider.Static, Creates: "Render static site"},
			},
			Provisions: []provider.Provisions{
				{Kind: "postgres", Creates: "Render Postgres database", Versions: []string{"17", "16", "15"}},
				{Kind: "redis", Creates: "Render Key Value instance"},
			},
			Regions: renderRegions, Scaling: true,
			Available: true,
		},
		{
			Name: "digitalocean", Title: "DigitalOcean App Platform", Cloud: "digitalocean",
			BuildLogs: "unavailable", LogNote: "App Platform's build logs are on the app's Activity tab in DigitalOcean; progress remains available.",
			Description: "Builds and runs services, workers and static sites from your repository on App Platform, checking each build is the commit you approved, and provisions Managed PostgreSQL, MySQL and Valkey alongside them.",
			Model:       provider.Build,
			Runs: []provider.Runs{
				{Workload: provider.HTTPService, Execution: provider.Container, Creates: "App Platform app"},
				{Workload: provider.WebApp, Execution: provider.Container, Creates: "App Platform app"},
				{Workload: provider.Worker, Execution: provider.Container, Creates: "App Platform app with a worker"},
				{Workload: provider.StaticSite, Execution: provider.Static, Creates: "App Platform static site"},
			},
			Provisions: []provider.Provisions{
				{Kind: "postgres", Creates: "Managed PostgreSQL cluster", Versions: []string{"17", "16", "15"}},
				{Kind: "mysql", Creates: "Managed MySQL cluster", Versions: []string{"8"}},
				{Kind: "redis", Creates: "Managed Valkey cluster"},
			},
			Regions: digitalOceanRegions, Scaling: true,
			Available: true,
		},
		{
			Name: "azure-container-apps", Title: "Azure Container Apps", Cloud: "azure",
			BuildLogs: "unavailable", LogNote: "ACR Tasks' build logs are under Services, Tasks, Runs in the container registry Loomplane makes; progress remains available.",
			Description: "Builds a service's Dockerfile with ACR Tasks and runs it on Azure Container Apps, which scales it with traffic, down to zero if you let it.",
			Model:       provider.Build,
			Runs: []provider.Runs{
				{Workload: provider.HTTPService, Execution: provider.Container, Creates: "Container app"},
				{Workload: provider.WebApp, Execution: provider.Container, Creates: "Container app"},
			},
			Regions: azureRegions, Scaling: true,
			// Built and run by a Loomplane adapter (internal/deployments,
			// internal/clouds/azure), each build pinned to the approved
			// commit's source.
			Available: true,
		},
		{
			Name: "cross-cloud-vpn", Title: "Cross-cloud VPN", Cloud: "gcp",
			Description: "Links an environment's Google Cloud network to its AWS VPC with an HA VPN: four IPsec tunnels with BGP, so Cloud Run services reach private services and databases on AWS.",
			Model:       provider.Managed,
			Provisions:  []provider.Provisions{{Kind: "private-link", Creates: "HA VPN between Google Cloud and AWS"}},
			Regions:     gcpRegions, Available: true,
		},
		{
			Name: "cloud-sql", Title: "Google Cloud SQL", Cloud: "gcp",
			Description: "Managed Postgres and MySQL on Google Cloud.",
			Model:       provider.Managed,
			Provisions: []provider.Provisions{
				{Kind: "postgres", Creates: "Cloud SQL for PostgreSQL instance", Versions: []string{"17", "16", "15"}, ManagedProfiles: manageddb.Profiles("cloud-sql")},
				{Kind: "mysql", Creates: "Cloud SQL for MySQL instance", Versions: []string{"8.4", "8.0"}},
			},
			Regions: gcpRegions,
			// Provisioned by Loomplane, reached through the Cloud SQL
			// connector (internal/deployments).
			Available: true,
		},
		{
			Name: "memorystore", Title: "Google Memorystore", Cloud: "gcp",
			Description: "Managed Redis on Google Cloud.",
			Model:       provider.Managed,
			Provisions:  []provider.Provisions{{Kind: "redis", Creates: "Memorystore for Redis instance"}},
			Regions:     gcpRegions,
			// Provisioned by Loomplane on the default network; Cloud Run
			// services that need it get Direct VPC egress (internal/deployments).
			Available: true,
		},
		{
			Name: "cloud-storage", Title: "Google Cloud Storage", Cloud: "gcp",
			Description: "Object storage buckets on Google Cloud.",
			Model:       provider.Managed,
			Provisions:  []provider.Provisions{{Kind: "object-storage", Creates: "Cloud Storage bucket"}},
			Regions:     gcpRegions,
			// A private bucket the project's runtime account reaches
			// (internal/deployments).
			Available: true,
		},
		{
			Name: "rds", Title: "Amazon RDS", Cloud: "aws",
			Description: "Managed Postgres and MySQL on AWS.",
			Model:       provider.Managed,
			Provisions: []provider.Provisions{
				{Kind: "postgres", Creates: "RDS for PostgreSQL instance", Versions: []string{"17", "16", "15"}, ManagedProfiles: manageddb.Profiles("rds")},
				{Kind: "mysql", Creates: "RDS for MySQL instance", Versions: []string{"8.4", "8.0"}},
			},
			Regions: awsRegions,
			// Provisioned by Loomplane, reachable from the environment's
			// ECS tasks only (internal/deployments).
			Available: true,
		},
		{
			Name: "elasticache", Title: "Amazon ElastiCache", Cloud: "aws",
			Description: "Managed Redis on AWS.",
			Model:       provider.Managed,
			Provisions:  []provider.Provisions{{Kind: "redis", Creates: "ElastiCache Redis OSS cluster"}},
			Regions:     awsRegions,
			// Provisioned by Loomplane, reachable from the environment's
			// ECS tasks only (internal/deployments).
			Available: true,
		},
		{
			Name: "s3", Title: "Amazon S3", Cloud: "aws",
			Description: "Object storage buckets on AWS.",
			Model:       provider.Managed,
			Provisions:  []provider.Provisions{{Kind: "object-storage", Creates: "S3 bucket"}},
			Regions:     awsRegions,
			// A private bucket the environment's ECS tasks reach through
			// their role (internal/deployments).
			Available: true,
		},
		{
			Name: "server", Title: "Existing server", Cloud: "server",
			BuildLogs:   "streamed",
			Description: "Runs a service container on the Docker daemon of the SSH server you choose.",
			Model:       provider.Instance,
			Runs:        containerWorkloads("Docker container"),
			Provisions: []provider.Provisions{
				{Kind: "postgres", Creates: "Persistent PostgreSQL container", Versions: []string{"17"}},
				{Kind: "mysql", Creates: "Persistent MySQL container", Versions: []string{"8.4"}},
				{Kind: "mariadb", Creates: "Persistent MariaDB container", Versions: []string{"11.8"}},
				{Kind: "redis", Creates: "Persistent Redis container", Versions: []string{"8.2"}},
				{Kind: "valkey", Creates: "Persistent Valkey container", Versions: []string{"8.1"}},
			},
			Available: true,
		},
		{
			Name: "kubernetes", Title: "Kubernetes", Cloud: "kubernetes",
			BuildLogs:   "streamed",
			Description: "Runs service containers in the namespace of the cluster you choose.",
			Model:       provider.Instance, Scaling: true,
			Runs:       containerWorkloads("Kubernetes Deployment"),
			Provisions: []provider.Provisions{{Kind: "postgres", Creates: "Retained PostgreSQL StatefulSet", Versions: []string{"17"}}},
			Available:  true,
		},
	}
	for i := range all {
		switch all[i].Cloud {
		case "digitalocean":
			all[i].Infrastructure = digitalocean.InfrastructureCapabilities()
		case "vercel":
			all[i].Infrastructure = vercel.InfrastructureCapabilities()
		case "server", "kubernetes":
			all[i].Provisions = append(all[i].Provisions, registeredProvisions(kinds, all[i].Cloud)...)
		}
	}
	return all
}

// registeredProvisions describes the registered kinds, other than the five
// built-in engines, that run on the cloud.
func registeredProvisions(kinds *recipes.Registry, cloud string) []provider.Provisions {
	var out []provider.Provisions
	for _, info := range kinds.Kinds() {
		if slices.Contains(builtinKinds, info.Name) || !slices.Contains(info.Targets, cloud) {
			continue
		}
		out = append(out, provider.Provisions{
			Kind:     info.Name,
			Creates:  info.Title + " catalog service",
			Versions: slices.Clone(info.Versions),
		})
	}
	return out
}

func containerWorkloads(creates string) []provider.Runs {
	return []provider.Runs{
		{Workload: provider.HTTPService, Execution: provider.Container, Creates: creates},
		{Workload: provider.WebApp, Execution: provider.Container, Creates: creates},
		{Workload: provider.StaticSite, Execution: provider.Container, Creates: creates},
		{Workload: provider.Worker, Execution: provider.Container, Creates: creates},
	}
}

// PausedNote is what a plan says about a paused target.
const PausedNote = "It's paused in this Loomplane's configuration (providers.paused)."

// Pause makes p a target whose plans are for review only.
func Pause(p *provider.Provider) {
	p.Available, p.Note = false, PausedNote
}

// Registry returns a registry of the catalog.
// Any named in paused are for review only.
func Registry(paused ...string) *provider.Registry {
	all := Catalog()
	for i := range all {
		if slices.Contains(paused, all[i].Name) {
			Pause(&all[i])
		}
	}
	r, err := provider.NewRegistry(all...)
	if err != nil {
		// The catalog is a constant; its test registers it.
		panic(err)
	}
	return r
}

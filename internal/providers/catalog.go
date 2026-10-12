// Package providers is Loomplane's catalog of deployment targets: the
// clouds and platforms a plan can name, described with pkg/provider.
//
// Nothing here talks to a cloud. Each adapter arrives with its milestone
// (docs/implementation-plan.md); until then its target is described but
// not available, and plans that use it are for review only.
package providers

import (
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
)

const (
	milestone4 = "Loomplane's adapter for it arrives in milestone 4."
	milestone5 = "Loomplane's adapter for it arrives in milestone 5."
)

// Catalog returns the built-in targets, in the order a person choosing one
// should see them.
func Catalog() []provider.Provider {
	return []provider.Provider{
		{
			Name: "vercel", Title: "Vercel", Cloud: "vercel",
			Description: "Builds web apps and static sites into immutable deployments, served from Vercel's network with server rendering in functions.",
			Model:       provider.Build,
			Runs: []provider.Runs{
				{Workload: provider.WebApp, Execution: provider.Serverless, Creates: "Vercel project"},
				{Workload: provider.StaticSite, Execution: provider.Static, Creates: "Vercel project"},
			},
			Note: milestone4,
		},
		{
			Name: "cloud-run", Title: "Google Cloud Run", Cloud: "gcp",
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
			Description: "Runs containers as ECS services on Fargate, so there are no servers to manage. Services that serve HTTP sit behind a load balancer.",
			Model:       provider.Instance,
			Runs: []provider.Runs{
				{Workload: provider.HTTPService, Execution: provider.Container, Creates: "ECS service on Fargate, behind a load balancer"},
				{Workload: provider.WebApp, Execution: provider.Container, Creates: "ECS service on Fargate, behind a load balancer"},
				{Workload: provider.Worker, Execution: provider.Container, Creates: "ECS service on Fargate"},
			},
			Regions: awsRegions, Scaling: true,
			Note: milestone5,
		},
		{
			Name: "render", Title: "Render", Cloud: "render",
			Description: "Runs web services, background workers and static sites, and provisions Postgres and Key Value alongside them.",
			Model:       provider.Instance,
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
			Note: milestone5,
		},
		{
			Name: "cloud-sql", Title: "Google Cloud SQL", Cloud: "gcp",
			Description: "Managed Postgres and MySQL on Google Cloud.",
			Model:       provider.Managed,
			Provisions: []provider.Provisions{
				{Kind: "postgres", Creates: "Cloud SQL for PostgreSQL instance", Versions: []string{"17", "16", "15"}},
				{Kind: "mysql", Creates: "Cloud SQL for MySQL instance", Versions: []string{"8.4", "8.0"}},
			},
			Regions: gcpRegions,
			Note:    milestone5,
		},
		{
			Name: "memorystore", Title: "Google Memorystore", Cloud: "gcp",
			Description: "Managed Redis on Google Cloud.",
			Model:       provider.Managed,
			Provisions:  []provider.Provisions{{Kind: "redis", Creates: "Memorystore for Redis instance"}},
			Regions:     gcpRegions,
			Note:        milestone5,
		},
		{
			Name: "cloud-storage", Title: "Google Cloud Storage", Cloud: "gcp",
			Description: "Object storage buckets on Google Cloud.",
			Model:       provider.Managed,
			Provisions:  []provider.Provisions{{Kind: "object-storage", Creates: "Cloud Storage bucket"}},
			Regions:     gcpRegions,
			Note:        milestone5,
		},
		{
			Name: "rds", Title: "Amazon RDS", Cloud: "aws",
			Description: "Managed Postgres and MySQL on AWS.",
			Model:       provider.Managed,
			Provisions: []provider.Provisions{
				{Kind: "postgres", Creates: "RDS for PostgreSQL instance", Versions: []string{"17", "16", "15"}},
				{Kind: "mysql", Creates: "RDS for MySQL instance", Versions: []string{"8.4", "8.0"}},
			},
			Regions: awsRegions,
			Note:    milestone5,
		},
		{
			Name: "elasticache", Title: "Amazon ElastiCache", Cloud: "aws",
			Description: "Managed Redis and Memcached on AWS.",
			Model:       provider.Managed,
			Provisions: []provider.Provisions{
				{Kind: "redis", Creates: "ElastiCache Redis OSS cluster"},
				{Kind: "memcached", Creates: "ElastiCache Memcached cluster"},
			},
			Regions: awsRegions,
			Note:    milestone5,
		},
		{
			Name: "s3", Title: "Amazon S3", Cloud: "aws",
			Description: "Object storage buckets on AWS.",
			Model:       provider.Managed,
			Provisions:  []provider.Provisions{{Kind: "object-storage", Creates: "S3 bucket"}},
			Regions:     awsRegions,
			Note:        milestone5,
		},
	}
}

// Registry returns a registry of the catalog.
func Registry() *provider.Registry {
	r, err := provider.NewRegistry(Catalog()...)
	if err != nil {
		// The catalog is a constant; its test registers it.
		panic(err)
	}
	return r
}

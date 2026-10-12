package aws

import (
	"context"
	"fmt"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	sd "github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"

	"github.com/xraph/loomplane/internal/clouds/progress"
)

// EnsureNamespace finds or creates a Cloud Map HTTP namespace, which ECS
// Service Connect resolves service names in, and returns its ARN. It
// creates the namespace asynchronously, so this waits for the operation.
func (s *Session) EnsureNamespace(ctx context.Context, name string) (string, error) {
	cli := sd.NewFromConfig(s.cfg)
	pages := sd.NewListNamespacesPaginator(cli, &sd.ListNamespacesInput{Filters: []sdtypes.NamespaceFilter{{
		Name: sdtypes.NamespaceFilterNameType, Values: []string{string(sdtypes.NamespaceTypeHttp)},
	}}})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("list namespaces: %w", err)
		}
		for _, ns := range page.Namespaces {
			if sdk.ToString(ns.Name) == name {
				return sdk.ToString(ns.Arn), nil
			}
		}
	}
	out, err := cli.CreateHttpNamespace(ctx, &sd.CreateHttpNamespaceInput{
		Name: sdk.String(name), Description: sdk.String("Loomplane services in " + name),
	})
	if err != nil {
		return "", fmt.Errorf("create namespace %s: %w", name, err)
	}
	for {
		op, err := cli.GetOperation(ctx, &sd.GetOperationInput{OperationId: out.OperationId})
		if err != nil {
			return "", fmt.Errorf("read the namespace's creation: %w", err)
		}
		switch op.Operation.Status {
		case sdtypes.OperationStatusSuccess:
			id := op.Operation.Targets[string(sdtypes.OperationTargetTypeNamespace)]
			got, gerr := cli.GetNamespace(ctx, &sd.GetNamespaceInput{Id: sdk.String(id)})
			if gerr != nil {
				return "", fmt.Errorf("read namespace %s: %w", name, gerr)
			}
			return sdk.ToString(got.Namespace.Arn), nil
		case sdtypes.OperationStatusFail:
			return "", fmt.Errorf("create namespace %s: %s", name, sdk.ToString(op.Operation.ErrorMessage))
		}
		progress.Report(ctx, "Creating the service namespace")
		if werr := s.wait(ctx); werr != nil {
			return "", werr
		}
	}
}

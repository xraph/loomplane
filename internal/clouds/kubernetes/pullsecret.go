package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
)

func validatePullSecret(name string) error {
	if name != "" && (len(validation.IsDNS1123Subdomain(name)) != 0 || strings.HasPrefix(name, "lp-")) {
		return fmt.Errorf("%w: image_pull_secret must be an existing namespace Secret outside Loomplane's reserved lp- names", ErrInvalid)
	}
	return nil
}

// CheckPullSecret reads only the Secret printer's name/type row. Full objects
// and annotations can contain credentials, so there is no JSON fallback.
func (c *Client) CheckPullSecret(ctx context.Context) error {
	name := c.Config.Details["image_pull_secret"]
	if name == "" {
		return nil
	}
	if err := validatePullSecret(name); err != nil {
		return err
	}
	client, err := rest.HTTPClientFor(c.Config.REST)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.Config.REST.Host, "/")+"/api/v1/namespaces/"+url.PathEscape(c.Config.Namespace)+"/secrets/"+url.PathEscape(name)+"?includeObject=None", http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json;as=Table;g=meta.k8s.io;v=v1")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("check image_pull_secret: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("check image_pull_secret %s: HTTP %d; provide an accessible existing registry Secret with Table support", name, response.StatusCode)
	}
	var table metav1.Table
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&table); err != nil {
		return fmt.Errorf("decode image_pull_secret table: %w", err)
	}
	if table.Kind != "Table" || table.APIVersion != "meta.k8s.io/v1" || len(table.Rows) != 1 {
		return fmt.Errorf("%w: image_pull_secret response must be a single credential-free Table row", ErrInvalid)
	}
	row := table.Rows[0]
	if len(row.Object.Raw) != 0 || row.Object.Object != nil {
		return fmt.Errorf("%w: image_pull_secret table must not include an object", ErrInvalid)
	}
	cell := func(column string) string {
		for i, definition := range table.ColumnDefinitions {
			if definition.Name == column && i < len(row.Cells) {
				value, ok := row.Cells[i].(string)
				if ok {
					return value
				}
				return ""
			}
		}
		return ""
	}
	if cell("Name") != name || (cell("Type") != "kubernetes.io/dockerconfigjson" && cell("Type") != "kubernetes.io/dockercfg") {
		return fmt.Errorf("%w: image_pull_secret must name a dockerconfigjson or dockercfg Secret", ErrInvalid)
	}
	return nil
}

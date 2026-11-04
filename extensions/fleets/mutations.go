package fleets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/xraph/forge"

	domain "github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type createService interface {
	CreateInstance(context.Context, security.Principal, string, fleet.CreateInstanceInput) (*fleet.OperationResult, error)
}

func decodeRequest(c forge.Context, input any) error {
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, fleet.MaxTemplateSize+1))
	if err != nil || len(raw) == 0 || len(raw) > fleet.MaxTemplateSize {
		return fail(domain.ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(input); err != nil {
		return fail(domain.ErrInvalid)
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fail(domain.ErrInvalid)
	}
	return nil
}
func (e *Extension) createInstance(c forge.Context) error {
	p, f, err := e.scope(c)
	if err != nil {
		return err
	}
	var in fleet.CreateInstanceInput
	if decodeErr := decodeRequest(c, &in); decodeErr != nil {
		return decodeErr
	}
	svc, ok := e.svc.(createService)
	if !ok {
		return fail(domain.ErrInvalid)
	}
	result, err := svc.CreateInstance(c.Request().Context(), p, f.ID, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusAccepted, result)
}

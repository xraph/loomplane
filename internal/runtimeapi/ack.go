package runtimeapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func decodeRuntimeBody(w http.ResponseWriter, r *http.Request, limit int64, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return runtimecatalog.ErrInvalid
	}
	return nil
}

func (h *Handler) acknowledge(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r, 10*time.Second)
	defer cancel()
	agent, err := h.authenticate(ctx, r, h.registry)
	if err != nil {
		writeError(w, err)
		return
	}
	deadline, _ := ctx.Deadline()
	if deadlineErr := http.NewResponseController(w).SetReadDeadline(deadline); deadlineErr != nil {
		writeError(w, deadlineErr)
		return
	}
	var ack domain.Acknowledgment
	if err = decodeRuntimeBody(w, r, domain.MaxAcknowledgmentBytes, &ack); err != nil {
		writeDecodeError(w, err)
		return
	}
	if ack.Agent != agent {
		writeError(w, workloadidentity.ErrIdentity)
		return
	}
	if _, err = h.authenticate(ctx, r, h.registry); err != nil {
		writeError(w, err)
		return
	}
	if err = h.receipts.AcknowledgeSnapshot(ctx, agent, ack); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

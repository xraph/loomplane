package ai

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

func TestFailMapsServiceErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
		code string
		msg  string
	}{
		{fmt.Errorf("%w: a name is lower case", aisettings.ErrInvalid), http.StatusUnprocessableEntity, codeInvalid, "a name is lower case"},
		{aisettings.ErrInUse, http.StatusUnprocessableEntity, codeInUse, aisettings.ErrInUse.Error()},
		{fmt.Errorf("%w by $10.00", aisettings.ErrOverBudget), http.StatusUnprocessableEntity, codeInvalid, ""},
		{aisettings.ErrUnderAllocated, http.StatusUnprocessableEntity, codeInvalid, ""},
		{aisettings.ErrStale, http.StatusConflict, codeStale, ""},
		{aisettings.ErrNotFound, http.StatusNotFound, codeNotFound, ""},
		{fmt.Errorf("%w (ai:manage)", security.ErrForbidden), http.StatusForbidden, codeForbidden, ""},
		{security.ErrUnauthenticated, http.StatusUnauthorized, codeUnauthenticated, ""},

		// The models service's sentinels lose their own text in front.
		{fmt.Errorf("%w: a monthly budget is an amount in dollars above 0", models.ErrInvalid), http.StatusUnprocessableEntity, codeInvalid, "a monthly budget is an amount in dollars above 0"},
		{fmt.Errorf("%w: task %q isn't one Loomplane serves", models.ErrInvalidQuery, "dance"), http.StatusUnprocessableEntity, codeInvalid, `task "dance" isn't one Loomplane serves`},
		{fmt.Errorf("%w: checkout in production links to it. Unlink it first", models.ErrInUse), http.StatusUnprocessableEntity, codeInUse, "checkout in production links to it. Unlink it first"},
		{fmt.Errorf("%w: llm changed since you read it. Load it again", models.ErrStale), http.StatusConflict, codeStale, "llm changed since you read it. Load it again"},
		{models.ErrConflict, http.StatusConflict, codeConflict, ""},
		{fmt.Errorf("%w: the model registry has no x/y", models.ErrEntryNotFound), http.StatusNotFound, codeNotFound, "the model registry has no x/y"},
		{models.ErrNotFound, http.StatusNotFound, codeNotFound, ""},
		{projects.ErrEnvironmentNotFound, http.StatusNotFound, codeNotFound, ""},
		{manifests.ErrNotFound, http.StatusNotFound, codeNotFound, ""},

		// Busy and changed underneath.
		{planning.ErrConflict, http.StatusConflict, codeBusy, ""},
		{cloudinfra.ErrBusy, http.StatusConflict, codeBusy, ""},
		{manifests.ErrConflict, http.StatusConflict, codeStale, ""},
		{fmt.Errorf("apply: %w", cloudinfra.ErrConflict), http.StatusConflict, codeStale, ""},

		// A deploy's problem keeps its code and sentence.
		{&models.ProblemError{Problem: models.Problem{Code: models.ProblemGated, Message: "Llama 3.1 8B Instruct is gated on Hugging Face. Add a Hugging Face token that has accepted its license in AI settings."}},
			http.StatusUnprocessableEntity, models.ProblemGated, "Llama 3.1 8B Instruct is gated on Hugging Face. Add a Hugging Face token that has accepted its license in AI settings."},
		{fmt.Errorf("deploy: %w", &models.GatedError{Title: "Llama 3.1 8B Instruct"}), http.StatusUnprocessableEntity, codeGated,
			"Llama 3.1 8B Instruct is gated on Hugging Face. Add a Hugging Face token that has accepted its license in AI settings"},
		{&models.GatedAccessError{Repo: "meta-llama/Llama-3.1-8B-Instruct"}, http.StatusUnprocessableEntity, codeGated,
			(&models.GatedAccessError{Repo: "meta-llama/Llama-3.1-8B-Instruct"}).Error()},
		{&models.BaseGatedError{Repo: "a/b-GGUF", Base: "meta-llama/Llama-3.1-8B-Instruct", NoToken: true}, http.StatusUnprocessableEntity, codeGated,
			(&models.BaseGatedError{Repo: "a/b-GGUF", Base: "meta-llama/Llama-3.1-8B-Instruct", NoToken: true}).Error()},
		{fmt.Errorf("plan: %w", &models.RepinnedError{Resource: "llm", Entry: "Qwen/Qwen2.5-7B-Instruct", Quantization: "bf16", Model: "Qwen/Qwen2.5-7B-Instruct", Revision: strings.Repeat("b", 40), Asked: "Qwen/Qwen2.5-7B-Instruct@" + strings.Repeat("a", 40)}),
			http.StatusUnprocessableEntity, codeRepinned, "llm asks for Qwen/Qwen2.5-7B-Instruct@" + strings.Repeat("a", 40)},

		// What Hugging Face said never reaches the response: each answer
		// gets a fixed sentence.
		{fmt.Errorf("check the token: %w", hfhub.ErrUnavailable), http.StatusBadGateway, codeUnavailable, hubUnavailable},
		{fmt.Errorf("read x/y: %w", hfhub.ErrUnauthorized), http.StatusUnprocessableEntity, codeInvalid, hubUnauthorized},
		{fmt.Errorf("read x/y: %w", hfhub.ErrGated), http.StatusUnprocessableEntity, codeGated, hubGated},
		{fmt.Errorf("read x/y: %w", hfhub.ErrNotFound), http.StatusNotFound, codeNotFound, hubNotFound},
		{fmt.Errorf("read x/y: %w", hfhub.ErrTooLarge), http.StatusUnprocessableEntity, codeInvalid, hubTooLarge},
		{fmt.Errorf("%w: x/y has no config.json, so Loomplane can't work out how much memory it needs", hfhub.ErrRefused), http.StatusUnprocessableEntity, codeInvalid,
			"x/y has no config.json, so Loomplane can't work out how much memory it needs"},
	} {
		got := fail(c.err)
		var api *apiError
		if !errors.As(got, &api) || api.StatusCode() != c.want || api.body.Code != c.code {
			t.Errorf("%v: %v, want %d %s", c.err, got, c.want, c.code)
			continue
		}
		want := c.msg
		if want == "" {
			want = c.err.Error()
		}
		if !strings.HasPrefix(api.body.Message, want) {
			t.Errorf("%v: message %q, want %q", c.err, api.body.Message, want)
		}
		if body, ok := api.ResponseBody().(models.Problem); !ok || body != api.body {
			t.Errorf("%v: body %#v", c.err, api.ResponseBody())
		}
	}
	plain := errors.New("database is down")
	if got := fail(plain); !errors.Is(got, plain) || got.Error() != plain.Error() {
		t.Errorf("unknown error became %v", got)
	}
	if fail(nil) != nil {
		t.Error("fail(nil) isn't nil")
	}
}

// A change whose manifest edit landed and whose plan failed says to plan
// from the project. The plan's reason shows only when it's one fail knows;
// otherwise it may be internal, and is left out.
func TestPlanFailureSaysWhatToDo(t *testing.T) {
	head := "The manifest has llm on its new target now. Plan staging from the project to move it. Planning failed"
	for _, c := range []struct {
		inner error
		want  int
		msg   string
	}{
		{planning.ErrConflict, http.StatusConflict, head + ": " + planning.ErrConflict.Error()},
		{fmt.Errorf("%w (deployment:create)", security.ErrForbidden), http.StatusForbidden, head + ": " + security.ErrForbidden.Error() + " (deployment:create)"},
		{errors.New("pq: connection reset by 10.0.0.7"), http.StatusConflict, head + "."},
		{nil, http.StatusConflict, head},
		// What Hugging Face said is replaced with fail's sentence.
		{fmt.Errorf("read the weights: %w: 500 Internal Server Error: upstream hf-node-7 said hf_tokentoken", hfhub.ErrUnavailable), http.StatusBadGateway, head + ": " + hubUnavailable},
		{fmt.Errorf("%w: x/y has no config.json", models.ErrInvalid), http.StatusUnprocessableEntity, head + ": x/y has no config.json"},
	} {
		msg := head
		if c.inner != nil {
			msg += ": " + c.inner.Error()
		}
		got := planFailedBecause(msg, c.inner)
		var api *apiError
		if !errors.As(got, &api) || api.status != c.want || api.body.Code != codePlanFailed || api.body.Message != c.msg {
			t.Errorf("%v: %#v", c.inner, got)
		}
		if strings.Contains(api.body.Message, "hf-node-7") || strings.Contains(api.body.Message, "hf_tokentoken") {
			t.Errorf("Hugging Face's text in %q", api.body.Message)
		}
	}
	got := fail(&models.PlanAfterEditError{Resource: "llm", Environment: "staging", Removal: true})
	var api *apiError
	if !errors.As(got, &api) || api.body.Code != codePlanFailed || !strings.HasPrefix(api.body.Message, "The manifest no longer has llm. Plan staging from the project to remove it.") {
		t.Errorf("removal: %#v", got)
	}
}

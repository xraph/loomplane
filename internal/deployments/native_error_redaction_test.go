package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/planning"
)

func TestNativeDeliveryRedactsCredentialFieldsAndEscapes(t *testing.T) {
	privateKey := "-----BEGIN PRIVATE KEY-----\nfixture-private-key-material\nsecond-private-key-line\n-----END PRIVATE KEY-----\n"
	googleJSON, err := json.Marshal(map[string]any{"private_key": privateKey, "private_key_id": "google-key-identifier", "client_email": "fixture-account@project.iam.gserviceaccount.com"})
	if err != nil {
		t.Fatal(err)
	}
	job := &Job{
		Credential: []byte(`{"client_secret":"admin-secret-value","nested":{"token":"admin-token-value"}}`),
		Env: map[string]string{
			"DATABASE_URL":                "postgres://app:p%40ss%2Bword@db/app",
			"CACHE_URL":                   "rediss://:cache-secret-value@cache:6380",
			"GOOGLE_SERVICE_ACCOUNT_JSON": string(googleJSON),
			"AWS_ACCESS_KEY_ID":           "AKIAFIXTUREAPPLICATION",
			"AWS_SECRET_ACCESS_KEY":       "fixture/s3+secret=value",
			"AWS_SESSION_TOKEN":           "fixture-session-token-value",
			"LOG_LEVEL":                   "info",
		},
		Secrets: map[string]bool{"DATABASE_URL": true, "CACHE_URL": true, "GOOGLE_SERVICE_ACCOUNT_JSON": true, "AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true},
	}
	redactor := nativeDeliveryRedactor(job)
	secrets := []string{string(job.Credential), "admin-secret-value", "admin-token-value", "p@ss+word", "p%40ss%2Bword", "cache-secret-value", string(googleJSON), privateKey, "fixture-private-key-material", "second-private-key-line", "google-key-identifier", "fixture-account@project.iam.gserviceaccount.com", "AKIAFIXTUREAPPLICATION", "fixture/s3+secret=value", "fixture-session-token-value"}
	for _, secret := range secrets {
		for _, escaped := range []bool{false, true} {
			value := secret
			if escaped {
				encoded, merr := json.Marshal(value)
				if merr != nil {
					t.Fatal(merr)
				}
				value = string(encoded[1 : len(encoded)-1])
			}
			got, _ := redactor.Line("provider validation refused "+value+"; retry info", 1<<20)
			if strings.Contains(got, value) || !strings.Contains(got, "[redacted]") || !strings.Contains(got, "provider validation refused") || !strings.HasSuffix(got, "; retry info") {
				t.Fatalf("credential echo was not redacted (escaped=%t)", escaped)
			}
		}
	}
	if got, _ := redactor.Line("provider quota exceeded; LOG_LEVEL info", 1<<20); got != "provider quota exceeded; LOG_LEVEL info" {
		t.Fatalf("useful nonsecret provider message changed: %s", got)
	}
}

func TestNativeDeliveryBoundaryPreservesClassificationAndRestoresCallbacks(t *testing.T) {
	const secret = "fixture-boundary-secret"
	var progressMessages []string
	var warnings []error
	job := &Job{Env: map[string]string{"TOKEN": secret}, Secrets: map[string]bool{"TOKEN": true},
		Progress: func(_, message string) { progressMessages = append(progressMessages, message) },
		warn:     func(err error) { warnings = append(warnings, err) },
	}
	providerError := &azure.APIError{Status: http.StatusForbidden, Code: "AuthorizationFailed", Message: "provider refused " + secret}
	err := errors.Join(providerError, context.DeadlineExceeded)
	finish := nativeDeliveryBoundary(job)
	job.Progress(StepDeploying, "prune failed: "+err.Error())
	job.Warn(err)
	// Nested calls restore the outer sanitizing scope before its own exit.
	nestedFinish := nativeDeliveryBoundary(job)
	nestedErr := err
	nestedFinish(&nestedErr)
	job.Progress(StepDeploying, "nested finished: "+secret)
	finish(&err)
	var classified *azure.APIError
	if !errors.As(err, &classified) || classified != providerError || !errors.Is(err, context.DeadlineExceeded) || !azure.IsForbidden(err) {
		t.Fatal("sanitizing lost the provider error classification")
	}
	if strings.Contains(fmt.Sprintf("%v %+v %s", err, err, err), secret) || len(warnings) != 1 || strings.Contains(warnings[0].Error(), secret) || !errors.Is(warnings[0], providerError) {
		t.Fatal("public error or warning exposed a credential")
	}
	for _, message := range progressMessages {
		if strings.Contains(message, secret) || !strings.Contains(message, "[redacted]") {
			t.Fatal("public progress exposed a credential")
		}
	}
	job.Progress(StepDeploying, secret)
	job.Warn(providerError)
	if progressMessages[len(progressMessages)-1] != secret || !errors.Is(warnings[len(warnings)-1], providerError) || warnings[len(warnings)-1].Error() != providerError.Error() {
		t.Fatal("the completed native call did not restore the original callbacks")
	}
	var noError error
	nativeDeliveryBoundary(&Job{})(&noError)
	if noError != nil {
		t.Fatal("successful delivery returned an error")
	}
}

func TestAzurePruneProgressDoesNotExposeExternalCredentials(t *testing.T) {
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	if _, err := e.exec.Promote(t.Context(), e.job, target); err != nil {
		t.Fatal(err)
	}
	e.job.Resume = nil
	secret := e.job.Env["TOKEN"]
	listed := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(strings.ToLower(r.URL.Path), "/listsecrets") {
			listed++
			if listed == 2 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "ValidationFailed", "message": "prune refused " + secret}})
				return
			}
		}
		e.f.Server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	e.exec.Client = azure.New(server.URL)
	var messages []string
	e.job.Progress = func(_, message string) { messages = append(messages, message) }
	if _, err := e.exec.Apply(t.Context(), e.job); err != nil {
		t.Fatal(err)
	}
	pruneNote := false
	for _, message := range messages {
		if strings.Contains(message, secret) {
			t.Fatal("public prune progress exposed a credential")
		}
		if strings.Contains(message, "prune refused [redacted]") {
			pruneNote = true
		}
	}
	if !pruneNote {
		t.Fatal("the provider failure did not reach the public prune progress")
	}
}

func TestNativeProviderErrorDoesNotExposeExternalCredentials(t *testing.T) {
	const secret = "external-application-password"
	t.Run("digitalocean", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "provider validation refused " + secret})
		}))
		t.Cleanup(server.Close)
		job := doJob(apiConfig(), "main")
		job.Env = map[string]string{"DATABASE_URL": "postgres://app:" + secret + "@database/app"}
		job.Secrets = map[string]bool{"DATABASE_URL": true}
		_, err := newDO(digitalocean.New(server.URL)).Apply(t.Context(), job)
		var providerError *digitalocean.APIError
		if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "provider validation refused") || !errors.As(err, &providerError) {
			t.Fatalf("public provider error was not safely retained: %v", err)
		}
	})
	for _, operation := range []string{"apply", "candidate", "weights", "promote", "revert"} {
		t.Run("azure/"+operation, func(t *testing.T) {
			fake := azuretest.New(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/oauth2/") {
					fake.Server.Config.Handler.ServeHTTP(w, r)
					return
				}
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "ValidationFailed", "message": "provider validation refused " + secret}})
			}))
			t.Cleanup(server.Close)
			job := azureJob(t, apiConfig())
			job.Env = map[string]string{"DATABASE_URL": "postgres://app:" + secret + "@database/app"}
			job.Secrets = map[string]bool{"DATABASE_URL": true}
			appID := "/subscriptions/" + azuretest.Subscription + "/resourceGroups/loomplane/providers/Microsoft.App/containerApps/api-production-23456789"
			job.Binding = &planning.Binding{ExternalID: appID}
			target := CanaryTarget{Baseline: "api-production-23456789--baseline", Candidate: "api-production-23456789--candidate", Refs: map[string]string{azureRefApp: appID}}
			exec := &Azure{Client: azure.New(server.URL)}
			var err error
			switch operation {
			case "apply":
				_, err = exec.Apply(t.Context(), job)
			case "candidate":
				_, err = exec.ApplyCandidate(t.Context(), job)
			case "weights":
				err = exec.SetWeights(t.Context(), job, target, 10)
			case "promote":
				_, err = exec.Promote(t.Context(), job, target)
			case "revert":
				err = exec.Revert(t.Context(), job, target)
			}
			var providerError *azure.APIError
			if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "provider validation refused") || !errors.As(err, &providerError) {
				t.Fatalf("public provider error was not safely retained: %v", err)
			}
		})
	}
}

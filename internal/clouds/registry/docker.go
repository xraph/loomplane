package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// errNotScoped is a registry that can't mint a short-lived token scoped to
// one repository, which a shared registry must.
var errNotScoped = fmt.Errorf("%w: the registry must mint repository-scoped tokens through a bearer token service with a username and password", ErrInvalid)

// DockerAuth returns a repository token, never a password or identity refresh
// token. Docker's own challenge exchange cannot forward the Vault credential.
func (c *Client) DockerAuth(ctx context.Context, repository, actions string) ([]byte, error) {
	if !repositoryPattern.MatchString(repository) || !strings.HasPrefix(repository, c.Config.Repository+"/") || (actions != "pull" && actions != "pull,push") {
		return nil, fmt.Errorf("%w: image is outside the selected registry prefix", ErrInvalid)
	}
	if c.Config.ScopedOnly && (c.Config.Username == "" || c.Config.Password == "") {
		return nil, errNotScoped
	}
	token := c.Config.Token
	if c.Config.ScopedOnly {
		token = ""
	}
	if c.Config.Username != "" {
		method, path := http.MethodGet, "/v2/"+repository+"/tags/list"
		if actions == "pull,push" {
			method, path = http.MethodPost, "/v2/"+repository+"/blobs/uploads/"
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Config.Endpoint+path, http.NoBody)
		if err != nil {
			return nil, err
		}
		response, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("reach registry for scoped authentication: %w", err)
		}
		challenge := response.Header.Get("WWW-Authenticate")
		_ = response.Body.Close()
		bearer := response.StatusCode == http.StatusUnauthorized && strings.HasPrefix(strings.ToLower(challenge), "bearer ")
		if c.Config.ScopedOnly && !bearer {
			if method == http.MethodPost && response.StatusCode == http.StatusAccepted {
				// Cancel the upload the anonymous probe opened, then refuse.
				if cancelPath, cancelErr := c.uploadPath(response.Header.Get("Location"), "/v2/"+repository); cancelErr == nil {
					if cancel, deleteErr := c.requestFor(context.WithoutCancel(ctx), http.MethodDelete, cancelPath, repository); deleteErr == nil {
						_ = cancel.Body.Close()
					}
				}
			}
			return nil, errNotScoped
		}
		switch {
		case method == http.MethodGet && (response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNotFound):
			token = "" // This repository permits an anonymous read.
		case method == http.MethodPost && response.StatusCode == http.StatusAccepted:
			cancelPath, cancelErr := c.uploadPath(response.Header.Get("Location"), "/v2/"+repository)
			if cancelErr != nil {
				return nil, cancelErr
			}
			cancel, cancelErr := c.requestFor(context.WithoutCancel(ctx), http.MethodDelete, cancelPath, repository)
			if cancelErr != nil {
				return nil, fmt.Errorf("cancel registry authentication probe: %w", cancelErr)
			}
			_ = cancel.Body.Close()
			if cancel.StatusCode != http.StatusNoContent && cancel.StatusCode != http.StatusNotFound {
				return nil, fmt.Errorf("cancel registry authentication probe returned HTTP %d", cancel.StatusCode)
			}
			token = "" // Publish anonymously only after cleaning the empty probe.
		case response.StatusCode == http.StatusUnauthorized && strings.HasPrefix(strings.ToLower(challenge), "bearer "):
			token, err = c.bearerToken(ctx, challenge, repository, actions)
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: Docker publishing and server pulls require a bearer token service or a direct registry token; Basic-only credentials cannot be delegated safely", ErrInvalid)
		}
	}
	if c.Config.ScopedOnly && token == "" {
		return nil, errNotScoped
	}
	return json.Marshal(map[string]string{"serveraddress": c.Config.Endpoint, "registrytoken": token})
}

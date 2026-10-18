// Package modeldiscovery provides the shared retry boundary for read-only model metadata requests.
package modeldiscovery

import (
	"net/http"

	"github.com/openvibely/openvibely/internal/httpretry"
)

// Do uses the standard retry policy. POST metadata queries (such as Ollama
// show) are read-only and explicitly replayable. Callers retain endpoint guards,
// authentication and an overall discovery deadline on the request context.
func Do(client httpretry.Doer, req *http.Request) (*http.Response, error) {
	policy := httpretry.DefaultPolicy()
	policy.AllowReplay = true
	return httpretry.Do(req.Context(), client, func() (*http.Request, error) {
		attempt := req.Clone(req.Context())
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			attempt.Body = body
		}
		return attempt, nil
	}, policy)
}

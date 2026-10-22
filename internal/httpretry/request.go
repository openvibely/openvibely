package httpretry

import "net/http"

// DoRequest applies the general policy to an explicitly replay-authorized
// operation. Bodies created by net/http from strings/bytes are recreated for
// each attempt. Streaming bodies without GetBody are never replayed or buffered.
func DoRequest(client Doer, req *http.Request) (*http.Response, error) {
	policy := DefaultPolicy()
	policy.AllowReplay = true
	return doRequest(client, req, policy)
}

func doRequest(client Doer, req *http.Request, policy Policy) (*http.Response, error) {
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		policy.MaxRetries = 0
	}
	first := true
	return Do(req.Context(), client, func() (*http.Request, error) {
		clone := req.Clone(req.Context())
		if !first && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			clone.Body = body
		}
		first = false
		return clone, nil
	}, policy)
}

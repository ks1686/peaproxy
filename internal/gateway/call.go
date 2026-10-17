package gateway

// Call is the account that served the request and the live model id that
// account was called with. The client-facing id stays on the response body.
type Call struct {
	Account    string
	Deployment string
}

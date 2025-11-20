package controllers

// ErrGatewayNotReady indicates the gateway is not ready to accept routes.
type ErrGatewayNotReady struct {
	msg string
}

func (e *ErrGatewayNotReady) Error() string {
	return "gateway not ready: " + e.msg
}

// ErrGatewayNotFound indicates the gateway was not found.
type ErrGatewayNotFound struct {
	msg string
}

func (e *ErrGatewayNotFound) Error() string {
	return "gateway not found: " + e.msg
}

// ErrRouteNotFound indicates the route was not found.
type ErrRouteNotFound struct {
	msg string
}

func (e *ErrRouteNotFound) Error() string {
	return "route not found: " + e.msg
}

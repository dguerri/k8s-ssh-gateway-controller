package controllers

import "fmt"

type ErrGatewayNotReady struct {
	msg string
}

func (e *ErrGatewayNotReady) Error() string {
	return fmt.Sprintf("gateway not ready: %s", e.msg)
}

type ErrGatewayNotFound struct {
	msg string
}

func (e *ErrGatewayNotFound) Error() string {
	return fmt.Sprintf("gateway not found: %s", e.msg)
}

type ErrRouteNotFound struct {
	msg string
}

func (e *ErrRouteNotFound) Error() string {
	return fmt.Sprintf("route not found: %s", e.msg)
}

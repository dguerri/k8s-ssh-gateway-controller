package controllers

import (
	"testing"
)

func TestErrGatewayNotReady(t *testing.T) {
	err := &ErrGatewayNotReady{msg: "test"}
	if err.Error() != "gateway not ready: test" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestErrGatewayNotFound(t *testing.T) {
	err := &ErrGatewayNotFound{msg: "test"}
	if err.Error() != "gateway not found: test" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestErrRouteNotFound(t *testing.T) {
	err := &ErrRouteNotFound{msg: "test"}
	if err.Error() != "route not found: test" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

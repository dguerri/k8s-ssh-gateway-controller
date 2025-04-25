package controllers

import (
	"errors"
	"time"
)

const requeueBackoff = 3 * time.Second

// ErrNoGateway is returned when no Gateway is found for a Listener.
var ErrNoGateway = errors.New("no Gateway found for Listener")

// ErrNoListener is returned when no Listener is found for a Gateway.
var ErrNoListener = errors.New("no Listener found for Gateway")

// ErrInvalidPort is returned when an invalid port is used.
var ErrInvalidPortForHTTPListener = errors.New("invalid port for HTTP listener")

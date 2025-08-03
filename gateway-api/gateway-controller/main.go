package main

import (
	"os"

	"log/slog"

	ctrl "sigs.k8s.io/controller-runtime"
	zaplog "sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/dguerri/ssh-gateway-api-controller/controllers"
)

func main() {
	level := slog.LevelInfo
	if os.Getenv("SLOG_LEVEL") == "DEBUG" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}),
	))

	// configure controller-runtime logging
	ctrl.SetLogger(zaplog.New(zaplog.UseDevMode(true)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: controllers.SetupScheme(),
	})
	if err != nil {
		slog.Error("unable to start manager", "error", err)
		os.Exit(1)
	}

	// Initialize and register the Gateway controller
	gwRecon := &controllers.GatewayReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	if err = gwRecon.SetupWithManager(mgr); err != nil {
		slog.Error("unable to create Gateway controller", "error", err)
		os.Exit(1)
	}

	if err = (&controllers.HTTPRouteReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		GatewayReconciler: gwRecon,
	}).SetupWithManager(mgr); err != nil {
		slog.Error("unable to create HTTPRoute controller", "error", err)
		os.Exit(1)
	}

	// Initialize and register the TCPRoute controller with reference to the Gateway controller
	tcpRecon := &controllers.TCPRouteReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		GatewayReconciler: gwRecon,
	}
	if err = tcpRecon.SetupWithManager(mgr); err != nil {
		slog.Error("unable to create TCPRoute controller", "error", err)
		os.Exit(1)
	}

	slog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		slog.Error("problem running manager", "error", err)
		os.Exit(1)
	}
}

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/mm503/avahi-dns/internal/avahi"
	"github.com/mm503/avahi-dns/internal/controller"
	"github.com/mm503/avahi-dns/internal/events"
	"github.com/mm503/avahi-dns/internal/hostsfile"
	"github.com/mm503/avahi-dns/internal/reconciler"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		hostsFilePath string
		kubeconfig    string
		cleanupOnExit bool
		avahiService  string
		resyncPeriod  time.Duration
		verbose       bool
		debug         bool
		reload        bool
	)

	flag.StringVar(&hostsFilePath, "hosts-file", "/etc/avahi/hosts", "Path to the avahi hosts file")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (empty = in-cluster)")
	flag.BoolVar(&cleanupOnExit, "cleanup-on-exit", false, "Remove managed block from hosts file on shutdown")
	flag.StringVar(&avahiService, "avahi-service", avahi.DefaultServiceName, "systemd unit name for avahi-daemon (e.g. avahi-daemon.service or avahi.service)")
	flag.DurationVar(&resyncPeriod, "resync-period", 10*time.Minute, "Informer resync period")
	flag.BoolVar(&verbose, "verbose", false, "Log reconcile details: qualifying services, scan summary")
	flag.BoolVar(&debug, "debug", false, "Log everything including Kubernetes client internals (very noisy)")
	flag.BoolVar(&reload, "reload", false, "Signal avahi-daemon to reload via systemd after writing hosts file (avahi watches the file itself by default)")
	flag.Parse()

	// --- logging setup ---
	logLevel := slog.LevelInfo
	if verbose || debug {
		logLevel = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	slog.SetDefault(slog.New(handler))

	// Route klog (used internally by client-go) through slog so we have a
	// single log format and sink. Outside debug mode, gate klog's Info calls
	// via V(10) — logr's Info is suppressed at high verbosities, but Error
	// calls always pass through, so client-go warnings/errors still surface.
	klogLogger := logr.FromSlogHandler(handler)
	if !debug {
		klogLogger = klogLogger.V(10)
	}
	klog.SetLogger(klogLogger)

	// --- startup ---
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		slog.Warn("NODE_NAME env var not set; events will have empty host field")
	}

	cfg, err := buildConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("build kubeconfig: %w", err)
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create kubernetes client: %w", err)
	}

	hostsMgr := &hostsfile.Manager{FilePath: hostsFilePath}
	var reloader avahi.Reloader
	if reload {
		reloader = avahi.NewDefaultReloader(avahiService)
	}
	recorder := events.New(client, nodeName)
	defer recorder.Stop()

	factory := informers.NewSharedInformerFactory(client, resyncPeriod)
	svcInformer := factory.Core().V1().Services()

	rec := &reconciler.Reconciler{
		Lister:   svcInformer.Lister(),
		HostsMgr: hostsMgr,
		Reloader: reloader,
		Recorder: recorder,
		Client:   client,
	}

	ctrl := controller.New(svcInformer.Informer(), rec)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	factory.Start(ctx.Done())

	slog.Info("avahi-controller started", "node", nodeName, "hosts", hostsFilePath, "reload", reload)

	runErr := ctrl.Run(ctx)

	// Cleanup runs regardless of whether ctrl.Run returned an error, since the
	// hosts file may have been written during successful reconciles before the
	// failure. WriteBlock(nil) is idempotent if the managed block isn't present.
	if cleanupOnExit {
		slog.Info("cleanup: removing managed block from hosts file")
		if err := hostsMgr.WriteBlock(nil); err != nil {
			slog.Error("cleanup write failed", "error", err)
		} else if reloader != nil {
			if err := reloader.Reload(); err != nil {
				slog.Error("cleanup reload failed", "error", err)
			}
		}
	}

	slog.Info("avahi-controller stopped")
	if runErr != nil {
		return fmt.Errorf("controller: %w", runErr)
	}
	return nil
}

// buildConfig returns an in-cluster config when kubeconfig is empty,
// otherwise loads the file at the given path.
func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

package main

import (
	"flag"
	"os"

	clusterviewv1 "github.com/girishsg24/k8-cluster-view/api/v1"
	"github.com/girishsg24/k8-cluster-view/internal/controller"
	"github.com/girishsg24/k8-cluster-view/internal/server"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	var apiAddr, crName string
	flag.StringVar(&apiAddr, "api-addr", ":8082", "HTTP API/UI listen address")
	flag.StringVar(&crName, "clusterview-name", "cluster", "ClusterView CR name the API serves")
	flag.Parse()

	ctrl.SetLogger(zap.New())
	log := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Error(err, "add client-go scheme")
		os.Exit(1)
	}
	if err := clusterviewv1.AddToScheme(scheme); err != nil {
		log.Error(err, "add clusterview scheme")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:         scheme,
		Metrics:        metricsserver.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	if err != nil {
		log.Error(err, "unable to create manager")
		os.Exit(1)
	}

	if err := (&controller.ClusterViewReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to set up reconciler")
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		log.Error(err, "unable to build clientset")
		os.Exit(1)
	}

	srv := &server.Server{
		Addr:      apiAddr,
		Clientset: clientset,
		Reader:    mgr.GetAPIReader(),
		CRName:    crName,
	}
	if err := mgr.Add(srv); err != nil {
		log.Error(err, "unable to add HTTP server")
		os.Exit(1)
	}

	log.Info("starting manager", "apiAddr", apiAddr, "clusterViewName", crName)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited with error")
		os.Exit(1)
	}
}

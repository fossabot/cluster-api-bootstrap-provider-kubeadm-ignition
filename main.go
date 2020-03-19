/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"github.com/minsheng-fintech-corp-ltd/cluster-api-bootstrap-provider-kubeadm-ignition/ignition"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	kubeadmbootstrapcontrollers "github.com/minsheng-fintech-corp-ltd/cluster-api-bootstrap-provider-kubeadm-ignition/controllers"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	"k8s.io/klog/klogr"
	clusterv1alpha3 "sigs.k8s.io/cluster-api/api/v1alpha3"
	kubeadmbootstrapv1alpha2 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1alpha2"
	kubeadmbootstrapv1alpha3 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1alpha3"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	klog.InitFlags(nil)

	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterv1alpha3.AddToScheme(scheme)
	_ = kubeadmbootstrapv1alpha2.AddToScheme(scheme)
	_ = kubeadmbootstrapv1alpha3.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

var (
	metricsAddr              string
	enableLeaderElection     bool
	watchNamespace           string
	profilerAddress          string
	kubeadmConfigConcurrency int
	syncPeriod               time.Duration
	webhookPort              int
	userDataBucket           string
	userdataDir              string
)

func main() {
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080",
		"The address the metric endpoint binds to.")

	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")

	flag.StringVar(&watchNamespace, "namespace", "",
		"Namespace that the controller watches to reconcile cluster-api objects. If unspecified, the controller watches for cluster-api objects across all namespaces.")

	flag.StringVar(&profilerAddress, "profiler-address", "",
		"Bind address to expose the pprof profiler (e.g. localhost:6060)")

	flag.IntVar(&kubeadmConfigConcurrency, "kubeadmconfig-concurrency", 10,
		"Number of kubeadm configs to process simultaneously")

	flag.DurationVar(&syncPeriod, "sync-period", 10*time.Minute,
		"The minimum interval at which watched resources are reconciled (e.g. 15m)")

	flag.DurationVar(&kubeadmbootstrapcontrollers.DefaultTokenTTL, "bootstrap-token-ttl", 15*time.Minute,
		"The amount of time the bootstrap token will be valid")

	flag.IntVar(&webhookPort, "webhook-port", 0,
		"Webhook Server port, disabled by default. When enabled, the manager will only work as webhook server, no reconcilers are installed.")

	flag.StringVar(
		&userDataBucket,
		"ignition-userdata-bucket",
		"container-service-demo",
		"The bucket the userdata ignition file resides",
	)
	flag.StringVar(
		&userdataDir,
		"ignition-userdata-dir",
		"node-userdata",
		"The bucket the userdata ignition file resides",
	)

	flag.Parse()

	ctrl.SetLogger(klogr.New())

	if profilerAddress != "" {
		klog.Infof("Profiler listening for requests at %s", profilerAddress)
		go func() {
			klog.Info(http.ListenAndServe(profilerAddress, nil))
		}()
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "kubeadm-bootstrap-manager-leader-election-capi",
		Namespace:          watchNamespace,
		SyncPeriod:         &syncPeriod,
		NewClient:          newClientFunc,
		Port:               webhookPort,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}
	templateBackend, err := ignition.NewS3TemplateBackend(userdataDir, userDataBucket)
	if err != nil {
		setupLog.Error(err, "unable to create aws s3 session")
		os.Exit(1)
	}
	setupWebhooks(mgr)
	setupReconcilers(mgr, templateBackend)

	// +kubebuilder:scaffold:builder
	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func setupReconcilers(mgr ctrl.Manager, templateBackend ignition.TemplateBackend) {
	if webhookPort != 0 {
		return
	}

	if err := (&kubeadmbootstrapcontrollers.KubeadmConfigReconciler{
		Client:          mgr.GetClient(),
		Log:             ctrl.Log.WithName("controllers").WithName("KubeadmConfig"),
		IgnitionFactory: ignition.NewFactory(templateBackend),
	}).SetupWithManager(mgr, concurrency(kubeadmConfigConcurrency)); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KubeadmConfig")
		os.Exit(1)
	}
}

func setupWebhooks(mgr ctrl.Manager) {
	if webhookPort == 0 {
		return
	}

	if err := (&kubeadmbootstrapv1alpha3.KubeadmConfig{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "KubeadmConfig")
		os.Exit(1)
	}
	if err := (&kubeadmbootstrapv1alpha3.KubeadmConfigList{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "KubeadmConfigList")
		os.Exit(1)
	}
	if err := (&kubeadmbootstrapv1alpha3.KubeadmConfigTemplate{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "KubeadmConfigTemplate")
		os.Exit(1)
	}
	if err := (&kubeadmbootstrapv1alpha3.KubeadmConfigTemplateList{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "KubeadmConfigTemplateList")
		os.Exit(1)
	}
}

func concurrency(c int) controller.Options {
	return controller.Options{MaxConcurrentReconciles: c}
}

// newClientFunc returns a client reads from cache and write directly to the server
// this avoid get unstructured object directly from the server
// see issue: https://github.com/kubernetes-sigs/cluster-api/issues/1663
func newClientFunc(cache cache.Cache, config *rest.Config, options client.Options) (client.Client, error) {
	// Create the Client for Write operations.
	c, err := client.New(config, options)
	if err != nil {
		return nil, err
	}

	return &client.DelegatingClient{
		Reader:       cache,
		Writer:       c,
		StatusClient: c,
	}, nil
}

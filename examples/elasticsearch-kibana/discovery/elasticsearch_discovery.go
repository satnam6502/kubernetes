package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client/cache"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client/clientcmd"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/controller/framework"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/fields"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/labels"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/runtime"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/watch"

	"github.com/golang/glog"
)

func main() {

	spec := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	settings, err := clientcmd.LoadFromFile(spec)
	if err != nil {
		glog.Fatalf("Error loading configuration: %v", err.Error())
	}

	glog.Infof("Current context: %s", settings.CurrentContext)

	config, err := clientcmd.NewDefaultClientConfig(*settings, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		glog.Fatalf("Failed to construct config: %v", err)
	}

	c, err := client.New(config)
	if err != nil {
		glog.Fatalf("Failed to make client: %v", err)
	}

	pods, err := c.Pods("mytunes").List(labels.Everything(), fields.Everything())
	if err != nil {
		glog.Fatalf("Failed to list pods: %v", err)
	}
	glog.Infof("Initial pods")
	for i, p := range pods.Items {
		glog.Infof("%d: %v", i, p.Name)
	}
	glog.Info("[END]")

	_, controller := framework.NewInformer(
		&cache.ListWatch{
			ListFunc: func() (runtime.Object, error) {
				return c.Pods("mytunes").List(labels.Everything(), fields.Everything())
			},
			WatchFunc: func(rv string) (watch.Interface, error) {
				return c.Pods("mytunes").Watch(labels.Everything(), fields.Everything(), rv)
			},
		},
		&api.Pod{},
		time.Millisecond*100,
		framework.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				glog.Infof("Added %+v", obj)
			},
			DeleteFunc: func(obj interface{}) {
				glog.Infof("Deleted %+v", obj)
				key, err := framework.DeletionHandlingMetaNamespaceKeyFunc(obj)
				if err != nil {
					key = "oops something went wrong with the key"
				}
				glog.Infof("Deletion key: %s", key)
			},
			UpdateFunc: func(obj1 interface{}, obj2 interface{}) {
				glog.Infof("Update obj1 = %+v", obj1)
				glog.Infof("Update obj2 = %+v", obj2)
				pod2, ok := obj1.(*api.Pod)
				if !ok {
					glog.Fatalf("Decode failed")
				}
				glog.Infof("PodIP: %s", pod2.Status.PodIP)
			},
		},
	)

	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(stop)
	select {}
}

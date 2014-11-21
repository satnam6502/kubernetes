package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/clientauth"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/labels"
)

var (
	authPath  = flag.String("auth_path", "", "Path to .kubernetes_auth file, specifying how to authenticate to API server.")
	apiServer = flag.String("api_server", "", "IP address of API server")
	size      = flag.Int("size", 100, "number of pods to create")
        name      = flag.String("name", "pod", "root name for pods")
)

func main() {

	flag.Parse()

	if *apiServer == "" {
		log.Fatal("Please specify a value for the api_server flag")
	}

	if *authPath == "" {
		*authPath = filepath.Join(os.Getenv("HOME"), ".kubernetes_auth")
	}

	authInfo, err := clientauth.LoadFromFile(*authPath)
	if err != nil {
		log.Fatalf("failed to load auth information: %v")
	}

	config, err := authInfo.MergeWithConfig(client.Config{})
	if err != nil {
		log.Fatalf("failed to merge auth info: %v", err)
	}

	config.Host = fmt.Sprintf("%s:443", *apiServer)

	client, err := client.New(&config)
	if err != nil {
		log.Fatalf("failed to make client: %v", err)
	}

	podIntf := client.Pods(api.NamespaceDefault)

	fmt.Fprint(os.Stderr, "Pods before:\n")
	pods, err := podIntf.List(labels.Everything())
	if err != nil {
		log.Fatalf("failed to get pods: %v", err)
	}
	for i, pod := range pods.Items {
		fmt.Printf("%d: %s\n", i, pod.Name)
	}

	for i := 0; i < *size; i++ {

		pod := &api.Pod{
			TypeMeta: api.TypeMeta{
				Kind:       "Pod",
				APIVersion: "v1beta1",
			},
			ObjectMeta: api.ObjectMeta{
				Name: fmt.Sprintf("%s_%d", *name, i),
			},
			Spec: api.PodSpec{
				Containers: []api.Container{
					api.Container{Name: "pause",
						Image: "kubernetes/pause",
					},
				},
			},
		}

		fmt.Fprintf(os.Stderr, "Creating %s...", fmt.Sprintf("$s_%d", *name, i))
		_, err := podIntf.Create(pod)
		if err != nil {
			log.Fatalf("failed to create pod: %v", err)
		}
		fmt.Fprint(os.Stderr, "\n")

		if i%10 == 0 || i+1 == *size {
			startTime := time.Now()
			_, err = podIntf.List(labels.Everything())
			elapsed := time.Since(startTime)
			if err != nil {
				log.Fatalf("failed to get pods: %v", err)
			}
			fmt.Printf("%d\t%v\t%v\n", (i/10+1)*10, elapsed.Seconds(), float64(i+1)/elapsed.Seconds())
		}
	}

	fmt.Fprint(os.Stderr, "Pods created:\n")
	pods, err = podIntf.List(labels.Everything())
	if err != nil {
		log.Fatalf("failed to get pods: %v", err)
	}
	for i, pod := range pods.Items {
		fmt.Fprintf(os.Stderr, "%d: %s\n", i, pod.Name)
	}

}

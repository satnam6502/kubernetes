package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/clientauth"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/labels"
)

var (
	authPath  = flag.String("auth_path", "", "Path to .kubernetes_auth file, specifying how to authenticate to API server.")
	apiServer = flag.String("api_server", "", "IP address of API server")
)

func main() {

	flag.Parse()

	if *apiServer == "" {
		log.Fatal("Please specify a value for the api_server flag")
	}

	ns := api.NamespaceDefault

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

	podIntf := client.Pods(ns)

	fmt.Print("Pods before:\n")
	pods, err := podIntf.List(labels.Everything())
	if err != nil {
		log.Fatalf("failed to get pods: %v", err)
	}
	for i, pod := range pods.Items {
		fmt.Printf("%d: %s\n", i, pod.Name)
	}

	for _, pod := range pods.Items {
		fmt.Print(fmt.Sprintf("Deleting %s...", pod.Name))
		err = podIntf.Delete(pod.Name)
		if err != nil {
			log.Fatalf("failed to delete pod %s: %v", pod.Name, err)
		}
		fmt.Print("\n")
	}

	fmt.Print("Pods after:\n")
	pods, err = podIntf.List(labels.Everything())
	if err != nil {
		log.Fatalf("failed to get pods: %v", err)
	}
	for i, pod := range pods.Items {
		fmt.Printf("%d: %s\n", i, pod.Name)
	}

}

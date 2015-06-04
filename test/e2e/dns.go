/*
Copyright 2015 The Kubernetes Authors All rights reserved.

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

package e2e

import (
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/latest"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/fields"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/labels"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util/wait"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var dnsServiceLableSelector = labels.Set{
	"k8s-app":                       "kube-dns",
	"kubernetes.io/cluster-service": "true",
}.AsSelector()

var _ = Describe("DNS", func() {
	f := NewFramework("dns")

	It("should provide DNS for the cluster", func() {
		if providerIs("vagrant") {
			By("Skipping test which is broken for vagrant (See https://github.com/GoogleCloudPlatform/kubernetes/issues/3580)")
			return
		}

		podClient := f.Client.Pods(api.NamespaceDefault)

		By("Waiting for DNS Service to be Running")
		dnsPods, err := podClient.List(dnsServiceLableSelector, fields.Everything())
		if err != nil {
			Failf("Failed to list all dns service pods")
		}
		if len(dnsPods.Items) != 1 {
			Failf("Unexpected number of pods (%d) matches the label selector %v", len(dnsPods.Items), dnsServiceLableSelector.String())
		}
		expectNoError(waitForPodRunning(f.Client, dnsPods.Items[0].Name))

		// All the names we need to be able to resolve.
		// TODO: Spin up a separate test service and test that dns works for that service.
		namesToResolve := []string{
			"kubernetes.default",
			"kubernetes.default.svc",
			"kubernetes.default.svc.cluster.local",
			"kubernetes.default.cluster.local",
			"google.com",
		}
		// Added due to #8512. This is critical for GCE and GKE deployments.
		if providerIs("gce", "gke") {
			namesToResolve = append(namesToResolve, "metadata")
		}

		probeCmd := "for i in `seq 1 600`; do "
		for _, name := range namesToResolve {
			// Resolve by TCP and UDP DNS.  Use $$(...) because $(...) is
			// expanded by kubernetes (though this won't expand so should
			// remain a literal, safe > sorry).
			probeCmd += fmt.Sprintf(`test -n "$$(dig +notcp +noall +answer +search %s)" && echo OK > /results/udp@%s;`, name, name)
			probeCmd += fmt.Sprintf(`test -n "$$(dig +tcp +noall +answer +search %s)" && echo OK > /results/tcp@%s;`, name, name)
		}
		probeCmd += "sleep 1; done"

		// Run a pod which probes DNS and exposes the results by HTTP.
		By("creating a pod to probe DNS")
		pod := &api.Pod{
			TypeMeta: api.TypeMeta{
				Kind:       "Pod",
				APIVersion: latest.Version,
			},
			ObjectMeta: api.ObjectMeta{
				Name:      "dns-test-" + string(util.NewUUID()),
				Namespace: f.Namespace.Name,
			},
			Spec: api.PodSpec{
				Volumes: []api.Volume{
					{
						Name: "results",
						VolumeSource: api.VolumeSource{
							EmptyDir: &api.EmptyDirVolumeSource{},
						},
					},
				},
				Containers: []api.Container{
					// TODO: Consider scraping logs instead of running a webserver.
					{
						Name:  "webserver",
						Image: "gcr.io/google_containers/test-webserver",
						VolumeMounts: []api.VolumeMount{
							{
								Name:      "results",
								MountPath: "/results",
							},
						},
					},
					{
						Name:    "querier",
						Image:   "gcr.io/google_containers/dnsutils",
						Command: []string{"sh", "-c", probeCmd},
						VolumeMounts: []api.VolumeMount{
							{
								Name:      "results",
								MountPath: "/results",
							},
						},
					},
				},
			},
		}

		By("submitting the pod to kubernetes")
		podClient = f.Client.Pods(f.Namespace.Name)
		defer func() {
			By("deleting the pod")
			defer GinkgoRecover()
			podClient.Delete(pod.Name, nil)
		}()
		if _, err := podClient.Create(pod); err != nil {
			Failf("Failed to create %s pod: %v", pod.Name, err)
		}

		expectNoError(f.WaitForPodRunning(pod.Name))

		By("retrieving the pod")
		pod, err = podClient.Get(pod.Name)
		if err != nil {
			Failf("Failed to get pod %s: %v", pod.Name, err)
		}

		// Try to find results for each expected name.
		By("looking for the results for each expected name")
		var failed []string

		expectNoError(wait.Poll(time.Second*2, time.Second*60, func() (bool, error) {
			failed = []string{}
			for _, name := range namesToResolve {
				for _, proto := range []string{"udp", "tcp"} {
					testCase := fmt.Sprintf("%s@%s", proto, name)
					_, err := f.Client.Get().
						Prefix("proxy").
						Resource("pods").
						Namespace(f.Namespace.Name).
						Name(pod.Name).
						Suffix("results", testCase).
						Do().Raw()
					if err != nil {
						failed = append(failed, testCase)
					}
				}
			}
			if len(failed) == 0 {
				return true, nil
			}
			Logf("Lookups using %s failed for: %v\n", pod.Name, failed)
			return false, nil
		}))
		Expect(len(failed)).To(Equal(0))

		// TODO: probe from the host, too.

		Logf("DNS probes using %s succeeded\n", pod.Name)
	})
	It("should provide DNS for headless services", func() {
		if providerIs("vagrant") {
			By("Skipping test which is broken for vagrant (See https://github.com/GoogleCloudPlatform/kubernetes/issues/3580)")
			return
		}

		podClient := f.Client.Pods(api.NamespaceDefault)

		By("Waiting for DNS Service to be Running")
		dnsPods, err := podClient.List(dnsServiceLableSelector, fields.Everything())
		if err != nil {
			Failf("Failed to list all dns service pods")
		}
		if len(dnsPods.Items) != 1 {
			Failf("Unexpected number of pods (%d) matches the label selector %v", len(dnsPods.Items), dnsServiceLableSelector.String())
		}
		expectNoError(waitForPodRunning(f.Client, dnsPods.Items[0].Name))

		// Create a test headless service.
		By("Creating a test headless service")
		testServiceName := "test-service"
		testServiceSelector := map[string]string{
			"dns-test": "true",
		}
		svc := &api.Service{
			ObjectMeta: api.ObjectMeta{
				Name: testServiceName,
			},
			Spec: api.ServiceSpec{
				ClusterIP: "None",
				Ports: []api.ServicePort{
					{Port: 80},
				},
				Selector: testServiceSelector,
			},
		}

		_, err = f.Client.Services(f.Namespace.Name).Create(svc)
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			By("deleting the test headless service")
			defer GinkgoRecover()
			f.Client.Services(f.Namespace.Name).Delete(svc.Name)
		}()

		// All the names we need to be able to resolve.
		// TODO: Create more endpoints and ensure that multiple A records are returned
		// for headless service.
		namesToResolve := []string{
			fmt.Sprintf("%s", testServiceName),
			fmt.Sprintf("%s.%s", testServiceName, f.Namespace.Name),
			fmt.Sprintf("%s.%s.svc", testServiceName, f.Namespace.Name),
		}

		probeCmd := "for i in `seq 1 600`; do "
		for _, name := range namesToResolve {
			// Resolve by TCP and UDP DNS.  Use $$(...) because $(...) is
			// expanded by kubernetes (though this won't expand so should
			// remain a literal, safe > sorry).
			probeCmd += fmt.Sprintf(`test -n "$$(dig +notcp +noall +answer +search %s)" && echo OK > /results/udp@%s;`, name, name)
			probeCmd += fmt.Sprintf(`test -n "$$(dig +tcp +noall +answer +search %s)" && echo OK > /results/tcp@%s;`, name, name)
		}
		probeCmd += "sleep 1; done"
		// Run a pod which probes DNS and exposes the results by HTTP.
		By("creating a pod to probe DNS")
		pod := &api.Pod{
			TypeMeta: api.TypeMeta{
				Kind:       "Pod",
				APIVersion: latest.Version,
			},
			ObjectMeta: api.ObjectMeta{
				Name:   "dns-test",
				Labels: testServiceSelector,
			},
			Spec: api.PodSpec{
				Volumes: []api.Volume{
					{
						Name: "results",
						VolumeSource: api.VolumeSource{
							EmptyDir: &api.EmptyDirVolumeSource{},
						},
					},
				},
				Containers: []api.Container{
					// TODO: Consider scraping logs instead of running a webserver.
					{
						Name:  "webserver",
						Image: "gcr.io/google_containers/test-webserver",
						VolumeMounts: []api.VolumeMount{
							{
								Name:      "results",
								MountPath: "/results",
							},
						},
					},
					{
						Name:    "querier",
						Image:   "gcr.io/google_containers/dnsutils",
						Command: []string{"sh", "-c", probeCmd},
						VolumeMounts: []api.VolumeMount{
							{
								Name:      "results",
								MountPath: "/results",
							},
						},
					},
				},
			},
		}

		By("submitting the pod to kubernetes")
		podClient = f.Client.Pods(f.Namespace.Name)
		defer func() {
			By("deleting the pod")
			defer GinkgoRecover()
			podClient.Delete(pod.Name, nil)
		}()
		if _, err := podClient.Create(pod); err != nil {
			Failf("Failed to create %s pod: %v", pod.Name, err)
		}

		expectNoError(f.WaitForPodRunning(pod.Name))

		By("retrieving the pod")
		pod, err = podClient.Get(pod.Name)
		if err != nil {
			Failf("Failed to get pod %s: %v", pod.Name, err)
		}

		// Try to find results for each expected name.
		By("looking for the results for each expected name")
		var failed []string

		expectNoError(wait.Poll(time.Second*2, time.Second*60, func() (bool, error) {
			failed = []string{}
			for _, name := range namesToResolve {
				for _, proto := range []string{"udp", "tcp"} {
					testCase := fmt.Sprintf("%s@%s", proto, name)
					_, err := f.Client.Get().
						Prefix("proxy").
						Resource("pods").
						Namespace(f.Namespace.Name).
						Name(pod.Name).
						Suffix("results", testCase).
						Do().Raw()
					if err != nil {
						failed = append(failed, testCase)
					}
				}
			}
			if len(failed) == 0 {
				return true, nil
			}
			Logf("Lookups using %s failed for: %v\n", pod.Name, failed)
			return false, nil
		}))
		Expect(len(failed)).To(Equal(0))

		// TODO: probe from the host, too.

		Logf("DNS probes using %s succeeded\n", pod.Name)
	})

})

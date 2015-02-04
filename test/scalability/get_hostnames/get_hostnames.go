/*
Copyright 2015 Google Inc. All rights reserved.

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
	"fmt"
	"io/ioutil"
	"net/http"
)

var (
	host = flag.String("host", "", "Address of serve hostname service")
	n    = flag.Int("n", 10000, "Number of queries")
)

func main() {
	flag.Parse()
	m := make(map[string]int)
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s:9500", *host), nil)
	tr := &http.Transport{}
	client := &http.Client{Transport: tr}
	if err != nil {
		fmt.Errorf("%v", err)
	}
	for i := 0; i < *n; i++ {
		resp, err := client.Do(req)
		if err != nil {
			fmt.Errorf("%v", err)
		}
		if resp == nil || resp.Body == nil {
			m["resperr"]++
			continue
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			fmt.Errorf("%v", err)
		}
		if resp.StatusCode != http.StatusOK {
			m[resp.Status]++
			continue
		}
		m[string(body)]++
		t := client.Transport.(*http.Transport)
		t.CloseIdleConnections()
	}
	for k, v := range m {
		fmt.Printf("%s\t%d\n", k, v)
	}
}

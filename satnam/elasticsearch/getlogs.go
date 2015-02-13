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
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
)

var (
	esHost = flag.String("es", "", "URL of Elasticsearch service")
)

type LogStash struct {
	Log       string `json:"log"`
	Stream    string `json:"stream"`
	Tag       string `json:"tag"`
	Timestamp string `json:"@timestamp"`
}

func get(uri string) string {
	resp, err := http.Get(uri)
	if err != nil {
		fmt.Errorf("%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Errorf("%v", resp.Status)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Errorf("%v", err)
	}
	return string(body)
}

func main() {
	flag.Parse()
	fmt.Printf("%s\n", get(fmt.Sprintf("http://%s:9200", *esHost)))
	fmt.Printf("%s\n", get(fmt.Sprintf("http://%s:9200/_cat/indices?v", *esHost)))
	fmt.Print("Logstash:\n")
	fmt.Printf("%s\n", get(fmt.Sprintf("http://%s:9200/logstash-*", *esHost)))
	query := []byte(`{
     "query" : {
          "term" : { "log" : "synthlgr0_25" }
     },
     "size" : 100
  }`)
	fmt.Printf("Query response:\n")
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s:9200/_search", *esHost), bytes.NewBuffer(query))
	if err != nil {
		fmt.Errorf("Failed to form search request: %v", err)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Errorf("Failed Do: %v", err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Errorf("Failed to ready body of response: %v", err)
	}
	fmt.Print("Body:\n")
	fmt.Printf("%s\n", body)
	fmt.Print("End of body.\n")
	var r interface{}
	if err = json.Unmarshal(body, &r); err != nil {
		fmt.Errorf("Failed to unmarshal response: %v")
	}
	var rm map[string]interface{}
	rm = r.(map[string]interface{})
	hits := rm["hits"].(map[string]interface{})
	total := hits["total"].(float64)
	fmt.Printf("Total :%f\n", total)
	hits2 := hits["hits"].([]interface{})
	fmt.Print("JSON\n")
	for k, v := range hits2 {
		fmt.Printf("hits #%d\n", k)
		vr := v.(map[string]interface{})
		source := (vr["_source"]).(map[string]interface{})
		l := source["log"].(string)
		fmt.Printf("%v\n", l)
	}

}

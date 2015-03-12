#!/bin/bash

# Copyright 2014 Google Inc. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# A library of helper functions and constant for the local config.

# Use the config file specified in $KUBE_CONFIG_FILE, or default to
# config-default.sh.

password=$(kubectl config view -o template --template='{{ index . "users" "kubernetes-satnam_kubernetes" "password" }}')
master=$(kubectl config view -o template --template='{{ index . "clusters" "kubernetes-satnam_kubernetes" "server" }}')
curl -X GET -k -u admin:"${password}" "${master}"/api/v1beta1/services/redis-master 

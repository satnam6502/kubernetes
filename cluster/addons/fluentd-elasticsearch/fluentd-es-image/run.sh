#!/bin/bash

# Copyright 2015 The Kubernetes Authors All rights reserved.
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

grep token /etc/token-system-logging/kubeconfig | awk '{print $2}' > /etc/td-agent/token
sed -i -e "s/KUBERNETES_SERVICE_HOST/${KUBERNETES_SERVICE_HOST}/" /etc/td-agent/td-agent.conf
sed -i -e "s/KUBERNETES_SERVICE_PORT/${KUBERNETES_SERVICE_PORT}/" /etc/td-agent/td-agent.conf
/usr/sbin/td-agent "$FLUENTD_ARGS" > /var/log/td-agent/td-agent.log
sleep infinity
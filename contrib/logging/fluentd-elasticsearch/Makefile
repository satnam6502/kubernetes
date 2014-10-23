up:	
	-kubectl.sh create -f go-ws-pod.yml
	-kubectl.sh create -f go-ws-service.yml
	-kubectl.sh create -f es-pod.yml
	-kubectl.sh create -f es-service.yml
	# The Elasticsearch service must be running before the Kibana
	# service is deployed.
	-kubectl.sh create -f kibana-pod.yml
	-kubectl.sh create -f kibana-service.yml


down:	
	-kubectl.sh delete pods go-webserver-pod 
	-kubectl.sh delete service go-webserver-service
	-kubectl.sh delete pods elasticsearch-pod
	-kubectl.sh delete service elasticsearch
	-kubectl.sh delete pods kibana-pod
	-kubectl.sh delete service kibana-service

get:	
	kubectl.sh get pods
	kubectl.sh get services

net:	
	gcutil getforwardingrule go-webserver-service
	gcutil getforwardingrule elasticsearch
	gcutil getforwardingrule kibana-service

firewall:	
	gcutil addfirewall --allowed=tcp:8080,tcp:8085,tcp:8090,tcp:9200,tcp:9300 --target_tags=kubernetes-minion kubernetes-elk-example


rmfirewall:
	gcutil deletefirewall -f kubernetes-elk-example


up:	
	-kubectl.sh create -f go-ws-pod.yml
	-kubectl.sh create -f go-ws-service.yml
	-kubectl.sh create -f es-pod.yml
	-kubectl.sh create -f kibana-pod.yml


down:	
	-kubectl.sh delete pods go-webserver
	-kubectl.sh delete pods elasticsearch
	-kubectl.sh delete pods kibana
	-kubectl.sh delete pods synthetic-logger-0.25lps
	-kubectl.sh delete service go-webserver
	-kubectl.sh delete service elastic-search
	-kubectl.sh delete service kibana-service

get:	
	kubectl.sh get pods
	kubectl.sh get services

net:	
	gcutil getforwardingrule go-webserver

add:	
	gcutil addfirewall --allowed=tcp:8085 --target_tags=kubernetes-minion kubernetes-minion-8085

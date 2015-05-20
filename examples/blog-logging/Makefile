

create:
	kubectl create -f mytunes-pod-v1.yaml --namespace=mytunes

delete:
	kubectl delete pod mytunes-v1 --namespace=mytunes

mkservice:
	kubectl create -f mytunes-service.yaml --namespace=mytunes

get:
	kubectl get pods,services,endpoints --namespace=mytunes

json:
	kubectl get pod -o json mytunes-v1 --namespace=mytunes

firewalls:
	gcloud compute firewall-rules create mytunes --allow tcp:9200,tcp:5601

counter:
	 kubectl create -f counter-pod.yaml

stop:
	kubectl stop pod counter
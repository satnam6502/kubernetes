API_SERVER=146.148.79.36
SIZE=10
NAME=wombat
KUBECTL=../../cluster/kubectl.sh

all:	
	cd createpods; go build
	cd deletepods; go build
	cd getpods; go build


create:	
	createpods/createpods --api_server=$(API_SERVER) --size=$(SIZE) --name=$(NAME)

getp:	
	getpods/getpods --api_server=$(API_SERVER) --size=$(SIZE) --name=$(NAME)

delete:	
	deletepods/deletepods --api_server=$(API_SERVER)

get:	
	$(KUBECTL) get pods

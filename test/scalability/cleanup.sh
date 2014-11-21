#!/bin/bash -u


ZONE=us-central1-f

 gcloud compute instances delete --zone=${ZONE} --delete-disks=all --q `gcloud compute instances list | grep kubernetes-minion- | awk '{print $1}'`

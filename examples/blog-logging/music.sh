#!/bin/bash
readonly ES=http://104.197.86.235:9200
# readonly ES=http://$(kubectl get service mytunes --namespace=mytunes -o template --template='{{ index .spec.publicIPs 0 }}'):9200
#readonly ES="https://130.211.168.167/api/v1beta3/proxy/namespaces/mytunes/services/mytunes:db"
#readonly CURL_ARGS="-k -H 'Authorization: Bearer pTQOUv3tFMsmF6VSvx2nQcLvoMxW1UzY'"
echo ${CURL_ARGS}
echo "Submitting music to Elasticsearch server at ${ES}"
curl $CURL_ARGS -XDELETE "${ES}/music/album"
curl $CURL_ARGS -XPUT "${ES}/music/album/1" \
	  -d '{"artist": "Portugal. The Man", "alubm": "Evil Friends", "year": 2013}'
curl $CURL_ARGS -XPUT "${ES}/music/album/2" \
	  -d '{"artist": "Portugal. The Man", "alubm": "In the Mountain  in the Cloud", "year": 2011}'
curl $CURL_ARGS -XPUT "${ES}/music/album/3" \
	  -d '{"artist": "Metric", "alubm": "Fantasies", "year": 2009}'
curl $CURL_ARGS -XPUT "${ES}/music/album/4" \
	  -d '{"artist": "The Vaselines", "alubm": "V for Vaselines", "year": 2014}'
curl $CURL_ARGS -XPUT "${ES}/music/album/5" \
	  -d '{"artist": "Bo Kaspers Orkester", "alubm": "Amerika", "year": 1996}'

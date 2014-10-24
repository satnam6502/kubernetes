# A variation of the arcus/kibana Dockerfile for use with
# an Elasticsearch service configured on Kubernetes.

FROM ubuntu:14.04
MAINTAINER Satnam Singh "satnam@google.com"

ENV DEBIAN_FRONTEND noninteractive

RUN apt-get update && \
    apt-get install -y nginx-full curl && \
    apt-get clean

RUN curl -O http://download.elasticsearch.org/kibana/kibana/kibana-3.1.1.tar.gz && \
    tar xf kibana-3.1.1.tar.gz && \
    mv kibana-3.1.1/* /usr/share/nginx/html && \
    rm kibana-3.1.1.tar.gz

RUN echo "daemon off;" >> /etc/nginx/nginx.conf

ADD default /etc/nginx/sites-available/default
ADD run.sh /usr/local/bin/run.sh

EXPOSE 80
CMD ["/usr/local/bin/run.sh"]

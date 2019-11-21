FROM centos:7

ADD webhook /bin/webhook
RUN chmod +x /bin/webhook
ENTRYPOINT ["/webhook"]
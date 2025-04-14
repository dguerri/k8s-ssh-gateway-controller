FROM alpine:3
LABEL name=pico-sh-ingress-controller.sh
LABEL version=1.8

RUN apk add --no-cache bash autossh jq kubectl

WORKDIR /

COPY controller.sh /controller.sh

RUN chmod +x /controller.sh

ENTRYPOINT ["/controller.sh"]

FROM alpine:3
LABEL name=pico-ingress.controller.sh
LABEL version=1.6

RUN apk add --no-cache bash autossh openssh jq kubectl curl

WORKDIR /

COPY controller.sh /controller.sh

RUN chmod +x /controller.sh

ENTRYPOINT ["/controller.sh"]

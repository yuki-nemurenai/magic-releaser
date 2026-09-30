FROM alpine:3.24

RUN apk add --no-cache \
    ca-certificates \
    git

ARG TARGETPLATFORM

COPY --link $TARGETPLATFORM/magic-releaser /usr/local/bin/magic-releaser

ENTRYPOINT ["magic-releaser"]

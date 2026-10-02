FROM alpine:3.24

ARG TARGETPLATFORM

COPY --link $TARGETPLATFORM/magic-releaser /usr/local/bin/magic-releaser

ENTRYPOINT ["magic-releaser"]

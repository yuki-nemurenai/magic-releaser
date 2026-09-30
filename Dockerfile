# Image for CI jobs. git is included because CI scripts configure the commit
# identity with git config; magic-releaser itself talks to git through go-git.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates git
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/magic-releaser /usr/local/bin/magic-releaser
ENTRYPOINT ["magic-releaser"]

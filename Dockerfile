ARG BASE_IMAGE=alpine:3.13


#Build stage
FROM golang:1.18-alpine3.15 AS build-env

ARG GOPROXY
ENV GOPROXY ${GOPROXY:-direct}

ARG GITEA_VERSION
ARG TAGS="sqlite sqlite_unlock_notify"
ENV TAGS "bindata timetzdata $TAGS"
ARG CGO_EXTRA_CFLAGS

#Build deps
RUN apk --no-cache add build-base git nodejs npm curl

#Setup repo
COPY . ${GOPATH}/src/code.gitea.io/gitea
WORKDIR ${GOPATH}/src/code.gitea.io/gitea


FROM build-env AS builder

RUN ${GOPATH}/src/code.gitea.io/gitea/build_gitea.sh

# Begin env-to-ini build
RUN go build contrib/environment-to-ini/environment-to-ini.go


FROM ${BASE_IMAGE}
LABEL maintainer="maintainers@gitea.io"

EXPOSE 22 3000

RUN apk --no-cache add \
    bash \
    ca-certificates \
    curl \
    gettext \
    git \
    linux-pam \
    openssh \
    s6 \
    sqlite \
    su-exec \
    gnupg

RUN addgroup \
    -S -g 1000 \
    git && \
  adduser \
    -S -H -D \
    -h /data/git \
    -s /bin/bash \
    -u 1000 \
    -G git \
    git && \
  echo "git:*" | chpasswd -e

ENV USER git
ENV GITEA_CUSTOM /data/gitea

VOLUME ["/data"]

ENTRYPOINT ["/usr/bin/entrypoint"]
CMD ["/bin/s6-svscan", "/etc/s6"]

COPY docker/root /
COPY --from=builder /go/src/code.gitea.io/gitea/gitea /app/gitea/gitea
COPY --from=builder /go/src/code.gitea.io/gitea/environment-to-ini /usr/local/bin/environment-to-ini
RUN chmod 755 /usr/bin/entrypoint /app/gitea/gitea /usr/local/bin/gitea /usr/local/bin/environment-to-ini
RUN chmod 755 /etc/s6/gitea/* /etc/s6/openssh/* /etc/s6/.s6-svscan/*

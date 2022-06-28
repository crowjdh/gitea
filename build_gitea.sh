#!/bin/sh

GITEA_VERSION=$(curl -H "Accept: application/vnd.github.v3+json" https://api.github.com/repos/go-gitea/gitea/releases/latest | grep '^  "name' | grep -Eio 'v[0-9\.]+')

if [ $(uname -m) = "armv7l" ]; then
  GITEA_VERSION=$(echo $GITEA_VERSION | cut -c 2-)
  wget -O gitea https://dl.gitea.io/gitea/$GITEA_VERSION/gitea-$GITEA_VERSION-linux-arm-6 \
    && chmod 0744 gitea \
    && mv gitea /go/src/code.gitea.io/gitea/gitea
else
  if [ -n "${GITEA_VERSION}" ]; then
    git checkout "${GITEA_VERSION}" \
      && make clean-all build
  fi
fi

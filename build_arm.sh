#!/bin/bash

DOCKER_BUILDKIT=1 docker build -t crowjdh/gitea:armv6-1.16.8 --build-arg BASE_IMAGE="arm32v7/alpine:3.12.0" .

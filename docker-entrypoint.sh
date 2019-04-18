#!/bin/bash

set -e

export GO111MODULE=on
export DATASTORE_EMULATOR_HOST=localhost:8081

gcloud config set project foxsec-pipeline-contrib
nohup gcloud beta emulators datastore start >/dev/null 2>&1 &
while ! nc -z localhost 8081; do sleep 0.1; done

cd /go/src/github.com/mozilla-services/foxsec-pipeline-contrib
exec "$@"

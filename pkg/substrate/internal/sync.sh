#!/usr/bin/env bash
# Copies agent-substrate's generated gRPC API packages next to this script, so pkg/substrate
# talks to Substrate without depending on its Go modules or their Kubernetes version.
set -o errexit -o nounset -o pipefail

# The Substrate control plane API, and the env service API built against it.
SUBSTRATE=go.ate.dev/substrate@v1.0.0-rc.2
ENV=github.com/agent-substrate/env@v0.0.11-0.20261101192238-56424de763f9

dest="$(cd "$(dirname "$0")" && pwd)"
moddir() { GOWORK=off GOFLAGS=-mod=mod go mod download -json "$1" | sed -n 's/^\t"Dir": "\(.*\)",$/\1/p'; }

substrate="$(moddir "${SUBSTRATE}")"
env="$(moddir "${ENV}")"
mkdir -p "${dest}/ateapi" "${dest}/ateenvv1alpha"
for file in ateapi.proto ateapi.pb.go ateapi_grpc.pb.go; do
  install -m 0644 "${substrate}/api/ateapi/v1/${file}" "${dest}/ateapi/${file}"
done
for file in env.proto env.pb.go env_grpc.pb.go guest.proto guest.pb.go guest_grpc.pb.go; do
  install -m 0644 "${env}/proto/ateenv/v1alpha/${file}" "${dest}/ateenvv1alpha/${file}"
done
printf 'Copied by sync.sh from %s (ateapi) and %s (ateenvv1alpha).\n' "${SUBSTRATE}" "${ENV}" > "${dest}/SOURCE"

# Makefile stub - forwards to Taskfile
# Install task: https://taskfile.dev/installation/

.PHONY: build lint test install fmt tidy clean all generate-llm-model-registry

build:
	@task build

lint:
	@task lint

test:
	@task test

install:
	@task install

fmt:
	@task fmt

tidy:
	@task tidy

clean:
	@task clean

all:
	@task all

generate-llm-model-registry:
	go run ./pkg/ai/internal/gen-model-registry --output pkg/ai/model_registry_static.go

# Makefile stub - forwards to Taskfile
# Install task: https://taskfile.dev/installation/

.PHONY: build lint test install fmt tidy clean all

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

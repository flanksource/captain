#!/bin/sh
set -e
EXEC_UID="${USER_UID:-501}"
EXEC_GID="${USER_GID:-20}"
exec gosu "${EXEC_UID}:${EXEC_GID}" "$@"

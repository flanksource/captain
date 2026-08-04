#!/usr/bin/env bash
# gitagent_empirical.sh — verifies the git behaviours the git-agent protocol
# (SPEC-git-agent-protocol §1) relies on, against the git on PATH.
#
# The four properties:
#   1.1 receive-pack quarantine env leaks into hook descendants, and
#       `rev-parse --local-env-vars` does NOT list GIT_QUARANTINE_PATH,
#       so the githooks(5) scrub idiom leaves it set (R1.1).
#   1.2 push options survive byte-identical under receive.advertisePushOptions,
#       and a push with options fails against a receiver that has not
#       advertised them (R1.2).
#   1.3 read-tree + checkout-index materializes a quarantined tree when the
#       work tree is absolute; the relative form is a trap (R1.3, H18).
#   1.4 a relay push from inside pre-receive succeeds once GIT_QUARANTINE_PATH
#       alone is unset, with the inherited object directories retained (R1.4).
#
# Output is TAP-like; exit status is non-zero if any check fails.
# Rerun this before trusting the protocol on a new git version.

set -u

ROOT="$(mktemp -d "${TMPDIR:-/tmp}/gitagent-empirical.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT

N=0
FAIL=0

ok() {
	N=$((N + 1))
	echo "ok $N - $1"
}

not_ok() {
	N=$((N + 1))
	FAIL=$((FAIL + 1))
	echo "not ok $N - $1"
}

# assert <ok-if-zero> <description>
assert() {
	if [ "$1" -eq 0 ]; then ok "$2"; else not_ok "$2"; fi
}

g() {
	git -c user.name=captain -c user.email=captain@localhost \
		-c init.defaultBranch=main -c protocol.file.allow=always "$@"
}

# mkclient <dir> — working repo with a three-file commit (one nested path).
mkclient() {
	g init -q "$1"
	mkdir -p "$1/pkg/deep"
	echo alpha >"$1/alpha.txt"
	echo beta >"$1/pkg/beta.txt"
	echo gamma >"$1/pkg/deep/gamma.txt"
	g -C "$1" add -A
	g -C "$1" commit -q -m seed
}

echo "# git version: $(git version)"

# --- 1.1 quarantine leak + --local-env-vars omission --------------------------

r11="$ROOT/r11"
client11="$ROOT/client11"
unrelated="$ROOT/unrelated"
out11="$ROOT/out11"
mkdir -p "$out11"
g init -q --bare "$r11"
mkclient "$client11"
mkclient "$unrelated"
unrelated_head="$(g -C "$unrelated" rev-parse HEAD)"

cat >"$r11/hooks/pre-receive" <<EOF
#!/bin/sh
cat >/dev/null
printf '%s' "\${GIT_QUARANTINE_PATH:-}" >"$out11/quarantine_path"
sh -c "cd '$unrelated' && git update-ref refs/heads/probe-leak $unrelated_head" \
	2>"$out11/leak_err"
echo \$? >"$out11/leak_rc"
sh -c "unset GIT_QUARANTINE_PATH GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE \
	GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES; \
	cd '$unrelated' && git update-ref refs/heads/probe-scrubbed $unrelated_head" \
	2>"$out11/scrub_err"
echo \$? >"$out11/scrub_rc"
git rev-parse --local-env-vars >"$out11/local_env_vars"
exit 0
EOF
chmod +x "$r11/hooks/pre-receive"

g -C "$client11" push -q "$r11" HEAD:refs/heads/main 2>"$out11/push_err"
assert $? "1.1 push driving the quarantine probe succeeds"

test -s "$out11/quarantine_path"
assert $? "1.1 GIT_QUARANTINE_PATH is set in the pre-receive environment"

test "$(cat "$out11/leak_rc" 2>/dev/null)" = "128"
assert $? "1.1 inherited env breaks a descendant git in an unrelated repo (rc=128)"

test "$(cat "$out11/scrub_rc" 2>/dev/null)" = "0" &&
	g -C "$unrelated" rev-parse -q --verify refs/heads/probe-scrubbed >/dev/null
assert $? "1.1 scrubbing the R1.1 variable list makes the same command succeed"

! grep -q GIT_QUARANTINE_PATH "$out11/local_env_vars"
assert $? "1.1 rev-parse --local-env-vars omits GIT_QUARANTINE_PATH"

# --- 1.2 push options ---------------------------------------------------------

r12="$ROOT/r12"
client12="$ROOT/client12"
out12="$ROOT/out12"
mkdir -p "$out12"
g init -q --bare "$r12"
g -C "$r12" config receive.advertisePushOptions true
mkclient "$client12"

cat >"$r12/hooks/pre-receive" <<EOF
#!/bin/sh
cat >/dev/null
printf '%s' "\${GIT_PUSH_OPTION_COUNT:-}" >"$out12/count"
printf '%s' "\${GIT_PUSH_OPTION_0:-}" >"$out12/opt0"
printf '%s' "\${GIT_PUSH_OPTION_1:-}" >"$out12/opt1"
exit 0
EOF
chmod +x "$r12/hooks/pre-receive"

g -C "$client12" push -q \
	--push-option=captain-envelope-v1 --push-option=attempt=2 \
	"$r12" HEAD:refs/heads/main 2>"$out12/push_err"
assert $? "1.2 push with options succeeds when advertised"

test "$(cat "$out12/count")" = "2" &&
	test "$(cat "$out12/opt0")" = "captain-envelope-v1" &&
	test "$(cat "$out12/opt1")" = "attempt=2"
assert $? "1.2 both options arrive byte-identical in pre-receive"

r12b="$ROOT/r12b"
g init -q --bare "$r12b" # receive.advertisePushOptions left at the false default
if g -C "$client12" push -q --push-option=x "$r12b" HEAD:refs/heads/main \
	2>"$out12/noadv_err"; then
	not_ok "1.2 push with options fails against a non-advertising receiver"
else
	if g -C "$r12b" rev-parse -q --verify refs/heads/main >/dev/null; then
		not_ok "1.2 rejected options push leaves no ref behind"
	else
		ok "1.2 push with options fails outright when not advertised"
	fi
fi

# --- 1.3 materialization ------------------------------------------------------

r13="$ROOT/r13"
client13="$ROOT/client13"
out13="$ROOT/out13"
abswt="$ROOT/abswt"
mkdir -p "$out13" "$abswt"
g init -q --bare "$r13"
mkclient "$client13"

cat >"$r13/hooks/pre-receive" <<EOF
#!/bin/sh
read old new ref
expected=\$(git ls-tree -r "\$new" | wc -l)
printf '%s' "\$expected" >"$out13/expected"

idx="$out13/idx-abs"
GIT_INDEX_FILE="\$idx" git read-tree "\$new" &&
	GIT_INDEX_FILE="\$idx" GIT_WORK_TREE="$abswt" git checkout-index -a -f
echo \$? >"$out13/abs_rc"
find "$abswt" -type f | wc -l | tr -d ' ' >"$out13/abs_count"

mkdir -p relwt
idxrel="$out13/idx-rel"
GIT_INDEX_FILE="\$idxrel" git read-tree "\$new" &&
	GIT_INDEX_FILE="\$idxrel" GIT_WORK_TREE=relwt git checkout-index -a -f
echo \$? >"$out13/rel_rc"
find relwt -type f | wc -l | tr -d ' ' >"$out13/rel_count"
exit 0
EOF
chmod +x "$r13/hooks/pre-receive"

g -C "$client13" push -q "$r13" HEAD:refs/heads/main 2>"$out13/push_err"
assert $? "1.3 push driving the materialization probe succeeds"

expected="$(tr -d ' ' <"$out13/expected")"
test "$(cat "$out13/abs_rc")" = "0" &&
	test -n "$expected" && test "$expected" -gt 0 &&
	test "$(cat "$out13/abs_count")" = "$expected"
assert $? "1.3 absolute work-tree materializes the full quarantined tree ($expected files)"

rel_rc="$(cat "$out13/rel_rc" 2>/dev/null)"
rel_count="$(cat "$out13/rel_count" 2>/dev/null)"
if [ "$rel_rc" = "0" ] && [ "$rel_count" = "0" ]; then
	ok "1.3 relative work-tree writes nothing while exiting 0 (H18 confirmed)"
elif [ "$rel_rc" != "0" ]; then
	ok "1.3 relative work-tree fails visibly on this git (rc=$rel_rc; safe, H18 moot)"
else
	# It materialized where the naive reading expects. Still not a failure for
	# the protocol (which always absolutizes) but flag the substrate change.
	ok "1.3 relative work-tree materialized $rel_count files on this git # H18 behaviour differs; absolutizing remains correct"
fi

# --- 1.4 relay from inside pre-receive ---------------------------------------

sidecar="$ROOT/sidecar"
upstream="$ROOT/upstream"
client14="$ROOT/client14"
out14="$ROOT/out14"
mkdir -p "$out14"
g init -q --bare "$sidecar"
g init -q --bare "$upstream"
mkclient "$client14"

cat >"$sidecar/hooks/pre-receive" <<EOF
#!/bin/sh
read old new ref
git push "$upstream" "\$new:refs/heads/relay-quarantined" \
	2>"$out14/naive_err"
echo \$? >"$out14/naive_rc"
env -u GIT_QUARANTINE_PATH \
	git push "$upstream" "\$new:refs/heads/relay-scrubbed" \
	2>"$out14/scrubbed_err"
echo \$? >"$out14/scrubbed_rc"
exit 0
EOF
chmod +x "$sidecar/hooks/pre-receive"

g -C "$client14" push -q "$sidecar" HEAD:refs/heads/main 2>"$out14/push_err"
assert $? "1.4 push driving the relay probe succeeds"

test "$(cat "$out14/naive_rc")" != "0" &&
	grep -qi quarantine "$out14/naive_err"
assert $? "1.4 naive relay is refused by the upstream quarantine guard"

! g -C "$upstream" rev-parse -q --verify refs/heads/relay-quarantined >/dev/null
assert $? "1.4 refused relay left no ref upstream"

pushed="$(g -C "$client14" rev-parse HEAD)"
test "$(cat "$out14/scrubbed_rc")" = "0" &&
	test "$(g -C "$upstream" rev-parse refs/heads/relay-scrubbed 2>/dev/null)" = "$pushed"
assert $? "1.4 unsetting GIT_QUARANTINE_PATH alone lets the relay through"

# ------------------------------------------------------------------------------

echo "1..$N"
if [ "$FAIL" -ne 0 ]; then
	echo "# $FAIL of $N checks failed"
	exit 1
fi
echo "# all $N checks passed"

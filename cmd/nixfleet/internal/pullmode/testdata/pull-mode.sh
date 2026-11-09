#!/usr/bin/env bash
# Run either generated pull script with fake commands and only temporary paths.
set -euo pipefail
TASK_TMP=$(mktemp -d)
trap 'rm -rf "$TASK_TMP"' EXIT
export TASK_TMP
mkdir -p "$TASK_TMP/bin" "$TASK_TMP/repo/.git" "$TASK_TMP/system"
sed -e '/export PATH=/d' \
        -e "s|^ *REPO_PATH=.*|REPO_PATH=\"$TASK_TMP/repo\"|" \
        -e "s|/var/log/nixfleet/pull.log|$TASK_TMP/pull.log|g" \
        -e "s|/var/run/nixfleet-pull.lock|$TASK_TMP/pull.lock|g" \
        -e "s|/var/lib/nixfleet|$TASK_TMP/state|g" "$1" >"$TASK_TMP/pull"
cat >"$TASK_TMP/bin/git" <<'SH'
#!/bin/sh
case "$1" in rev-parse) cat "$TASK_TMP/commit";; fetch|reset) :;; *) exit 99;; esac
SH
cat >"$TASK_TMP/bin/nix" <<'SH'
#!/bin/sh
case "$1" in build) test ! -f "$TASK_TMP/fail-build";; path-info) echo "$TASK_TMP/system";; *) exit 99;; esac
SH
cat >"$TASK_TMP/bin/nix-env" <<'SH'
#!/bin/sh
case "$*" in *--set*) test ! -f "$TASK_TMP/fail-profile";; esac
SH
cat >"$TASK_TMP/bin/flock" <<'SH'
#!/bin/sh
test ! -f "$TASK_TMP/fail-lock"
SH
cat >"$TASK_TMP/bin/date" <<'SH'
#!/bin/sh
echo fixture-time
SH
cat >"$TASK_TMP/system/activate" <<'SH'
#!/bin/sh
echo apply >> "$TASK_TMP/attempts"
test ! -f "$TASK_TMP/fail-activate"
SH
chmod +x "$TASK_TMP/bin/"* "$TASK_TMP/system/activate"
export PATH="$TASK_TMP/bin:$PATH"
echo first >"$TASK_TMP/commit"
run_pull() { bash "$TASK_TMP/pull" >"$TASK_TMP/output" 2>&1; }
fail() {
        cat "$TASK_TMP/output" >&2
        echo "$*" >&2
        exit 1
}
# A losing contender must not unlink the inode held by another process.
touch "$TASK_TMP/pull.lock" "$TASK_TMP/fail-lock"
if run_pull; then fail 'losing contender succeeded'; fi
[ -f "$TASK_TMP/pull.lock" ] || fail 'losing contender deleted the lock'
rm "$TASK_TMP/fail-lock"
# Initial checkout equals origin, but has never activated.
run_pull || fail 'first activation failed'
if [ "${2:-retry}" = legacy ]; then
        [ ! -f "$TASK_TMP/attempts" ] || fail 'legacy mode no longer skips unchanged checkout'
        exit 0
fi
[ -f "$TASK_TMP/attempts" ] || fail 'initial activation skipped'
[ "$(wc -l <"$TASK_TMP/attempts" | tr -d ' ')" = 1 ] || fail 'initial activation count'
run_pull || fail 'unchanged pull failed'
[ "$(wc -l <"$TASK_TMP/attempts" | tr -d ' ')" = 1 ] || fail 'successful commit reapplied'
for phase in build activate profile; do
        echo "$phase" >"$TASK_TMP/commit"
        touch "$TASK_TMP/fail-$phase"
        if run_pull; then fail "$phase failure reported success"; fi
        rm "$TASK_TMP/fail-$phase"
        before=$(wc -l <"$TASK_TMP/attempts")
        run_pull || fail "$phase retry failed"
        after=$(wc -l <"$TASK_TMP/attempts")
        [ "$after" -gt "$before" ] || fail "$phase failure was not retried"
done

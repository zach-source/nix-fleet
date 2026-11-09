# A stale lock is left intact after SIGKILL; an operator must verify its owner
# has stopped before removing it. Never steal a potentially live lock.
nixfleet_lock_enter() {
	nixfleet_lock_path=$1
	nixfleet_lock_timeout=$2
	if [ "${NIXFLEET_LOCK_PATH:-}" = "$nixfleet_lock_path" ]; then
		return 0
	fi
	mkdir -p "$(dirname "$nixfleet_lock_path")"
	nixfleet_lock_waited=0
	until mkdir "$nixfleet_lock_path" 2>/dev/null; do
		if [ "$nixfleet_lock_waited" -ge "$nixfleet_lock_timeout" ]; then
			echo "Timed out waiting for NixFleet mutation lock" >&2
			return 75
		fi
		sleep 1
		nixfleet_lock_waited=$((nixfleet_lock_waited + 1))
	done
	trap 'rmdir "$nixfleet_lock_path"' EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	export NIXFLEET_LOCK_PATH="$nixfleet_lock_path"
}

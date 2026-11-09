# Pull runs locally as root; SSH connectivity and sudo checks are unnecessary.
test "$(id -u)" -eq 0
for check in /nix:5242880 /var:1048576; do
	path=${check%:*}
	minimum=${check#*:}
	available=$(df -Pk "$path" | awk 'END {print $4}')
	case "$available" in '' | *[!0-9]*)
		echo 'Cannot determine free space' >&2
		exit 1
		;;
	esac
	if [ "$available" -lt "$minimum" ]; then
		echo "Insufficient free space on $path" >&2
		exit 1
	fi
done
systemctl is-active --quiet nix-daemon
nix --version >/dev/null

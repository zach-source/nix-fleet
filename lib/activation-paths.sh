# shellcheck shell=bash
# Root-owned, non-writable ancestors cannot be swapped by an unprivileged user
# between validation and mkdir/chmod/chown. Symlinks are deliberately refused.
nixfleet_check_path() {
  local path="$1" rest part current="" metadata uid mode permissions
  case "$path" in
  /*) ;;
  *)
    echo "Managed path must be absolute: $path" >&2
    return 1
    ;;
  esac
  case "$path" in
  / | */ | *//* | */./* | */../* | */. | */..)
    echo "Managed path must be normalized: $path" >&2
    return 1
    ;;
  esac
  rest="${path#/}"
  while [[ "$rest" == */* ]]; do
    part="${rest%%/*}"
    rest="${rest#*/}"
    current="$current/$part"
    if [[ -L "$current" ]]; then
      echo "Managed path has a symlink ancestor: $path" >&2
      return 1
    fi
    if [[ -e "$current" ]]; then
      [[ -d "$current" ]] || return 1
      # Darwin ACL grants are not represented by the POSIX mode bits. Refuse
      # extended ACL ancestors conservatively rather than guessing their effect.
      if [[ "$(uname -s)" == Darwin ]]; then
        permissions=$(/bin/ls -lde "$current") || return 1
        permissions="${permissions%% *}"
        if [[ "$permissions" == *+ ]]; then
          echo "Managed path has an ACL ancestor: $path" >&2
          return 1
        fi
      fi
      metadata=$(@stat@ -c '%u %a' -- "$current") || return 1
      read -r uid mode <<<"$metadata"
      if [[ "$uid" != 0 ]] || (((8#$mode & 0022) != 0)); then
        echo "Managed path has an untrusted ancestor: $path" >&2
        return 1
      fi
    fi
  done
  if [[ -L "$path" ]]; then
    echo "Managed path is a symlink: $path" >&2
    return 1
  fi
}

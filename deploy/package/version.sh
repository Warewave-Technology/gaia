#!/bin/sh
# A package version from a git describe: v1.2.3-4-gabc gives 1.2.3~4~gabc,
# no tag gives 0.0.0~<commit>.
v=${1%-dirty}
case "$v" in
  v[0-9]*) echo "${v#v}" | sed 's/-/~/g' ;;
  *) echo "0.0.0~$v" ;;
esac

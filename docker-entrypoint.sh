#!/bin/sh
set -e
# Named volumes are root-owned; SQLite CANTOPEN (14) is often reported as "out of memory".
mkdir -p /data /data/imgcache
if [ "$(id -u)" = "0" ]; then
	chown hap:hap /data /data/imgcache
	exec setpriv --reuid=hap --regid=hap --init-groups hapudding "$@"
fi
exec hapudding "$@"

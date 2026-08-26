#!/bin/sh
set -e
# Named volumes are root-owned; SQLite CANTOPEN (14) is often reported as "out of memory".
mkdir -p /data
if [ "$(id -u)" = "0" ]; then
	chown hap:hap /data
	exec setpriv --reuid=hap --regid=hap --init-groups hapudding "$@"
fi
exec hapudding "$@"

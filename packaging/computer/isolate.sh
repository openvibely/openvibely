#!/bin/bash
# Runs as trusted root inside one task's mount, PID, and network namespaces.
set -euo pipefail
umask 077
name=$OPENVIBELY_DESKTOP_ID
[[ "$name" =~ ^task-[a-z0-9-]+$ ]] || exit 1
base="/home/ubuntu/.desktops/$name"
runtime="/run/ov-desktops/$name"
desktop_uid=$OPENVIBELY_DESKTOP_UID
shell_gid=$((desktop_uid + 1))
# Parent directories are not writable by the task account. Never follow
# profile/home symlinks while assigning ownership.
mkdir -p "$base"
[[ ! -L "$base" ]] || exit 1
chown root:root "$base"
chmod 0700 "$base"
for dir in home browser config cache; do
 [[ ! -L "$base/$dir" ]] || exit 1
 mkdir -p "$base/$dir"
done
# IDs are deterministic across restarts; collisions fail closed in the router.
# Private passwd/group files avoid concurrent useradd mutations across tasks.
cp /etc/passwd "$runtime/passwd"
cp /etc/group "$runtime/group"
printf 'ov-desktop:x:%s:%s::%s:/bin/bash\n' "$desktop_uid" "$desktop_uid" "$base/home" >> "$runtime/passwd"
printf 'ov-desktop:x:%s:\nov-shell:x:%s:\n' "$desktop_uid" "$shell_gid" >> "$runtime/group"
chmod 0644 "$runtime/passwd" "$runtime/group"
mount --bind "$runtime/passwd" /etc/passwd
mount --bind "$runtime/group" /etc/group
# Migrate files retained from the former separate shell user. Do not follow
# symlinks or cross filesystems while changing ownership.
find "$base" -xdev -uid "$shell_gid" -exec chown -h "$desktop_uid:$desktop_uid" {} +
chown -h "$desktop_uid:$desktop_uid" "$base/home"
chmod 0770 "$base/home"
for dir in browser config cache; do
 chown -h "$desktop_uid:$desktop_uid" "$base/$dir"
 chmod 0700 "$base/$dir"
done
chmod 0711 "$base"
mount -t tmpfs -o mode=1777,nosuid,nodev tmpfs /tmp
mount -t tmpfs -o mode=1777,nosuid,nodev tmpfs /dev/shm
mount -o remount,hidepid=2 /proc
mkdir /tmp/agent
chown "$desktop_uid:$shell_gid" /tmp/agent
chmod 0700 /tmp/agent
# Only root's trusted bridge can open the control socket. The entire directory
# is inaccessible to both desktop applications and model shell jobs.
chmod 0700 "$runtime"
ip link set lo up
# Browser and shell share one user and may use this task's local services.
# No task may talk to peers through the parent namespace or host gateway.
ip route >/dev/null
: > "$runtime/namespace-ready"
for i in {1..400}; do [[ -f "$runtime/network-ready" ]] && break; sleep .05; done
[[ -f "$runtime/network-ready" ]] || exit 1
# slirp's DNS resolver; do not use Docker's parent-namespace 127.0.0.11.
printf 'nameserver 10.0.2.3\n' > "$runtime/resolv.conf"
chmod 0644 "$runtime/resolv.conf"
mount --bind "$runtime/resolv.conf" /etc/resolv.conf
exec /usr/local/bin/start-computer

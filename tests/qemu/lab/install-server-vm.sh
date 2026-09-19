#!/bin/sh
# install-server-vm.sh -- runs INSIDE the Alpine live ISO of the pp-server VM.
# It installs Alpine 3.23 to /dev/vda and makes the VM reachable with SSH.
set -eux

ip link set eth0 up
udhcpc -i eth0 -n -q
ip -4 addr show eth0

cat >/etc/apk/repositories <<REPO
https://dl-cdn.alpinelinux.org/alpine/v3.23/main
https://dl-cdn.alpinelinux.org/alpine/v3.23/community
REPO
apk update

echo pp-server >/etc/hostname
hostname pp-server
cat >/etc/network/interfaces <<NET
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet dhcp
	hostname pp-server
NET
setup-timezone -z UTC || true

# The unattended install. ERASE_DISKS removes the one confirmation question.
ERASE_DISKS=/dev/vda setup-disk -m sys /dev/vda

# Find the root of the new system and finish the work there.
mkdir -p /m
target=""
for p in /dev/vda1 /dev/vda2 /dev/vda3 /dev/vda4; do
	[ -b "$p" ] || continue
	if mount "$p" /m 2>/dev/null; then
		if [ -f /m/etc/alpine-release ] && [ -d /m/etc/runlevels ]; then
			target="$p"
			break
		fi
		umount /m
	fi
done
[ -n "$target" ] || { echo NO-TARGET-ROOT; exit 1; }
echo "target root is $target"

apk --root /m add openssh openssh-server-common-openrc chrony chrony-openrc
for s in sshd chronyd; do
	ln -sfn "/etc/init.d/$s" "/m/etc/runlevels/default/$s"
done

# Root login with a password, because this is a lab machine on a trusted LAN.
mkdir -p /m/etc/ssh/sshd_config.d
printf 'PermitRootLogin yes\nPasswordAuthentication yes\n' \
	>/m/etc/ssh/sshd_config.d/10-lab.conf
if ! grep -q '^Include /etc/ssh/sshd_config.d' /m/etc/ssh/sshd_config; then
	printf '\nPermitRootLogin yes\nPasswordAuthentication yes\n' \
		>>/m/etc/ssh/sshd_config
fi

mkdir -p /m/root/.ssh
cp /mnt/work/authorized_keys /m/root/.ssh/authorized_keys
chmod 700 /m/root/.ssh
chmod 600 /m/root/.ssh/authorized_keys

mount --bind /dev /m/dev
chroot /m /bin/sh -c "echo 'root:portapixel' | chpasswd"
umount /m/dev

echo pp-server >/m/etc/hostname
cp /etc/network/interfaces /m/etc/network/interfaces
sync
umount /m
echo INSTALL-SERVER-VM-OK

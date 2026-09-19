# The two-machine lab

This directory holds the scripts of a permanent test lab. The lab has two QEMU
virtual machines on the LAN of the owner:

| VM | What it is | Disk | RAM | CPU |
|---|---|---|---|---|
| `pp-server` | Alpine 3.23 with the fleet server `portapixel-server` | 8 GB | 1 GB | 1 |
| `pp-client` | A signage device from the real PortaPixel image | 16 GB | 2 GB | 2 |

Each VM has a tap device in the bridge `br0`. The LAN router gives each VM its
own address with DHCP. So a person tests the two machines from a desktop, in the
same way as two real machines.

The scripts run on an Alpine host that has KVM, a tap device and QEMU. The host
of this lab is an LXC container on Proxmox, which has no loop devices. So the
image comes from `tests/qemu/builder-vm.sh`, which builds it in a short-lived VM.

## The network

`br0` holds `eth0` and the address of the host. Every VM gets a tap device in the
same bridge, with a fixed MAC, so each DHCP lease stays the same:

| Device | MAC |
|---|---|
| `pp-server` | `52:54:00:50:50:01` |
| `pp-client` | `52:54:00:50:50:02` |

`netup.sh` makes the bridge. A change of the network over SSH can cut the
connection that makes it, so the script has a dead-man timer: it applies the
change, waits 90 s, and puts the old state back unless the file `net-ok` exists.
You make that file over a NEW SSH connection, which proves that the access
survived.

```sh
ssh root@HOST 'nohup setsid /root/ppwork/lab/netup.sh >/dev/null 2>&1 &'
ssh root@HOST touch /root/ppwork/lab/net-ok      # a new connection
```

Record the state before the change: `ip -d addr`, `ip route`,
`/etc/network/interfaces` and `/etc/resolv.conf`.

For a permanent bridge, write it in `/etc/network/interfaces`:

```
auto eth0
iface eth0 inet manual

auto br0
iface br0 inet static
	address 10.0.0.146/24
	gateway 10.0.0.1
	bridge-ports eth0
	hwaddress bc:24:11:1d:6c:8b
```

`bridge-ports` needs the package `bridge`. On Proxmox, the host writes the
`/etc/network/interfaces` of a container at each start of that container. The
file `/etc/network/.pve-ignore.interfaces` stops that. Make it, or the bridge
goes away at the next start.

## The files

| File | What it does |
|---|---|
| `lab.env` | The settings and the helper functions. Every script reads it. |
| `netup.sh` | Makes the bridge, with a dead-man timer. |
| `install-server.sh` | Makes the disk of pp-server and installs Alpine on it. |
| `mkserver.exp` | Drives that install over the serial console. |
| `install-server-vm.sh` | Runs in the installer VM. It calls `setup-disk`. |
| `make-client-disk.sh` | Makes the 16 GB disk of pp-client from a `.img.gz`. |
| `start-server.sh` | Starts pp-server. |
| `start-client.sh` | Starts pp-client, with VNC on the loopback. |
| `stop.sh` | Stops one VM or both. It stops no other QEMU process. |
| `status.sh` | Shows the VMs, their addresses and their answers. |
| `shot.sh` | Writes a screenshot of the pp-client display to a PNG file. |
| `pp-lab.start` | The OpenRC `local.d` script. It starts the lab after a boot. |

## How to build the lab

```sh
# 1. The bridge (see above), then the two disks.
sh install-server.sh                      # needs seed/authorized_keys
sh make-client-disk.sh /path/to/portapixel-VERSION-x86_64.img.gz

# 2. Start the two machines.
sh start-server.sh
sh start-client.sh
sh status.sh
```

Install the fleet server on pp-server with the steps of `deploy/README.md`,
path 2. The first start prints the admin password one time. Read it with
`grep -A4 "first run" /var/log/portapixel-server.log`.

## How to see the screen of pp-client

QEMU takes a password for VNC, but that password is weak. So the VNC server
listens on the loopback of the host only. Make a tunnel, then point a VNC client
at `127.0.0.1:5901`:

```sh
ssh -L 5901:127.0.0.1:5901 root@HOST
```

A screenshot needs no tunnel:

```sh
sh shot.sh /tmp/screen.png
```

## After a restart of the host

Copy `pp-lab.start` to `/etc/local.d/pp-lab.start`, make it executable and run
`rc-update add local default`. The script makes the bridge when it is missing
and starts the two VMs.

## Limits

- The PortaPixel image has no `acpid`, so the guest does not obey the ACPI power
  button. `stop.sh` asks first and then stops QEMU after 30 s. To stop pp-client
  gracefully, run `poweroff` in the guest first.
- `status.sh` finds an address with a ping sweep of the LAN and the neighbour
  table. A VM that answers no ping keeps the address `unknown`.

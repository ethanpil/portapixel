# x86_64 boot files

`cmdline.base` is the only source of the kernel command line for the x86_64
image (plan section 6). `build-image.sh` reads it and puts it into
`grub.cfg.in` and `syslinux.cfg.in`. Never write a command line anywhere else.

What each part does:

- `root=LABEL=PPROOT` finds the root by label, never by UUID. A label survives
  a clone of the image and is the same on both architectures (D53).
- `rootwait` waits for a slow USB stick or SD card to appear.
- `modules=` lists the bus drivers the initramfs must load before it looks for
  the label: SATA and PATA (`ahci`, `ata_piix`), SCSI and USB disks (`sd-mod`,
  `usb-storage`), NVMe, SD cards and eMMC (`mmc_block`, `sdhci*`), and the three
  QEMU virtio buses. The QEMU smoke test attaches the disk as virtio, so
  `virtio_blk` must be here or the test fails for the wrong reason
  (mountnas lesson).
- `console=tty1 console=ttyS0,115200`: kernel messages go to the screen and to
  the serial port. The serial port is last, so `/dev/console` is the serial
  port and the getty in `/etc/inittab` lands there. There is no getty on tty1:
  the display belongs to the cage session (plan section 5).

The files `grub.cfg.in` (UEFI) and `syslinux.cfg.in` (BIOS) hold `@CMDLINE@`,
which `build-image.sh` replaces. Both put the CPU microcode first in the initrd
chain: the kernel reads microcode before it unpacks the real initramfs.

## Graphics drivers are not in the initramfs

The `modules=` list above holds storage buses only. The display drivers,
`virtio_gpu` included, are not there and do not need to be: `udev` and
`hwdrivers` load them from the real root by PCI id, a second after the root
mounts. cage then finds `/dev/dri/card0`.

The QEMU smoke test therefore needs a real DRM device on the virtual machine
(`-device virtio-vga`), not a module in the initramfs.


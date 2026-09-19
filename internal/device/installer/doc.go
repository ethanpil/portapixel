// Package installer copies the running system onto an internal disk (D54).
//
// Why this package exists: a person tries PortaPixel from a USB stick and then
// wants it to stay. The stick already holds a complete system, so the shortest
// path is a copy: the same three partitions with the same labels and the same
// partition types, and the media partition grown to the whole disk.
//
// NOT VERIFIED ON HARDWARE. The release checklist has the check: install onto a
// SATA disk on an x86 machine and onto an eMMC module on a Raspberry Pi, power
// off, remove the stick, and boot from the disk. Only that run proves the boot
// loader steps. The unit tests here use ordinary files in place of block devices
// and fake programs in place of sgdisk, so they prove the copy and the refusals,
// and they prove nothing about a real disk.
//
// The steps, and why each one is there:
//
//  1. Refuse. The boot device, a disk that is too small, a read-only disk and a
//     confirmation string that is not the device path are all refused before
//     anything is written. There is no undo.
//  2. Write a new GPT on the target with sgdisk. p1 and p2 take the sizes of the
//     source, p3 takes the rest of the disk. The labels (PPBOOT, PPROOT, PPMEDIA)
//     and the type GUIDs are the ones that the image uses, because the boot chain
//     and every fstab line find the partitions by label.
//  3. Copy p1 and p2 as blocks. They hold a FAT32 boot partition and an ext4 root,
//     and a block copy of the same size needs no knowledge of either.
//  4. Make a new exFAT PPMEDIA at the full size of the target, then walk the
//     media files across. exFAT has no grow tool, so the partition is made at its
//     final size and the content is copied into it (the same reason as D36).
//  5. On x86 write the first 440 bytes of the source disk onto the target. That is
//     the GPT-aware MBR boot code of syslinux, which lives outside every
//     partition. The legacy_boot attribute of p1 goes on in step 2. A Raspberry Pi
//     needs neither: its firmware reads the FAT partition.
//
// The two disks then carry the same three labels. That is why the last instruction
// is to power off and remove the stick: with one of them gone, the label points at
// one disk and the boot is unambiguous.
//
// Every program call goes through Runner, and every path root is a field, so the
// tests need no root rights, no Linux and no disk.
package installer

//go:build linux

package power

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The DRM ioctl calls of the kernel (include/uapi/drm/drm.h and drm_mode.h). The
// package does not use cgo, so it does the calls itself. Four calls read the
// connectors and one call sets a property.
//
// The numbers use the common layout of _IOWR: two direction bits, 14 size bits,
// the type 'd' and the call number. amd64, arm64, arm and riscv64 all use it.

const (
	drmIoctlType = 'd'

	drmCmdSetMaster          = 0x1e
	drmCmdModeGetResources   = 0xa0
	drmCmdModeGetConnector   = 0xa7
	drmCmdModeGetProperty    = 0xaa
	drmCmdModeObjSetProperty = 0xba

	// DRM_MODE_OBJECT_CONNECTOR and DRM_MODE_CONNECTED.
	drmObjectConnector = 0xc0c0c0c0
	drmConnected       = 1

	drmPropNameLen = 32
)

// ioctlIO and ioctlIOWR build the number of a call with no data and of a call that
// reads and writes a struct of the given size.
func ioctlIO(nr uintptr) uintptr { return drmIoctlType<<8 | nr }

func ioctlIOWR(nr, size uintptr) uintptr {
	const read, write = 2, 1
	return (read|write)<<30 | size<<16 | drmIoctlType<<8 | nr
}

// The structs have the layout of the C structs. A pointer goes in as a uint64.
type modeCardRes struct {
	fbIDPtr, crtcIDPtr, connectorIDPtr, encoderIDPtr     uint64
	countFbs, countCrtcs, countConnectors, countEncoders uint32
	minWidth, maxWidth, minHeight, maxHeight             uint32
}

type modeGetConnector struct {
	encodersPtr, modesPtr, propsPtr, propValuesPtr uint64
	countModes, countProps, countEncoders          uint32
	encoderID, connectorID, connectorType          uint32
	connectorTypeID, connection                    uint32
	mmWidth, mmHeight, subpixel, pad               uint32
}

type modeGetProperty struct {
	valuesPtr, enumBlobPtr      uint64
	propID, flags               uint32
	name                        [drmPropNameLen]byte
	countValues, countEnumBlobs uint32
}

type modeObjSetProperty struct {
	value                  uint64
	propID, objID, objType uint32
}

var (
	ioctlGetResources   = ioctlIOWR(drmCmdModeGetResources, unsafe.Sizeof(modeCardRes{}))
	ioctlGetConnector   = ioctlIOWR(drmCmdModeGetConnector, unsafe.Sizeof(modeGetConnector{}))
	ioctlGetProperty    = ioctlIOWR(drmCmdModeGetProperty, unsafe.Sizeof(modeGetProperty{}))
	ioctlObjSetProperty = ioctlIOWR(drmCmdModeObjSetProperty, unsafe.Sizeof(modeObjSetProperty{}))
	ioctlSetMaster      = ioctlIO(drmCmdSetMaster)
)

// drmFile is an open DRM device node.
type drmFile struct {
	fd   int
	path string
}

// openCards opens each /dev/dri/card* node that can set a mode. A card of a
// render only driver, for example v3d of the Raspberry Pi, has no connectors and
// answers "not supported"; it is not in the list. Raspberry Pi 5 has such a card
// next to the display card, so the code does not assume card0.
func openCards() ([]drmCard, error) {
	paths, err := filepath.Glob("/dev/dri/card*")
	if err != nil {
		return nil, err
	}
	var cards []drmCard
	var lastErr error
	for _, path := range paths {
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			lastErr = fmt.Errorf("open %s: %w", path, err)
			continue
		}
		f := &drmFile{fd: fd, path: path}
		var res modeCardRes
		if err := ioctl(fd, ioctlGetResources, unsafe.Pointer(&res)); err != nil {
			// No display function on this card.
			_ = f.close()
			if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EINVAL) {
				lastErr = fmt.Errorf("%s: %w", path, err)
			}
			continue
		}
		cards = append(cards, f)
	}
	if len(cards) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("no DRM card with a display function was found in /dev/dri")
	}
	return cards, nil
}

// ioctl makes one call. A signal can stop a DRM call before it ends. The kernel
// then asks for a new try, as libdrm does.
func ioctl(fd int, request uintptr, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(arg))
		switch errno {
		case 0:
			return nil
		case unix.EINTR, unix.EAGAIN:
			continue
		default:
			return errno
		}
	}
}

func (f *drmFile) close() error { return unix.Close(f.fd) }

func (f *drmFile) takeMaster() error {
	return ioctl(f.fd, ioctlSetMaster, nil)
}

func (f *drmFile) setProperty(connector, property uint32, value uint64) error {
	arg := modeObjSetProperty{value: value, propID: property, objID: connector, objType: drmObjectConnector}
	return ioctl(f.fd, ioctlObjSetProperty, unsafe.Pointer(&arg))
}

// connectors lists the connectors of the card, with the ID of the DPMS property
// of each.
func (f *drmFile) connectors() ([]drmConnector, error) {
	ids, err := f.connectorIDs()
	if err != nil {
		return nil, err
	}
	out := make([]drmConnector, 0, len(ids))
	for _, id := range ids {
		c, err := f.connector(id)
		if err != nil {
			return nil, fmt.Errorf("%s: connector %d: %w", f.path, id, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// connectorIDs asks twice: the first call gives the count and the second call
// gives the list. A connector that appears between the two calls makes the
// count grow, so the loop asks again.
func (f *drmFile) connectorIDs() ([]uint32, error) {
	var res modeCardRes
	for range 4 {
		res = modeCardRes{}
		if err := ioctl(f.fd, ioctlGetResources, unsafe.Pointer(&res)); err != nil {
			return nil, fmt.Errorf("%s: get resources: %w", f.path, err)
		}
		count := res.countConnectors
		if count == 0 {
			return nil, nil
		}
		ids := make([]uint32, count)
		res = modeCardRes{connectorIDPtr: uint64(uintptr(unsafe.Pointer(&ids[0]))), countConnectors: count}
		err := ioctl(f.fd, ioctlGetResources, unsafe.Pointer(&res))
		runtime.KeepAlive(ids)
		if err != nil {
			return nil, fmt.Errorf("%s: get resources: %w", f.path, err)
		}
		if res.countConnectors <= count {
			return ids[:res.countConnectors], nil
		}
	}
	return nil, fmt.Errorf("%s: the list of connectors keeps changing", f.path)
}

// connector reads one connector: its state and its properties. It finds the
// property that is called "DPMS".
func (f *drmFile) connector(id uint32) (drmConnector, error) {
	out := drmConnector{id: id}
	for range 4 {
		// The first call gives the number of properties. It also makes the kernel
		// probe the connector, so the connection state is current.
		info := modeGetConnector{connectorID: id}
		if err := ioctl(f.fd, ioctlGetConnector, unsafe.Pointer(&info)); err != nil {
			return out, err
		}
		out.connected = info.connection == drmConnected
		if info.countProps == 0 {
			return out, nil
		}

		props := make([]uint32, info.countProps)
		values := make([]uint64, info.countProps)
		info = modeGetConnector{
			connectorID:   id,
			propsPtr:      uint64(uintptr(unsafe.Pointer(&props[0]))),
			propValuesPtr: uint64(uintptr(unsafe.Pointer(&values[0]))),
			countProps:    uint32(len(props)),
		}
		err := ioctl(f.fd, ioctlGetConnector, unsafe.Pointer(&info))
		runtime.KeepAlive(props)
		runtime.KeepAlive(values)
		if err != nil {
			return out, err
		}
		if int(info.countProps) > len(props) {
			continue // a property appeared between the calls
		}
		out.connected = info.connection == drmConnected

		for _, prop := range props[:info.countProps] {
			name, err := f.propertyName(prop)
			if err != nil {
				return out, err
			}
			if name == "DPMS" {
				out.dpms = prop
				break
			}
		}
		return out, nil
	}
	return out, errors.New("the properties keep changing")
}

// propertyName gives the name of a property. The call gives the name and the
// counts, and it needs no lists.
func (f *drmFile) propertyName(id uint32) (string, error) {
	prop := modeGetProperty{propID: id}
	if err := ioctl(f.fd, ioctlGetProperty, unsafe.Pointer(&prop)); err != nil {
		return "", fmt.Errorf("property %d: %w", id, err)
	}
	name := prop.name[:]
	for i, c := range name {
		if c == 0 {
			name = name[:i]
			break
		}
	}
	return string(name), nil
}

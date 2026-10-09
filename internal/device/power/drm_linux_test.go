package power

import (
	"testing"
	"unsafe"
)

// The kernel knows a call by its number, and a struct of the wrong size gives a
// wrong number and a call that fails or writes to the wrong place. The values are
// the ones that libdrm has for these calls on amd64, arm and arm64.
func TestDRMIoctlNumbers(t *testing.T) {
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"DRM_IOCTL_MODE_GETRESOURCES", ioctlGetResources, 0xC04064A0},
		{"DRM_IOCTL_MODE_GETCONNECTOR", ioctlGetConnector, 0xC05064A7},
		{"DRM_IOCTL_MODE_GETPROPERTY", ioctlGetProperty, 0xC04064AA},
		{"DRM_IOCTL_MODE_OBJ_SETPROPERTY", ioctlObjSetProperty, 0xC01864BA},
		{"DRM_IOCTL_SET_MASTER", ioctlSetMaster, 0x641E},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %#x, want %#x", tt.name, tt.got, tt.want)
		}
	}
}

// The struct sizes are in the numbers above. This test names them, so that a
// change of a field shows up as one clear line.
func TestDRMStructSizes(t *testing.T) {
	if got := unsafe.Sizeof(modeCardRes{}); got != 64 {
		t.Errorf("drm_mode_card_res is %d bytes, want 64", got)
	}
	if got := unsafe.Sizeof(modeGetConnector{}); got != 80 {
		t.Errorf("drm_mode_get_connector is %d bytes, want 80", got)
	}
	if got := unsafe.Sizeof(modeGetProperty{}); got != 64 {
		t.Errorf("drm_mode_get_property is %d bytes, want 64", got)
	}
	if got := unsafe.Sizeof(modeObjSetProperty{}); got != 24 {
		t.Errorf("drm_mode_obj_set_property is %d bytes, want 24", got)
	}
}

// kernelCount is a variable, so the compiler cannot give the lists a fixed size.
var kernelCount uint32 = 4

// A list whose address goes to the kernel as a plain integer must be on the
// heap. Go can move a stack, and it does not correct such an integer. A short
// list is on the stack when the compiler can keep it there, and then the
// allocation count is 0.
func TestKernelBuffersAreOnTheHeap(t *testing.T) {
	allocs := testing.AllocsPerRun(20, func() {
		props := kernelBuffer[uint32](kernelCount)
		values := kernelBuffer[uint64](kernelCount)
		_ = uint64(uintptr(unsafe.Pointer(&props[0]))) + uint64(uintptr(unsafe.Pointer(&values[0])))
	})
	if allocs < 2 {
		t.Errorf("the two lists made %v allocations on the heap, want 2", allocs)
	}
}

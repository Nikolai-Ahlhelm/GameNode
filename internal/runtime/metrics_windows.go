//go:build windows

package runtime

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

func (nativeRuntime) Metrics(_ context.Context, identity Identity) (Metrics, error) {
	if err := verifyWindows(identity); err != nil {
		return Metrics{}, err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(identity.PID))
	if err != nil {
		return Metrics{}, err
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return Metrics{}, err
	}
	memory := processMemoryCounters{CB: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	ok, _, callErr := getProcessMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&memory)), uintptr(memory.CB))
	if ok == 0 {
		return Metrics{}, fmt.Errorf("GetProcessMemoryInfo: %w", callErr)
	}
	return Metrics{CPUTime: time.Duration(kernel.Nanoseconds() + user.Nanoseconds()), MemoryBytes: uint64(memory.WorkingSetSize)}, nil
}

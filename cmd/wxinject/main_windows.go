//go:build windows

// Command wxinject loads a DLL into a running WeChat.exe process.
//
// It is built as a 32-bit Windows binary (GOOS=windows GOARCH=386) and executed
// with wine inside the WxCat container: WeChat 3.9 is a 32-bit process, and
// Windows does not allow a 64-bit process's remote thread to call LoadLibraryA
// in a 32-bit target. The technique is CreateRemoteThread + LoadLibraryA:
// allocate a buffer in the target process, write the DLL path, then call
// LoadLibraryA in a new remote thread.
package main

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const defaultDLL = `C:\Program Files\Tencent\WeChat\sdk.dll`

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procVirtualAllocEx     = kernel32.NewProc("VirtualAllocEx")
	procCreateRemoteThread = kernel32.NewProc("CreateRemoteThread")
	procGetExitCodeThread  = kernel32.NewProc("GetExitCodeThread")
	procLoadLibraryA       = kernel32.NewProc("LoadLibraryA")
)

func main() {
	dll := defaultDLL
	if len(os.Args) > 1 {
		dll = os.Args[1]
	}

	if err := enableDebugPrivilege(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: SeDebugPrivilege: %v\n", err)
	}

	pid, err := findProcess("WeChat.exe")
	if err != nil {
		fatal(err)
	}
	fmt.Printf("found WeChat.exe pid=%d\n", pid)

	if err := inject(pid, dll); err != nil {
		fatal(err)
	}
	fmt.Printf("injected %s\n", dll)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "wxinject:", err)
	os.Exit(1)
}

func findProcess(name string) (uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, fmt.Errorf("snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return 0, fmt.Errorf("first process: %w", err)
	}
	for {
		exe := windows.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(exe, name) {
			return entry.ProcessID, nil
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return 0, fmt.Errorf("%s is not running", name)
}

// enableDebugPrivilege lets an elevated process open protected targets such as
// WeChat. Without it OpenProcess can fail with ERROR_ACCESS_DENIED even when the
// user is an administrator.
func enableDebugPrivilege() error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()

	name, err := windows.UTF16PtrFromString("SeDebugPrivilege")
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return err
	}
	state := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{{
			Luid:       luid,
			Attributes: windows.SE_PRIVILEGE_ENABLED,
		}},
	}
	return windows.AdjustTokenPrivileges(token, false, &state, 0, nil, nil)
}

func inject(pid uint32, dll string) error {
	const access = windows.PROCESS_CREATE_THREAD |
		windows.PROCESS_QUERY_INFORMATION |
		windows.PROCESS_VM_OPERATION |
		windows.PROCESS_VM_WRITE |
		windows.PROCESS_VM_READ

	proc, err := windows.OpenProcess(access, false, pid)
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(proc)

	path, err := windows.BytePtrFromString(dll)
	if err != nil {
		return err
	}
	size := uintptr(len(dll) + 1)

	remote, _, errno := procVirtualAllocEx.Call(
		uintptr(proc), 0, size,
		uintptr(windows.MEM_COMMIT|windows.MEM_RESERVE),
		uintptr(windows.PAGE_READWRITE),
	)
	if remote == 0 {
		return fmt.Errorf("VirtualAllocEx failed: %v", errno)
	}

	var written uintptr
	if err := windows.WriteProcessMemory(proc, remote, (*byte)(unsafe.Pointer(path)), size, &written); err != nil {
		return fmt.Errorf("WriteProcessMemory: %w", err)
	}

	if err := procLoadLibraryA.Find(); err != nil {
		return fmt.Errorf("resolve LoadLibraryA: %w", err)
	}

	thread, _, errno := procCreateRemoteThread.Call(
		uintptr(proc), 0, 0,
		procLoadLibraryA.Addr(), remote, 0, 0,
	)
	if thread == 0 {
		return fmt.Errorf("CreateRemoteThread failed: %v", errno)
	}
	defer windows.CloseHandle(windows.Handle(thread))

	event, err := windows.WaitForSingleObject(windows.Handle(thread), 15000)
	if err != nil {
		return fmt.Errorf("WaitForSingleObject: %w", err)
	}
	if event == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("remote thread timed out")
	}

	var exitCode uint32
	ok, _, errno := procGetExitCodeThread.Call(thread, uintptr(unsafe.Pointer(&exitCode)))
	if ok == 0 {
		return fmt.Errorf("GetExitCodeThread failed: %v", errno)
	}
	if exitCode == 0 {
		return fmt.Errorf("LoadLibraryA returned NULL (wrong DLL path or missing dependency)")
	}
	return nil
}

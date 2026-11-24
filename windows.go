package main

import (
	"context"
	"fmt"

	"os"
	"os/exec"
	"path/filepath"

	"syscall"
	"unsafe"
)

type toolPaths struct{ base string }

func firstFile(paths ...string) string {
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

func (t toolPaths) uxplay() string {
	path, _ := exec.LookPath("uxplay.exe")
	return firstFile(os.Getenv("MIRRORPORT_UXPLAY"), filepath.Join(t.base, "engines", "airplay", "backend", "uxplay.exe"),
		filepath.Join(t.base, "engines", "airplay", "uxplay.exe"),
		`C:\msys64\ucrt64\bin\uxplay.exe`, `C:\msys64\clangarm64\bin\uxplay.exe`, path)
}

func (t toolPaths) ffmpeg() string {
	path, _ := exec.LookPath("ffmpeg.exe")
	return firstFile(os.Getenv("MIRRORPORT_FFMPEG"), filepath.Join(t.base, "engines", "ffmpeg", "bin", "ffmpeg.exe"),
		`C:\ffmpeg\bin\ffmpeg.exe`, path)
}

func hiddenCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd
}

func shellOpen(target string) error {
	open, _ := syscall.UTF16PtrFromString("open")
	value, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	var directory *uint16
	if filepath.IsAbs(target) {
		directory, _ = syscall.UTF16PtrFromString(filepath.Dir(target))
	}
	shellExecute := syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW")
	result, _, _ := shellExecute.Call(0, uintptr(unsafe.Pointer(open)), uintptr(unsafe.Pointer(value)), 0, uintptr(unsafe.Pointer(directory)), 1)
	if result <= 32 {
		return fmt.Errorf("ShellExecuteW: %d", result)
	}
	return nil
}

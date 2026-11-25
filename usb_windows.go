package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	usbABIVersion    = 18
	usbReleaseFolder = "iPhoneMirror-v1.8.3-win-x64"
	usbDLLName       = "iPhoneMirror.Core.dll"
	usbHelperName    = "iPhoneMirror.UsbConfigurationSwitch.exe"
)

func (t toolPaths) usb() string {
	return firstFile(os.Getenv("MIRRORPORT_USB_CORE"),
		filepath.Join(t.base, "engines", "usb", usbReleaseFolder, usbDLLName),
		filepath.Join(t.base, "engines", "usb", usbDLLName))
}

func (t toolPaths) usbReady() bool {
	return t.usb() != "" && firstFile(filepath.Join(t.base, usbHelperName)) != ""
}

// ABI definitions for the iPhoneMirror v1.8.3 x64 CoreApi.h.
type usbDeviceInfo struct {
	size, version, id, port              uint32
	state, connected, paired, accessible int32
	udid, name                           [128]uint16
	model                                [64]uint16
	osVersion, transport                 [32]uint16
	status                               [192]uint16
}

type usbCaptureOptions struct {
	size, version, width, height, fps uint32
	playAudio                         int32
	volume                            float32
	reserved                          [5]uint32
}

type usbCaptureStatus struct {
	size, version             uint32
	state                     int32
	width, height             uint32
	fps, latency              float64
	videoFrames, audioPackets uint64
	rate, channels            uint32
	failure, stage, code      int32
	message                   [192]uint16
}

type usbFrameInfo struct {
	size, version, width, height, stride, format uint32
	timestamp                                    int64
}

type usbCore struct {
	refresh, create, stop, destroy, status, timestamp, frame, volume, shutdown *syscall.Proc
	scanMu                                                                     sync.Mutex
}

var usbRuntime struct {
	sync.Mutex
	path   string
	core   *usbCore
	closed bool
}

func loadUSBCore(path string) (*usbCore, error) {
	if path == "" {
		return nil, errors.New("USB受信エンジンを配置してください。READMEに初回セットアップ手順があります。")
	}
	if runtime.GOARCH != "amd64" {
		return nil, errors.New("USB受信エンジンにはWindows x64が必要です。")
	}
	usbRuntime.Lock()
	defer usbRuntime.Unlock()
	if usbRuntime.closed {
		return nil, errors.New("USB受信エンジンを終了しています。")
	}
	if usbRuntime.core != nil {
		if usbRuntime.path != path {
			return nil, errors.New("USB受信エンジンを切り替えるにはMirrorPortを再起動してください。")
		}
		return usbRuntime.core, nil
	}
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	load := syscall.NewLazyDLL("kernel32.dll").NewProc("LoadLibraryExW")
	const searchDLLDirectoryAndSystem32 = 0x100 | 0x800
	handle, _, err := load.Call(uintptr(unsafe.Pointer(pathPtr)), 0, searchDLLDirectoryAndSystem32)
	if handle == 0 {
		return nil, fmt.Errorf("USB受信エンジンを読み込めません。配布ZIPをすべて展開してください (%w)", err)
	}
	dll := &syscall.DLL{Name: path, Handle: syscall.Handle(handle)}
	core := &usbCore{}
	var initialize, version *syscall.Proc
	for _, proc := range []struct {
		name   string
		target **syscall.Proc
	}{
		{"im_initialize", &initialize}, {"im_shutdown", &core.shutdown}, {"im_api_version", &version},
		{"im_refresh_devices_ex", &core.refresh}, {"im_session_create", &core.create},
		{"im_session_stop", &core.stop}, {"im_session_destroy", &core.destroy},
		{"im_session_get_status", &core.status}, {"im_session_get_latest_video_timestamp", &core.timestamp},
		{"im_session_copy_latest_video_frame", &core.frame}, {"im_session_set_audio_volume", &core.volume},
	} {
		*proc.target, err = dll.FindProc(proc.name)
		if err != nil {
			_ = dll.Release()
			return nil, fmt.Errorf("USB受信エンジンのAPIが対応していません (%s)", proc.name)
		}
	}
	if value, _, _ := version.Call(); value != usbABIVersion {
		_ = dll.Release()
		return nil, fmt.Errorf("USB受信エンジンのAPIバージョン%dは対応していません", value)
	}
	if code, _, _ := initialize.Call(); int32(code) != 0 {
		core.shutdown.Call()
		_ = dll.Release()
		return nil, fmt.Errorf("USB受信エンジンを初期化できません (%d)", int32(code))
	}
	usbRuntime.path, usbRuntime.core = path, core
	return core, nil
}

func shutdownUSBCore() {
	usbRuntime.Lock()
	defer usbRuntime.Unlock()
	usbRuntime.closed = true
	if usbRuntime.core != nil {
		usbRuntime.core.shutdown.Call()
	}
}

type usbDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	udid string
}

func (core *usbCore) listDevices() ([]usbDevice, error) {
	core.scanMu.Lock()
	defer core.scanMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var count uint32
	code, _, _ := core.refresh.Call(0, uintptr(unsafe.Pointer(&count)), 1)
	if int32(code) != 0 || count > 256 {
		return nil, fmt.Errorf("USB端末を取得できません。Apple DevicesとUSBケーブルを確認してください (%d)", int32(code))
	}
	if count == 0 {
		return []usbDevice{}, nil
	}
	info := make([]usbDeviceInfo, count)
	code, _, _ = core.refresh.Call(uintptr(unsafe.Pointer(&info[0])), uintptr(unsafe.Pointer(&count)), 0)
	if int32(code) != 0 || count > uint32(len(info)) {
		return nil, fmt.Errorf("USB端末を取得できません。Apple DevicesとUSBケーブルを確認してください (%d)", int32(code))
	}
	devices := make([]usbDevice, 0, count)
	for _, item := range info[:count] {
		udid := syscall.UTF16ToString(item.udid[:])
		if item.connected == 0 || udid == "" {
			continue
		}
		id := sha256.Sum256([]byte(udid))
		name := syscall.UTF16ToString(item.name[:])
		if name == "" {
			name = "iPhone / iPad"
		}
		devices = append(devices, usbDevice{ID: fmt.Sprintf("%x", id[:16]), Name: name, udid: udid})
	}
	return devices, nil
}

// startUSB runs with the receiver lock held. Slow native operations run off the HTTP thread.
func (r *receiver) startUSB(name string, options receiverOptions, audio audioSettings) error {
	path := r.tools.usb()
	if !r.tools.usbReady() {
		return errors.New("setup-usb.ps1でUSB受信エンジンと補助プログラムを準備してください。")
	}
	if len(options.VideoDevice) != 32 {
		return errors.New("USB接続したiPhoneを選んでください。")
	}
	if options.Display != "" && options.Display != "auto" && options.Display != "browser" {
		return errors.New("USB接続の表示先はブラウザー・OBSです。")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &receiverSession{
		ctx: ctx, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}),
		status: receiverStatus{State: "starting", Name: name, Source: "usb", Display: "browser",
			ID: fmt.Sprintf("%x", randomBytes(12)), VideoDevice: options.VideoDevice},
		jpegQuality: options.JPEGQuality,
	}
	s.setAudio(audio)
	r.current, r.last = s, receiverStatus{}
	s.startMosaicWorker(r)
	go func() {
		err := r.runUSB(s, path, options)
		cancel()
		r.finish(s, "USB接続が終了しました。データ通信対応ケーブルとiPhoneの信頼設定を確認してください。", err)
	}()
	return nil
}

func (r *receiver) runUSB(s *receiverSession, path string, options receiverOptions) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	core, err := loadUSBCore(path)
	if err != nil {
		return err
	}
	devices, err := core.listDevices()
	if err != nil {
		return err
	}
	var udid *uint16
	for _, device := range devices {
		if device.ID == options.VideoDevice {
			udid, err = syscall.UTF16PtrFromString(device.udid)
			if err != nil {
				return err
			}
			break
		}
	}
	if udid == nil {
		return errors.New("選択したiPhoneがUSBで見つかりません")
	}
	edge := uint32(1920)
	if options.Resolution == "1280x720" {
		edge = 1280
	}
	settings := usbCaptureOptions{version: usbABIVersion, width: edge, height: edge, fps: uint32(options.FPS),
		playAudio: 1, volume: float32(s.gain.Load()) / 100}
	settings.size = uint32(unsafe.Sizeof(settings))
	var handle uint64
	if s.ctx.Err() != nil {
		return nil
	}
	code, _, _ := core.create.Call(uintptr(unsafe.Pointer(udid)), uintptr(unsafe.Pointer(&settings)), uintptr(unsafe.Pointer(&handle)))
	if int32(code) != 0 || handle == 0 {
		return fmt.Errorf("USBセッションを開始できません。iPhoneMirrorのドライバー管理ツールで対象端末を確認してください (%d)", int32(code))
	}
	defer core.destroy.Call(uintptr(handle))
	defer func() {
		if result, _, _ := core.stop.Call(uintptr(handle)); int32(result) != 0 {
			fmt.Fprintf(&s.output, "\n[usb] USB終了処理: %d\n", int32(result))
		}
	}()

	ticker := time.NewTicker(time.Second / time.Duration(options.FPS))
	defer ticker.Stop()
	pixels := make([]byte, int(edge)*int(edge)*4)
	var timestamp int64
	appliedGain := int32(math.Round(float64(settings.volume * 100)))
	nextStatus := time.Time{}
	var lastMessage string
	for {
		select {
		case <-s.ctx.Done():
			return nil
		case now := <-ticker.C:
			if gain := s.gain.Load(); gain != appliedGain {
				bits := math.Float32bits(float32(gain) / 100)
				if result, _, _ := core.volume.Call(uintptr(handle), uintptr(bits)); int32(result) == 0 {
					appliedGain = gain
				}
			}
			if !now.Before(nextStatus) {
				var state usbCaptureStatus
				state.size = uint32(unsafe.Sizeof(state))
				state.version = usbABIVersion
				result, _, _ := core.status.Call(uintptr(handle), uintptr(unsafe.Pointer(&state)))
				if int32(result) != 0 {
					return fmt.Errorf("USBの接続状態を取得できません (%d)", int32(result))
				}
				message := syscall.UTF16ToString(state.message[:])
				if message != "" && message != lastMessage {
					fmt.Fprintln(&s.output, "[usb]", message)
					lastMessage = message
				}
				if state.state == 6 || state.state == 7 {
					return fmt.Errorf("USB受信エンジンが停止しました (%d)", state.code)
				}
				nextStatus = now.Add(250 * time.Millisecond)
			}
			var latest int64
			result, _, _ := core.timestamp.Call(uintptr(handle), uintptr(unsafe.Pointer(&latest)))
			if int32(result) != 0 || latest == 0 || latest == timestamp {
				continue
			}
			info := usbFrameInfo{version: usbABIVersion}
			info.size = uint32(unsafe.Sizeof(info))
			size := uint32(len(pixels))
			result, _, _ = core.frame.Call(uintptr(handle), uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&pixels[0])),
				uintptr(unsafe.Pointer(&size)), uintptr(edge), uintptr(edge))
			if int32(result) != 0 || size > uint32(len(pixels)) {
				continue
			}
			frame, err := encodeUSBFrame(pixels[:size], info, options.JPEGQuality)
			if err != nil {
				return err
			}
			timestamp = info.timestamp
			if timestamp == 0 {
				timestamp = latest
			}
			r.setFrame(s, frame)
		}
	}
}

func encodeUSBFrame(pixels []byte, info usbFrameInfo, quality int) ([]byte, error) {
	width, height, stride := int(info.width), int(info.height), int(info.stride)
	if info.format != 1 || width < 1 || height < 1 || width > 4096 || height > 4096 ||
		stride < width*4 || stride > len(pixels)/height {
		return nil, errors.New("USB受信エンジンから読み取った映像サイズが無効です")
	}
	for y := 0; y < height; y++ {
		for offset, end := y*stride, y*stride+width*4; offset < end; offset += 4 {
			pixels[offset], pixels[offset+2] = pixels[offset+2], pixels[offset]
		}
	}
	img := image.RGBA{Pix: pixels, Stride: stride, Rect: image.Rect(0, 0, width, height)}
	var result bytes.Buffer
	err := jpeg.Encode(&result, &img, &jpeg.Options{Quality: 100 - quality*4})
	return result.Bytes(), err
}

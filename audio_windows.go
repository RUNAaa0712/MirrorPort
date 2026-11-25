package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	audioRate       = 44100
	audioBlockBytes = audioRate / 100 * 4 // 10 ms, stereo signed 16-bit PCM
	waveDone        = 1
)

var (
	winmm            = syscall.NewLazyDLL("winmm.dll")
	waveOutOpen      = winmm.NewProc("waveOutOpen")
	waveOutPrepare   = winmm.NewProc("waveOutPrepareHeader")
	waveOutWrite     = winmm.NewProc("waveOutWrite")
	waveOutReset     = winmm.NewProc("waveOutReset")
	waveOutUnprepare = winmm.NewProc("waveOutUnprepareHeader")
	waveOutClose     = winmm.NewProc("waveOutClose")
	audioKernel      = syscall.NewLazyDLL("kernel32.dll")
	createAudioEvent = audioKernel.NewProc("CreateEventW")
)

type pcmFormat struct {
	tag, channels           uint16
	rate, bytesPerSecond    uint32
	blockAlign, bits, extra uint16
}

type waveHeader struct {
	data           uintptr
	size, recorded uint32
	user           uintptr
	flags, loops   uint32
	next, reserved uintptr
}

func (s *receiverSession) startAudio(listener net.Listener) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		stop := context.AfterFunc(s.ctx, func() { _ = listener.Close() })
		defer stop()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			stopConnection := context.AfterFunc(s.ctx, func() { _ = conn.Close() })
			pcm := make(chan [audioBlockBytes]byte, 9)
			go queuePCM(s.ctx, conn, pcm)
			err = s.playPCM(pcm)
			if err != nil && s.ctx.Err() == nil {
				fmt.Fprintf(&s.output, "\n[audio] %v\n", err)
				// Keep draining the source after an audio-device failure so
				// capture and video can continue.
				for range pcm {
				}
			}
			stopConnection()
			_ = conn.Close()
		}
	}()
	return done
}

func (s *receiverSession) playPCM(pcm <-chan [audioBlockBytes]byte) error {
	// Drain capture independently of the audio device. If Windows or the
	// sender delivers a burst, play recent audio instead of accumulating delay.
	message, _, err := createAudioEvent.Call(0, 0, 0, 0)
	if message == 0 {
		return fmt.Errorf("Windows音声イベントを作成できません: %w", err)
	}
	defer syscall.CloseHandle(syscall.Handle(message))
	format := pcmFormat{tag: 1, channels: 2, rate: audioRate, bytesPerSecond: audioRate * 4, blockAlign: 4, bits: 16}
	var device uintptr
	const callbackEvent = 0x50000
	code, _, _ := waveOutOpen.Call(uintptr(unsafe.Pointer(&device)), 0xffffffff, uintptr(unsafe.Pointer(&format)), message, 0, callbackEvent)
	if code != 0 {
		return fmt.Errorf("Windows音声出力を開けません (%d)", code)
	}
	defer waveOutClose.Call(device)
	var buffers [4][audioBlockBytes]byte
	var headers [len(buffers)]waveHeader
	var pinned runtime.Pinner
	pinned.Pin(&buffers)
	pinned.Pin(&headers)
	defer pinned.Unpin()
	prepared := 0
	const headerSize = unsafe.Sizeof(waveHeader{})
	defer func() {
		waveOutReset.Call(device)
		for i := range headers[:prepared] {
			waveOutUnprepare.Call(device, uintptr(unsafe.Pointer(&headers[i])), headerSize)
		}
	}()
	for i := range headers {
		headers[i].data = uintptr(unsafe.Pointer(&buffers[i]))
		headers[i].size = audioBlockBytes
		if code, _, _ := waveOutPrepare.Call(device, uintptr(unsafe.Pointer(&headers[i])), headerSize); code != 0 {
			return fmt.Errorf("Windows音声出力を準備できません (%d)", code)
		}
		prepared++
	}

	ctx := s.ctx
	var first [audioBlockBytes]byte
	select {
	case <-ctx.Done():
		return nil
	case initial, ok := <-pcm:
		if !ok {
			return nil
		}
		first = initial
	}
	preroll := time.NewTimer(35 * time.Millisecond)
	defer preroll.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-preroll.C:
	}
	firstBlock := true
	for i := 0; ; i = (i + 1) % len(headers) {
		header := &headers[i]
		const inQueue = 0x10
		for flags := atomic.LoadUint32(&header.flags); flags&inQueue != 0 && flags&waveDone == 0; flags = atomic.LoadUint32(&header.flags) {
			if s.ctx.Err() != nil {
				return nil
			}
			_, _ = syscall.WaitForSingleObject(syscall.Handle(message), 35)
		}
		block := buffers[i][:]
		if firstBlock {
			copy(block, first[:])
			firstBlock = false
		} else {
			select {
			case <-ctx.Done():
				return nil
			case data, ok := <-pcm:
				if !ok {
					return nil
				}
				copy(block, data[:])
			default:
				// Keep the Windows playback clock running through brief Wi-Fi gaps.
				clear(block)
			}
		}
		scalePCM(block, s.gain.Load())
		if code, _, _ := waveOutWrite.Call(device, uintptr(unsafe.Pointer(header)), headerSize); code != 0 {
			return fmt.Errorf("Windowsで音声を再生できません (%d)", code)
		}
	}
}

func queuePCM(ctx context.Context, input io.Reader, pcm chan [audioBlockBytes]byte) {
	defer close(pcm)
	for ctx.Err() == nil {
		var block [audioBlockBytes]byte
		if _, err := io.ReadFull(input, block[:]); err != nil {
			return
		}
		select {
		case pcm <- block:
		default:
			select {
			case <-pcm:
			default:
			}
			pcm <- block
		}
	}
}

func scalePCM(block []byte, gain int32) {
	if gain == 100 {
		return
	}
	if gain == 0 {
		clear(block)
		return
	}
	for offset := 0; offset < len(block); offset += 2 {
		sample := int16(binary.LittleEndian.Uint16(block[offset:]))
		binary.LittleEndian.PutUint16(block[offset:], uint16(int32(sample)*gain/100))
	}
}

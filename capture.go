package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type captureDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type captureDevices struct {
	Video []captureDevice `json:"video"`
	Audio []captureDevice `json:"audio"`
}

var (
	dshowDeviceLine      = regexp.MustCompile(`^"(.*)"(?: \(([a-z, ]+)\))?$`)
	dshowAlternativeLine = regexp.MustCompile(`^Alternative name "(.*)"$`)
)

func validCaptureID(id string) bool {
	return len(id) > 0 && len(id) <= 1024 && utf8.ValidString(id) && !strings.ContainsAny(id, ":\r\n\t\x00")
}

func parseCaptureDevices(output string) captureDevices {
	devices := captureDevices{Video: []captureDevice{}, Audio: []captureDevice{}}
	var name, kind, section string
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "[dshow @ ") {
			continue
		}
		_, line, _ = strings.Cut(line, "]")
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "DirectShow video devices"):
			section = "video"
		case strings.HasPrefix(line, "DirectShow audio devices"):
			section = "audio"
		default:
			if match := dshowDeviceLine.FindStringSubmatch(line); match != nil {
				name, kind = match[1], match[2]
				if kind == "" {
					kind = section
				}
			} else if match := dshowAlternativeLine.FindStringSubmatch(line); match != nil && name != "" {
				id := match[1]
				if validCaptureID(id) && strings.HasPrefix(id, "@device_") {
					device := captureDevice{ID: id, Name: name}
					for _, mediaType := range strings.Split(kind, ", ") {
						switch mediaType {
						case "video":
							devices.Video = append(devices.Video, device)
						case "audio":
							devices.Audio = append(devices.Audio, device)
						}
					}
				}
				name, kind = "", ""
			}
		}
	}
	return devices
}

func listCaptureDevices(ctx context.Context, ffmpeg string) (captureDevices, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := hiddenCommand(ctx, ffmpeg, "-hide_banner", "-nostdin", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	var output bytes.Buffer
	cmd.Stderr = &output
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	log := strings.ToValidUTF8(output.String(), "�")
	// FFmpeg exits with an error after listing devices because dummy is not an input.
	if !strings.Contains(log, "[dshow @ ") || ctx.Err() != nil {
		return captureDevices{}, fmt.Errorf("キャプチャー機器を取得できません。DirectShow対応のFFmpegを確認してください。 (%v)", err)
	}
	return parseCaptureDevices(log), nil
}

// startCapture runs with the receiver lock held.
func (r *receiver) startCapture(name string, options receiverOptions, audio audioSettings) error {
	ffmpeg := r.tools.ffmpeg()
	if ffmpeg == "" {
		return errors.New("有線入力にはFFmpegが必要です。")
	}
	if options.Display != "" && options.Display != "auto" && options.Display != "browser" {
		return errors.New("有線入力の表示先はブラウザー・OBSです。")
	}
	if !validCaptureID(options.VideoDevice) || !strings.HasPrefix(options.VideoDevice, "@device_") ||
		(options.AudioDevice != "" && (!validCaptureID(options.AudioDevice) || !strings.HasPrefix(options.AudioDevice, "@device_"))) {
		return errors.New("キャプチャーデバイスを選び直してください。")
	}

	var audioListener net.Listener
	if options.AudioDevice != "" {
		var err error
		audioListener, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("音声用ポートを開けません: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &receiverSession{
		ctx: ctx, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}),
		status: receiverStatus{State: "starting", Name: name, Source: "capture", Display: "browser",
			ID: fmt.Sprintf("%x", randomBytes(12)), VideoDevice: options.VideoDevice},
	}
	var audioDone <-chan struct{}
	if audioListener != nil {
		s.setAudio(audio)
		audioDone = s.startAudio(audioListener)
	}
	s.jpegQuality = options.JPEGQuality
	r.current = s
	r.last = receiverStatus{}
	s.startMosaicWorker(r)

	go func() {
		var err error
		for _, mode := range []string{"requested", "mjpeg", "default"} {
			if mode == "mjpeg" {
				fmt.Fprintln(&s.output, "[capture] Retrying the requested frame rate with MJPEG input.")
			} else if mode == "default" {
				fmt.Fprintln(&s.output, "[capture] Requested frame rate failed. Retrying with the device default.")
			}
			var started bool
			started, err = r.runCapture(s, ffmpeg, options, audioListener, mode)
			if started || err == nil || ctx.Err() != nil {
				break
			}
		}
		cancel()
		if audioDone != nil {
			<-audioDone
		}
		r.finish(s, "有線入力が終了しました。キャプチャーデバイスと受信ログを確認してください。", err)
	}()
	return nil
}

func captureArgs(options receiverOptions, audioAddress, mode string) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-f", "dshow", "-rtbufsize", "32M"}
	if mode != "default" {
		args = append(args, "-framerate", fmt.Sprint(options.FPS))
	}
	if mode == "mjpeg" {
		args = append(args, "-c:v", "mjpeg")
	}
	if options.Resolution != "" {
		args = append(args, "-video_size", options.Resolution)
	}
	input := "video=" + options.VideoDevice
	if audioAddress != "" {
		input += ":audio=" + options.AudioDevice
	}
	args = append(args, "-i", input, "-map", "0:v:0")
	if mode == "default" {
		args = append(args, "-vf", fmt.Sprintf("select=isnan(prev_selected_t)+gte(t-prev_selected_t\\,1/%d-0.001)", options.FPS))
	}
	args = append(args, jpegOutputArgs(options.JPEGQuality)...)
	if audioAddress != "" {
		args = append(args, "-map", "0:a:0", "-vn", "-ac", "2", "-ar", "44100", "-c:a", "pcm_s16le",
			"-flush_packets", "1", "-f", "s16le", "tcp://"+audioAddress)
	}
	return args
}

func (r *receiver) runCapture(s *receiverSession, ffmpeg string, options receiverOptions, audioListener net.Listener, mode string) (bool, error) {
	audioAddress := ""
	if audioListener != nil {
		audioAddress = audioListener.Addr().String()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	cmd := hiddenCommand(ctx, ffmpeg, captureArgs(options, audioAddress, mode)...)
	cmd.Stderr = &s.output
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return false, err
	}
	started := false
	frameErr := readFrames(stdout, func(frame []byte) {
		started = true
		r.setFrame(s, frame)
	})
	if !errors.Is(frameErr, io.EOF) && !errors.Is(frameErr, io.ErrUnexpectedEOF) {
		cancel()
	}
	if err := cmd.Wait(); err != nil {
		return started, err
	}
	return started, frameErr
}

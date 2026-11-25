package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	boundary = "mirrorport"
	maxFrame = 8 * 1024 * 1024
)

type receiverOptions struct {
	Name          string `json:"name"`
	Display       string `json:"display"`
	FPS           int    `json:"fps"`
	Source        string `json:"source"`
	VideoDevice   string `json:"videoDevice"`
	AudioDevice   string `json:"audioDevice"`
	Resolution    string `json:"resolution"`
	JPEGQuality   int    `json:"jpegQuality"`
	Muted         bool   `json:"muted"`
	Volume        *int   `json:"volume"`
	AirPlayTiming string `json:"airplayTiming"`
}

type receiverStatus struct {
	State string `json:"state"`

	Name        string         `json:"name,omitempty"`
	Display     string         `json:"display,omitempty"`
	PIN         string         `json:"pin,omitempty"`
	ID          string         `json:"id,omitempty"`
	Source      string         `json:"source,omitempty"`
	Audio       *audioSettings `json:"audio,omitempty"`
	Mosaic      *mosaicRegion  `json:"mosaic,omitempty"`
	VideoDevice string         `json:"videoDevice,omitempty"`

	Error string `json:"error,omitempty"`
	Log   string `json:"log,omitempty"`
}

type audioSettings struct {
	Volume int  `json:"volume"`
	Muted  bool `json:"muted"`
}

type receiverSession struct {
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	changed  chan struct{}
	last     []byte
	stopping bool
	status   receiverStatus
	output   tailLog
	// Gain is read by the PCM output loop. Zero covers both mute and zero volume.
	gain          atomic.Int32
	jpegQuality   int
	mosaicVersion uint64
	mosaicJobs    chan mosaicJob
	hasVideo      bool
}

type receiver struct {
	mu      sync.Mutex
	tools   toolPaths
	port    int
	current *receiverSession
	last    receiverStatus
}

func (r *receiver) status() receiverStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.current; s != nil {
		value := s.status
		var listening, connected bool
		value.Log, listening, connected = s.output.snapshot()
		switch {
		case s.stopping:
			value.State = "stopping"
		case s.hasVideo:
			value.State = "playing"
		case value.Display == "window" && connected:
			value.State = "playing"
		case listening:
			value.State = "waiting"
		}
		return value
	}
	if r.last.State == "" {
		return receiverStatus{State: "idle"}
	}
	return r.last
}

func (s *receiverSession) setAudio(settings audioSettings) {
	s.status.Audio = &settings
	gain := settings.Volume
	if settings.Muted {
		gain = 0
	}
	s.gain.Store(int32(gain))
}

func (r *receiver) updateAudio(id string, settings audioSettings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.current
	if s == nil || s.stopping || s.status.ID != id || s.status.Audio == nil {
		return errors.New("音声を調整できる受信機が見つかりません。")
	}
	s.setAudio(settings)
	return nil
}

func (r *receiver) start(options receiverOptions) error {
	name := strings.TrimSpace(options.Name)
	if name == "" || len(name) > 48 || !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n\t\x00") || strings.HasPrefix(name, "-") {
		return errors.New("受信機名は48バイト以内で入力してください。")
	}
	if options.FPS == 0 {
		options.FPS = 60
	}
	if options.FPS != 24 && options.FPS != 30 && options.FPS != 60 {
		return errors.New("フレームレートは 24、30、60 から選んでください。")
	}
	switch options.Resolution {
	case "", "1920x1080", "1280x720":
	default:
		return errors.New("解像度を選んでください。")
	}
	switch options.JPEGQuality {
	case 0, 3, 5, 8:
	default:
		return errors.New("JPEG画質を選んでください。")
	}
	if options.JPEGQuality == 0 {
		options.JPEGQuality = 5
	}
	volume := 100
	if options.Volume != nil {
		volume = *options.Volume
	}
	if volume < 0 || volume > 100 {
		return errors.New("音量は0〜100%で指定してください。")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return errors.New("受信機は起動中です。")
	}
	audio := audioSettings{Volume: volume, Muted: options.Muted}
	switch options.Source {
	case "capture":
		return r.startCapture(name, options, audio)
	case "usb":
		return r.startUSB(name, options, audio)
	case "", "airplay":
	default:
		return errors.New("接続方法を選んでください。")
	}
	if options.AirPlayTiming != "" && options.AirPlayTiming != "low" && options.AirPlayTiming != "sync" {
		return errors.New("AirPlayの同期設定を選んでください。")
	}

	path := r.tools.uxplay()
	if path == "" {
		return errors.New("AirPlay受信エンジンをセットアップしてください。")
	}
	display := options.Display
	ffmpeg := r.tools.ffmpeg()
	switch display {
	case "", "auto":
		display = "browser"
		if ffmpeg == "" {
			display = "window"
		}
	case "browser":
		if ffmpeg == "" {
			return errors.New("ブラウザーへの直接表示にはFFmpegが必要です。表示先をWindowsウィンドウに変更できます。")
		}
	case "window":
	default:
		return errors.New("映像の表示先を選んでください。")
	}

	dir, err := os.MkdirTemp("", "mirrorport-")
	if err != nil {
		return fmt.Errorf("受信機の作業フォルダーを作成できません: %w", err)
	}
	rcPath := filepath.Join(dir, "receiver.conf")
	if err := os.WriteFile(rcPath, []byte("# MirrorPort\n"), 0600); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("受信設定を保存できません: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	code := fmt.Sprintf("%04d", 1000+int(binary.BigEndian.Uint32(randomBytes(4))%9000))
	deviceID := net.HardwareAddr(append([]byte{0x02}, randomBytes(5)...)).String()
	s := &receiverSession{
		ctx: ctx, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}),
		status:      receiverStatus{State: "starting", Name: name, Source: "airplay", Display: display, PIN: code, ID: deviceID},
		jpegQuality: options.JPEGQuality,
	}
	s.setAudio(audio)
	audioListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("音声用ポートを開けません: %w", err)
	}
	port := r.port
	if port == 0 {
		port = firstAirPlayPort
	}
	args := []string{"-rc", "receiver.conf", "-n", name, "-nh", "-m", deviceID, "-pin", code, "-reg", "trusted.txt",
		"-p", fmt.Sprint(port), "-fps", fmt.Sprint(options.FPS), "-nofreeze"}
	synchronize := options.AirPlayTiming == "sync"
	if !synchronize {
		args = append(args, "-vsync", "no")
	}
	if options.Resolution != "" {
		args = append(args, "-s", options.Resolution)
	}
	var listener net.Listener
	if display == "browser" {
		listener, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			cancel()
			_ = audioListener.Close()
			_ = os.RemoveAll(dir)
			return fmt.Errorf("映像用ポートを開けません: %w", err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		pipeline := fmt.Sprintf("config-interval=-1 ! rtph264depay ! h264parse config-interval=-1 ! video/x-h264,stream-format=byte-stream,alignment=au ! tcpclientsink host=127.0.0.1 port=%d sync=%t async=false", port, synchronize)
		args = append(args, "-vrtp", pipeline)
	} else {
		args = append(args, "-vs", "d3d11videosink")
	}
	queue := "queue max-size-time=40000000 max-size-buffers=4 max-size-bytes=0 leaky=downstream"
	if synchronize {
		queue = "queue max-size-time=2000000000 max-size-buffers=0 max-size-bytes=0 leaky=downstream"
	}
	args = append(args, "-artp", fmt.Sprintf("pt=96 ! rtpL16depay ! audioconvert ! audioresample ! audio/x-raw,format=S16LE,rate=%d,channels=2 ! %s ! tcpclientsink host=127.0.0.1 port=%d sync=%t async=false",
		audioRate, queue, audioListener.Addr().(*net.TCPAddr).Port, synchronize))
	cmd := hiddenCommand(ctx, path, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &s.output, &s.output
	cmd.WaitDelay = 2 * time.Second
	runtimeDir := filepath.Dir(path)
	cmd.Env = append(os.Environ(), "PATH="+runtimeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if info, err := os.Stat(filepath.Join(runtimeDir, "gstreamer-1.0")); err == nil && info.IsDir() {
		cmd.Env = append(cmd.Env, "GST_PLUGIN_SYSTEM_PATH_1_0="+filepath.Join(runtimeDir, "gstreamer-1.0"))
	}
	if scanner := firstFile(filepath.Join(runtimeDir, "gst-plugin-scanner.exe")); scanner != "" {
		cmd.Env = append(cmd.Env, "GST_PLUGIN_SCANNER="+scanner)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = audioListener.Close()
		if listener != nil {
			_ = listener.Close()
		}
		_ = os.RemoveAll(dir)
		return fmt.Errorf("AirPlay受信機を起動できません: %w", err)
	}
	r.current = s
	r.last = receiverStatus{}
	s.startMosaicWorker(r)
	audioDone := s.startAudio(audioListener)
	var videoDone chan struct{}
	if listener != nil {
		videoDone = make(chan struct{})
		go func() {
			defer close(videoDone)
			r.receiveFrames(s, listener, ffmpeg, options.JPEGQuality)
		}()
	}
	go func() {
		err := cmd.Wait()
		cancel()
		if listener != nil {
			_ = listener.Close()
			<-videoDone
		}
		<-audioDone
		_ = os.RemoveAll(dir)
		r.finish(s, "AirPlay受信機が終了しました。受信ログを確認してください。", err)
	}()
	return nil
}

func (r *receiver) finish(s *receiverSession, message string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	log, _, _ := s.output.snapshot()
	r.last = receiverStatus{State: "idle", Log: log}
	if !s.stopping {
		r.last.State = "error"
		r.last.Source = s.status.Source
		r.last.Error = message
		if err != nil {
			r.last.Error += " (" + err.Error() + ")"
		}
	}
	r.current = nil
	close(s.done)
}

func (r *receiver) stop() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.current; s != nil {
		s.stopping = true
		s.cancel()
		return s.done
	}
	return nil
}

func (r *receiver) shutdown() {
	if done := r.stop(); done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (r *receiver) setFrame(s *receiverSession, frame []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.stopping {
		return
	}
	s.hasVideo = len(frame) != 0
	if !s.hasVideo {
		s.mosaicVersion++
	}
	if mask := s.status.Mosaic; mask != nil && s.hasVideo {
		job := mosaicJob{frame: frame, region: *mask, version: s.mosaicVersion}
		select {
		case <-s.mosaicJobs:
		default:
		}
		s.mosaicJobs <- job
		return
	}
	s.replaceFrame(frame)
}

func (s *receiverSession) replaceFrame(frame []byte) {
	s.last = frame
	close(s.changed)
	s.changed = make(chan struct{})
}

func readFrames(input io.Reader, publish func([]byte)) error {
	reader := multipart.NewReader(input, boundary)
	for {
		part, err := reader.NextPart()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(part, maxFrame+1))
		if err != nil {
			return err
		}
		if len(data) > maxFrame {
			return errors.New("video frame too large")
		}
		if part.Header.Get("Content-Type") == "image/jpeg" && len(data) >= 4 &&
			bytes.HasPrefix(data, []byte{0xff, 0xd8}) && bytes.HasSuffix(data, []byte{0xff, 0xd9}) {
			publish(data)
		}
	}
}

func (r *receiver) receiveFrames(s *receiverSession, listener net.Listener, ffmpeg string, quality int) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		stop := context.AfterFunc(s.ctx, func() { _ = conn.Close() })
		err = decodeFrames(s.ctx, ffmpeg, conn, &s.output, quality, func(frame []byte) { r.setFrame(s, frame) })
		stop()
		_ = conn.Close()
		r.setFrame(s, nil)
		if err != nil && !errors.Is(err, io.EOF) && s.ctx.Err() == nil {
			_, _ = fmt.Fprintf(&s.output, "\n[video] %v\n", err)
		}
	}
}

func jpegOutputArgs(quality int) []string {
	return []string{"-an", "-c:v", "mjpeg", "-q:v", fmt.Sprint(quality), "-threads", "2", "-flush_packets", "1",
		"-fps_mode", "passthrough", "-f", "mpjpeg", "-boundary_tag", boundary, "pipe:1"}
}

func decodeFrames(ctx context.Context, ffmpeg string, input net.Conn, output io.Writer, quality int, publish func([]byte)) error {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{
		"-hide_banner", "-loglevel", "warning", "-nostdin",
		"-probesize", "8192", "-analyzeduration", "100000", "-flags", "low_delay",
		"-f", "h264", "-threads", "2", "-i", "pipe:0",
	}
	cmd := hiddenCommand(childCtx, ffmpeg, append(args, jpegOutputArgs(quality)...)...)
	cmd.Stdin = input
	cmd.Stderr = output
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return err
	}
	frameErr := readFrames(stdout, publish)
	_ = input.Close() // Unblock os/exec's stdin copy after FFmpeg completes.
	if !errors.Is(frameErr, io.EOF) && !errors.Is(frameErr, io.ErrUnexpectedEOF) {
		cancel()
	}
	if err := cmd.Wait(); err != nil {
		return err
	}
	return frameErr
}

func (r *receiver) snapshot(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	var frame []byte
	if s := r.current; s != nil && !s.stopping {
		frame = s.last
	}
	r.mu.Unlock()
	if len(frame) == 0 {
		http.Error(w, "No active frame", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Disposition", "attachment; filename=mirrorport.jpg")
	_, _ = w.Write(frame)
}

func (r *receiver) video(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	s := r.current
	active := s != nil && !s.stopping && s.status.Display == "browser" && s.status.ID == req.URL.Query().Get("id")
	r.mu.Unlock()
	if !active {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
	controller := http.NewResponseController(w)
	writer := multipart.NewWriter(w)
	_ = writer.SetBoundary(boundary)
	header := textproto.MIMEHeader{"Content-Type": {"image/jpeg"}}
	var sent <-chan struct{}
	for {
		r.mu.Lock()
		frame, changed := s.last, s.changed
		r.mu.Unlock()
		if changed != sent && len(frame) != 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(3 * time.Second))
			header.Set("Content-Length", fmt.Sprint(len(frame)))
			part, err := writer.CreatePart(header)
			if err != nil {
				return
			}
			if _, err := part.Write(frame); err != nil || controller.Flush() != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Time{})
			sent = changed
		}
		select {
		case <-s.ctx.Done():
			return
		case <-req.Context().Done():
			return
		case <-changed:
		}
	}
}

type tailLog struct {
	mu        sync.Mutex
	data      []byte
	listening bool
	connected bool
}

func (l *tailLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.data = append(l.data, p...)
	if !l.listening {
		l.listening = bytes.Contains(l.data, []byte("Initialized server socket(s)"))
	}
	start := bytes.LastIndex(l.data, []byte("begin video stream wxh"))
	stop := max(bytes.LastIndex(l.data, []byte("video stream is stopping")), bytes.LastIndex(l.data, []byte("TEARDOWN request,  96=")))
	if start != -1 || stop != -1 {
		l.connected = start > stop
	}
	if max := 8000; len(l.data) > max {
		l.data = append([]byte(nil), l.data[len(l.data)-max:]...)
	}
	return len(p), nil
}

func (l *tailLog) snapshot() (string, bool, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.ToValidUTF8(string(l.data), "�"), l.listening, l.connected
}

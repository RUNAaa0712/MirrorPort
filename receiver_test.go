package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestMain(m *testing.M) {
	// The test executable can stand in for the UxPlay child process. No AirPlay
	// discovery or protocol negotiation is simulated.
	if len(os.Args) > 1 && os.Args[1] == "-rc" && os.Getenv("MIRRORPORT_TEST_RECEIVER") != "" {
		if err := runReceiverFixture(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runReceiverFixture() error {
	arg := func(name string) string {
		for i := 1; i < len(os.Args)-1; i++ {
			if os.Args[i] == name {
				return os.Args[i+1]
			}
		}
		return ""
	}
	if _, err := os.Stat(arg("-rc")); err != nil {
		return err
	}
	name, port, input := arg("-n"), "35000", os.Getenv("MIRRORPORT_TEST_H264")
	if name == "MirrorPort second" {
		port = "35003"
		input = os.Getenv("MIRRORPORT_TEST_H264_SECOND")
	} else if name != "MirrorPort test" {
		return fmt.Errorf("unexpected receiver name %q", name)
	}
	if len(arg("-pin")) != 4 || arg("-p") != port {
		return fmt.Errorf("invalid receiver arguments")
	}
	if arg("-vsync") != "no" || !strings.Contains(arg("-artp"), "leaky=downstream") {
		return fmt.Errorf("missing low-latency AirPlay settings")
	}
	if os.Getenv("MIRRORPORT_TEST_RECEIVER") == "window" {
		if arg("-vs") != "d3d11videosink" || arg("-vrtp") != "" {
			return fmt.Errorf("invalid Windows window arguments")
		}
		fmt.Println("Initialized server socket(s)")
		fmt.Println("Windows window fixture started")
		for {
			time.Sleep(time.Second)
		}
	}
	if arg("-vs") != "" {
		return fmt.Errorf("unexpected native video sink")
	}
	_, suffix, found := strings.Cut(arg("-vrtp"), "tcpclientsink host=127.0.0.1 port=")
	videoPort, _, _ := strings.Cut(suffix, " ")
	if !found || videoPort == "" {
		return fmt.Errorf("missing video port")
	}
	conn, err := net.DialTimeout("tcp4", "127.0.0.1:"+videoPort, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	data, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("empty fixture video")
	}
	for {
		for offset := 0; offset < len(data); offset += 1024 {
			chunk := data[offset:min(offset+1024, len(data))]
			if _, err := conn.Write(chunk); err != nil {
				return err
			}
			time.Sleep(9 * time.Millisecond)
		}
	}
}

func testReceiver(t *testing.T, display string) *receiver {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIRRORPORT_UXPLAY", exe)
	t.Setenv("MIRRORPORT_TEST_RECEIVER", display)
	r := &receiver{}
	t.Cleanup(r.shutdown)
	return r
}

func TestIndependentPreviewsFromLiveH264(t *testing.T) {
	ffmpeg := (toolPaths{}).ffmpeg()
	if ffmpeg == "" {
		t.Skip("FFmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for key, size := range map[string]string{
		"MIRRORPORT_TEST_H264":        "192x320",
		"MIRRORPORT_TEST_H264_SECOND": "320x192",
	} {
		input := filepath.Join(t.TempDir(), "fixture.h264")
		encoder := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size="+size+":rate=30", "-t", "2", "-c:v", "libx264",
			"-tune", "zerolatency", "-preset", "ultrafast", "-threads", "2", "-f", "h264", input)
		if out, err := encoder.CombinedOutput(); err != nil {
			t.Fatalf("create fixture: %v\n%s", err, out)
		}
		t.Setenv(key, input)
	}
	r := testReceiver(t, "browser")
	second := &receiver{port: 35003}
	t.Cleanup(second.shutdown)
	startBody := []byte(`{"display":"browser","name":"MirrorPort test"}`)
	app := &application{key: "fixture-key", receivers: [slotCount]*receiver{r, second}}
	server := httptest.NewUnstartedServer(app.routes())
	app.host = server.Listener.Addr().String()
	app.origin = "http://" + app.host
	server.Start()
	defer server.Close()
	started := fixturePost(t, server.URL, "/api/airplay/start", app.key, startBody, http.StatusOK)
	var startedStatus receiverStatus
	if err := json.Unmarshal(started, &startedStatus); err != nil || startedStatus.ID == "" || startedStatus.Display != "browser" {
		t.Fatalf("receiver start response %s, error %v", started, err)
	}
	fixturePost(t, server.URL, "/api/airplay/2/start", app.key, startBody, http.StatusConflict)
	secondStarted := fixturePost(t, server.URL, "/api/airplay/2/start", app.key,
		[]byte(`{"display":"browser","name":"MirrorPort second","fps":30,"muted":true}`), http.StatusOK)
	var secondStatus receiverStatus
	if err := json.Unmarshal(secondStarted, &secondStatus); err != nil || secondStatus.ID == "" || secondStatus.ID == startedStatus.ID {
		t.Fatalf("independent receiver start response %s, error %v", secondStarted, err)
	}
	streamCtx, stopStream := context.WithTimeout(ctx, 5*time.Second)
	defer stopStream()
	request, _ := http.NewRequestWithContext(streamCtx, "GET", server.URL+"/api/airplay/video?id="+startedStatus.ID, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%v: %+v", err, r.status())
	}
	defer response.Body.Close()
	contentType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "multipart/x-mixed-replace" {
		t.Fatalf("invalid preview response: %q", contentType)
	}
	reader := multipart.NewReader(response.Body, params["boundary"])
	var previous []byte
	for i := 0; i < 3; i++ {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		image, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil || image.Width != 192 || image.Height != 320 {
			t.Fatalf("invalid JPEG: %#v, %v", image, err)
		}
		if bytes.Equal(data, previous) {
			t.Fatal("expected advancing video frames")
		}
		previous = data
	}
	if status := r.status(); status.State != "playing" {
		t.Fatalf("preview state = %q", status.State)
	}
	waitReceiver(t, second, func(status receiverStatus) bool { return status.State == "playing" })
	fixturePost(t, server.URL, "/api/airplay/2/audio", app.key,
		[]byte(fmt.Sprintf(`{"id":%q,"volume":41,"muted":false}`, secondStatus.ID)), http.StatusNoContent)
	if audio := second.status().Audio; audio == nil || audio.Volume != 41 || audio.Muted || second.current.gain.Load() != 41 {
		t.Fatalf("unexpected live audio settings: %+v", audio)
	}
	fixturePost(t, server.URL, "/api/airplay/2/audio", app.key,
		[]byte(fmt.Sprintf(`{"id":%q,"volume":80,"muted":true}`, startedStatus.ID)), http.StatusConflict)
	state := httptest.NewRecorder()
	app.routes().ServeHTTP(state, httptest.NewRequest("GET", server.URL+"/obs/2/state", nil))
	var obs map[string]string
	if err := json.Unmarshal(state.Body.Bytes(), &obs); err != nil || state.Code != http.StatusOK ||
		len(obs) != 3 || obs["state"] != "playing" || obs["display"] != "browser" || obs["id"] != secondStatus.ID {
		t.Fatalf("unexpected OBS state: %d %s, %v", state.Code, state.Body, err)
	}
	snapshot := httptest.NewRecorder()
	r.snapshot(snapshot, httptest.NewRequest("GET", "/api/airplay/snapshot", nil))
	if snapshot.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d", snapshot.Code)
	}
	if _, err := jpeg.DecodeConfig(snapshot.Body); err != nil {
		t.Fatal(err)
	}
	fixturePost(t, server.URL, "/api/airplay/stop", app.key, []byte("{}"), http.StatusNoContent)
	waitReceiver(t, r, func(status receiverStatus) bool { return status.State == "idle" })
	if status := r.status(); status.ID != "" {
		t.Fatalf("stopped receiver still exposes the session ID: %+v", status)
	}
	snapshot = httptest.NewRecorder()
	app.routes().ServeHTTP(snapshot, httptest.NewRequest("GET", server.URL+"/api/airplay/2/snapshot", nil))
	image, err := jpeg.DecodeConfig(snapshot.Body)
	if err != nil || snapshot.Code != http.StatusOK || image.Width != 320 || image.Height != 192 {
		t.Fatalf("second preview after stopping the first: %d %#v, %v", snapshot.Code, image, err)
	}
	fixturePost(t, server.URL, "/api/airplay/2/stop", app.key, []byte("{}"), http.StatusNoContent)
	waitReceiver(t, second, func(status receiverStatus) bool { return status.State == "idle" })
	// Use a different display backend after stopping the browser receiver.
	t.Setenv("MIRRORPORT_TEST_RECEIVER", "window")
	startBody = []byte(`{"display":"window","name":"MirrorPort test","fps":30}`)
	restarted := fixturePost(t, server.URL, "/api/airplay/start", app.key, startBody, http.StatusOK)
	if !bytes.Contains(restarted, []byte(`"display":"window"`)) {
		t.Fatalf("unexpected restart response: %s", restarted)
	}
	waitReceiver(t, r, func(status receiverStatus) bool {
		return status.State == "waiting" && strings.Contains(status.Log, "Windows window fixture started")
	})
	fixturePost(t, server.URL, "/api/airplay/stop", app.key, []byte("{}"), http.StatusNoContent)
	waitReceiver(t, r, func(status receiverStatus) bool { return status.State == "idle" })
}

func fixturePost(t *testing.T, origin, path, key string, body []byte, want int) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", origin+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("X-MirrorPort-Key", key)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != want {
		t.Fatalf("%s = %d, want %d (%s); %v", path, res.StatusCode, want, data, err)
	}
	return data
}

func waitReceiver(t *testing.T, r *receiver, accept func(receiverStatus) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := r.status()
		if accept(status) {
			return
		}
		if status.State == "error" || time.Now().After(deadline) {
			t.Fatalf("receiver did not reach expected state: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLocalAccess(t *testing.T) {
	host := "127.0.0.1:12345"
	origin := "http://" + host
	app := application{host: host, origin: origin, key: "test-key", receivers: [slotCount]*receiver{&receiver{}}}
	handler := app.routes()
	for _, check := range []struct {
		method, path, origin, site, key string
		code                            int
	}{
		{"GET", "/", "", "", "", 200},
		{"GET", "/api/status", "", "same-origin", "", 200},
		{"GET", "/api/status", "https://foreign.invalid", "cross-site", "", 403},
		{"GET", "/api/airplay/snapshot", "", "same-site", "", 403},
		{"GET", "/api/airplay/snapshot", "", "same-origin", "", 404},
		{"GET", "/obs/1", "", "none", "", 200},
		{"GET", "/obs/1/state", "", "same-origin", "", 200},
		{"GET", "/obs/1/state", "", "cross-site", "", 403},
		{"GET", "/obs/5", "", "none", "", 404},
		{"POST", "/api/airplay/stop", origin, "same-origin", "", 403},
		{"POST", "/api/airplay/stop", origin, "same-origin", "test-key", 204},
		{"POST", "/api/airplay/start", origin, "same-origin", "test-key", 400},
	} {
		request := httptest.NewRequest(check.method, origin+check.path, nil)
		request.Header.Set("Origin", check.origin)
		request.Header.Set("Sec-Fetch-Site", check.site)
		request.Header.Set("X-MirrorPort-Key", check.key)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != check.code {
			t.Errorf("%s %s = %d, want %d", check.method, check.path, recorder.Code, check.code)
		}
	}
}

func TestPCMVolumeAndMute(t *testing.T) {
	audio := []byte{0x00, 0x80, 0x00, 0x40}
	scalePCM(audio, 50)
	if left, right := int16(binary.LittleEndian.Uint16(audio)), int16(binary.LittleEndian.Uint16(audio[2:])); left != -16384 || right != 8192 {
		t.Fatalf("half gain: %d, %d", left, right)
	}
	scalePCM(audio, 0)
	if !bytes.Equal(audio, make([]byte, len(audio))) {
		t.Fatalf("mute: %v", audio)
	}
}

func TestPCMQueueDropsOldestDuringBurst(t *testing.T) {
	pcm := make(chan [audioBlockBytes]byte, 4)
	data := make([]byte, 10*audioBlockBytes)
	for block := range 10 {
		data[block*audioBlockBytes] = byte(block)
	}
	queuePCM(context.Background(), bytes.NewReader(data), pcm)
	var seen []byte
	for block := range pcm {
		seen = append(seen, block[0])
	}
	if !bytes.Equal(seen, []byte{6, 7, 8, 9}) {
		t.Fatalf("playback queue contains stale blocks: %v", seen)
	}
}

func TestUSBFrameABIAndColor(t *testing.T) {
	if unsafe.Sizeof(usbCaptureOptions{}) != 48 || unsafe.Sizeof(usbFrameInfo{}) != 32 ||
		unsafe.Sizeof(usbDeviceInfo{}) != 1184 || unsafe.Sizeof(usbCaptureStatus{}) != 464 {
		t.Fatal("USB capture ABI no longer matches the x64 backend")
	}
	pixels := make([]byte, 8*8*4)
	for offset := 0; offset < len(pixels); offset += 4 {
		pixels[offset+2], pixels[offset+3] = 255, 255
	}
	encoded, err := encodeUSBFrame(pixels, usbFrameInfo{width: 8, height: 8, stride: 32, format: 1}, 5)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := jpeg.Decode(bytes.NewReader(encoded))
	if err != nil || frame.Bounds().Size() != image.Pt(8, 8) {
		t.Fatalf("invalid USB JPEG: %v", err)
	}
	r, g, b, _ := frame.At(4, 4).RGBA()
	if r < 60000 || g > 3000 || b > 3000 {
		t.Fatalf("incorrect BGRA channel ordering: %d, %d, %d", r, g, b)
	}
}

func TestCaptureListsStableDeviceNames(t *testing.T) {
	devices := parseCaptureDevices(`[dshow @ 0] "USB Capture" (video)
[dshow @ 0] Alternative name "@device_pnp_video#1"
[dshow @ 0] "USB Capture" (video)
[dshow @ 0] Alternative name "@device_pnp_video#2"
[dshow @ 0] "Digital Audio (USB Capture)" (audio)
[dshow @ 0] Alternative name "@device_cm_audio"
[dshow @ 0] "Broken Device" (video)
[dshow @ 0] Alternative name "@device_other:audio=other"
Error opening input file dummy.`)
	if len(devices.Video) != 2 || len(devices.Audio) != 1 || devices.Video[0].ID == devices.Video[1].ID {
		t.Fatalf("unexpected devices: %+v", devices)
	}
	options := receiverOptions{VideoDevice: devices.Video[0].ID, AudioDevice: devices.Audio[0].ID, FPS: 60, JPEGQuality: 5}
	args := strings.Join(captureArgs(options, "127.0.0.1:1234", "requested"), " ")
	if !strings.Contains(args, "-framerate 60") ||
		!strings.Contains(args, "video=@device_pnp_video#1:audio=@device_cm_audio") ||
		!strings.Contains(args, "-map 0:a:0") {
		t.Fatal(args)
	}
}

func TestMosaicSnapshotUsesSelectedRegion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &receiverSession{
		ctx: ctx, changed: make(chan struct{}), jpegQuality: 3,
		status: receiverStatus{ID: "mask-fixture", Display: "browser"},
	}
	r := &receiver{current: s}
	s.startMosaicWorker(r)
	input := image.NewGray(image.Rect(0, 0, 128, 128))
	for y := range 128 {
		for x := range 128 {
			if (x/3+y/3)%2 == 0 {
				input.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, input, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	r.setFrame(s, jpegData.Bytes())
	region := &mosaicRegion{Left: 2500, Top: 2500, Right: 7500, Bottom: 7500}
	if err := r.updateMosaic("mask-fixture", region); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	r.snapshot(response, nil)
	if response.Code != http.StatusNotFound {
		t.Fatal("snapshot returned the last unprocessed frame")
	}
	changed := s.changed
	r.setFrame(s, jpegData.Bytes())
	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("processed frame did not arrive")
	}
	response = httptest.NewRecorder()
	r.snapshot(response, nil)
	result, err := jpeg.Decode(response.Body)
	if err != nil || response.Code != http.StatusOK {
		t.Fatalf("snapshot: %d %v", response.Code, err)
	}
	luma := func(x, y int) uint8 { return color.GrayModel.Convert(result.At(x, y)).(color.Gray).Y }
	if luma(35, 35)/64 != luma(38, 35)/64 || luma(8, 8)/64 == luma(11, 8)/64 {
		t.Fatal("pixelation did not stay inside the selected region")
	}
}

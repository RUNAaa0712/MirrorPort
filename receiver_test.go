package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	startBody := []byte(`{"display":"browser","name":"MirrorPort test","fps":30}`)
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

func TestBundledAudioWithoutLevel(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "uxplay.exe")
	if bundledAudioNeedsRTP(engine) {
		t.Fatal("a system installation should keep the default audio pipeline")
	}
	plugins := filepath.Join(dir, "gstreamer-1.0")
	if err := os.Mkdir(plugins, 0700); err != nil {
		t.Fatal(err)
	}
	if !bundledAudioNeedsRTP(engine) {
		t.Fatal("a portable installation without level should use the RTP audio pipeline")
	}
	if err := os.WriteFile(filepath.Join(plugins, "libgstlevel.dll"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if bundledAudioNeedsRTP(engine) {
		t.Fatal("a portable installation with level should keep the default audio pipeline")
	}
}

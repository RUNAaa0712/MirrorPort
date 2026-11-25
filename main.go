package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	slotCount        = 4
	firstAirPlayPort = 35000
	uiPort           = 42884
)

//go:embed web/*
var assets embed.FS

type application struct {
	origin       string
	host         string
	key          string
	quit         chan struct{}
	receivers    [slotCount]*receiver
	tools        toolPaths
	performance  *performanceMonitor
	startMu      sync.Mutex
	shuttingDown bool
}

func main() {
	port := flag.Int("port", uiPort, "local UI and OBS port (0 selects an available port)")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser")
	flag.Parse()

	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	tools := toolPaths{base: filepath.Dir(executable)}
	app := &application{
		origin:      "http://" + listener.Addr().String(),
		host:        listener.Addr().String(),
		key:         hex.EncodeToString(randomBytes(32)),
		quit:        make(chan struct{}, 1),
		tools:       tools,
		performance: startPerformanceMonitor(),
	}
	for i := range app.receivers {
		app.receivers[i] = &receiver{tools: tools, port: firstAirPlayPort + i*3}
	}
	server := &http.Server{
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	ended := make(chan error, 1)
	go func() { ended <- server.Serve(listener) }()
	fmt.Printf("MirrorPort   %s\nCtrl+C to quit.\n", app.origin)
	if !*noBrowser {
		if err := shellOpen(app.origin); err != nil {
			log.Print(err)
		}
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	select {
	case <-interrupt:
	case <-app.quit:
	case err := <-ended:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Print(err)
		}
	}
	_ = server.Close()
	close(app.performance.stop)
	if app.shutdown() {
		shutdownUSBCore()
	}
}

func (a *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.asset("index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /style.css", a.asset("style.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /app.js", a.asset("app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /obs.css", a.asset("obs.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /obs.js", a.asset("obs.js", "application/javascript; charset=utf-8"))
	obsPage := a.asset("obs.html", "text/html; charset=utf-8")
	mux.HandleFunc("GET /obs/{slot}", a.slotHandler(func(_ *receiver, w http.ResponseWriter, r *http.Request) {
		obsPage(w, r)
	}))
	mux.HandleFunc("GET /obs/{slot}/state", a.slotHandler(func(s *receiver, w http.ResponseWriter, _ *http.Request) {
		status := s.status()
		writeJSON(w, http.StatusOK, map[string]any{"state": status.State, "display": status.Display, "id": status.ID})
	}))
	mux.HandleFunc("GET /api/status", a.status)
	mux.HandleFunc("GET /api/capture/devices", a.captureDevices)
	mux.HandleFunc("GET /api/usb/devices", a.usbDevices)
	start := func(s *receiver, w http.ResponseWriter, r *http.Request) { a.start(s, w, r) }
	stop := func(s *receiver, w http.ResponseWriter, _ *http.Request) {
		s.stop()
		w.WriteHeader(http.StatusNoContent)
	}
	for _, prefix := range []string{"/api/airplay", "/api/airplay/{slot}"} {
		mux.HandleFunc("POST "+prefix+"/start", a.slotHandler(start))
		mux.HandleFunc("POST "+prefix+"/stop", a.slotHandler(stop))
		mux.HandleFunc("GET "+prefix+"/video", a.slotHandler((*receiver).video))
		mux.HandleFunc("GET "+prefix+"/snapshot", a.slotHandler((*receiver).snapshot))
		mux.HandleFunc("POST "+prefix+"/audio", a.slotHandler((*receiver).audioSettings))
		mux.HandleFunc("POST "+prefix+"/mosaic", a.slotHandler((*receiver).mosaicSettings))
	}

	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		_ = http.NewResponseController(w).Flush()
		select {
		case a.quit <- struct{}{}:
		default:
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' blob:; media-src 'self' blob:; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		origin := r.Header.Get("Origin")
		if r.Host != a.host || (origin != "" && origin != a.origin) {
			http.Error(w, "Invalid origin", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasSuffix(r.URL.Path, "/state") {
			site := r.Header.Get("Sec-Fetch-Site")
			if site != "" && site != "same-origin" {
				http.Error(w, "Invalid origin", http.StatusForbidden)
				return
			}
			if r.Method != "GET" && (origin != a.origin ||
				subtle.ConstantTimeCompare([]byte(r.Header.Get("X-MirrorPort-Key")), []byte(a.key)) != 1) {
				http.Error(w, "Invalid request", http.StatusForbidden)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *application) asset(name, contentType string) http.HandlerFunc {
	data, err := assets.ReadFile("web/" + name)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}

func (a *application) slotHandler(handle func(*receiver, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.PathValue("slot")
		number := 1
		if raw != "" {
			var err error
			number, err = strconv.Atoi(raw)
			if err != nil || number < 1 || number > slotCount || raw != strconv.Itoa(number) {
				http.NotFound(w, r)
				return
			}
		}
		s := a.receivers[number-1]
		if s == nil {
			http.NotFound(w, r)
			return
		}
		handle(s, w, r)
	}
}

func (a *application) shutdown() bool {
	a.startMu.Lock()
	a.shuttingDown = true
	wait := make([]<-chan struct{}, 0, slotCount)
	timeoutDuration := 5 * time.Second
	for _, s := range a.receivers {
		if s != nil {
			if s.status().Source == "usb" {
				// A USB configuration switch can take up to 18 seconds.
				timeoutDuration = 25 * time.Second
			}
			if done := s.stop(); done != nil {
				wait = append(wait, done)
			}
		}
	}
	a.startMu.Unlock()
	timeout := time.NewTimer(timeoutDuration)
	defer timeout.Stop()
	for _, done := range wait {
		select {
		case <-done:
		case <-timeout.C:
			return false
		}
	}
	return true
}

func (a *application) status(w http.ResponseWriter, _ *http.Request) {
	var slots [slotCount]receiverStatus
	for i, s := range a.receivers {
		slots[i].State = "idle"
		if s != nil {
			slots[i] = s.status()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key": a.key, "airplay": slots[0],
		"slots":            slots,
		"airplayInstalled": a.tools.uxplay() != "",
		"ffmpegInstalled":  a.tools.ffmpeg() != "",
		"usbInstalled":     a.tools.usbReady(),
		"performance":      a.performance.snapshot(),
	})
}

func (a *application) start(s *receiver, w http.ResponseWriter, r *http.Request) {
	var options receiverOptions
	if !decodeRequest(w, r, &options) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "受信設定を読み取れません。"})
		return
	}
	a.startMu.Lock()
	defer a.startMu.Unlock()
	if a.shuttingDown {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "MirrorPortを停止しています。"})
		return
	}
	for _, other := range a.receivers {
		if other == nil || other == s {
			continue
		}
		status := other.status()
		if status.ID == "" {
			continue
		}
		if (options.Source == "capture" || options.Source == "usb") &&
			status.Source == options.Source && strings.EqualFold(status.VideoDevice, options.VideoDevice) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "別の受信機がこの映像デバイスを使用しています。"})
			return
		}
		if (options.Source == "" || options.Source == "airplay") &&
			status.Source == "airplay" && strings.EqualFold(status.Name, strings.TrimSpace(options.Name)) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "別の受信機が同じ名前を使用しています。異なる名前を指定してください。"})
			return
		}
	}
	if err := s.start(options); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.status())
}

func decodeRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value) == nil && decoder.Decode(new(any)) == io.EOF
}

func (a *application) captureDevices(w http.ResponseWriter, r *http.Request) {
	ffmpeg := a.tools.ffmpeg()
	if ffmpeg == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "有線入力にはFFmpegが必要です。"})
		return
	}
	devices, err := listCaptureDevices(r.Context(), ffmpeg)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, devices)
}

func (a *application) usbDevices(w http.ResponseWriter, _ *http.Request) {
	core, err := loadUSBCore(a.tools.usb())
	if err == nil {
		var devices []usbDevice
		devices, err = core.listDevices()
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"video": devices})
			return
		}
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
}

func (s *receiver) audioSettings(w http.ResponseWriter, r *http.Request) {
	var options struct {
		ID     string `json:"id"`
		Volume *int   `json:"volume"`
		Muted  bool   `json:"muted"`
	}
	if !decodeRequest(w, r, &options) || options.Volume == nil || *options.Volume < 0 || *options.Volume > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "音量は0〜100%で指定してください。"})
		return
	}
	if err := s.updateAudio(options.ID, audioSettings{Volume: *options.Volume, Muted: options.Muted}); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *receiver) mosaicSettings(w http.ResponseWriter, r *http.Request) {
	var options struct {
		ID     string        `json:"id"`
		Region *mosaicRegion `json:"region"`
	}
	if !decodeRequest(w, r, &options) || (options.Region != nil && !options.Region.valid()) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "映像上でモザイクの範囲を選んでください。"})
		return
	}
	if err := s.updateMosaic(options.ID, options.Region); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func randomBytes(size int) []byte {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return value
}

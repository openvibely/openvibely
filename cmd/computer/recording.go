//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"image/jpeg"
	"net/http"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/computerrecording"
)

type desktopRecorder struct {
	recording computerrecording.Recorder
	captureMu sync.Mutex
	// Input processing and lifecycle transitions share this boundary. A save
	// must include frames already being forwarded or classified by the viewer.
	inputMu sync.Mutex
}

func (d *desktopRecorder) capture() {
	d.captureMu.Lock()
	defer d.captureMu.Unlock()
	generation, ms, active := d.recording.Ticket()
	if !active {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// ImageMagick already ships with the desktop; don't add a capture dependency.
	raw, err := command(ctx, "import", "-window", "root", "-resize", "1280x1280>", "-quality", "72", "jpeg:-")
	if err != nil {
		d.recording.CaptureFailed(generation, fmt.Errorf("screen capture failed: %w", err))
		return
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		d.recording.CaptureFailed(generation, err)
		return
	}
	sig := make([]byte, 32*18)
	bounds := img.Bounds()
	for y := 0; y < 18; y++ {
		for x := 0; x < 32; x++ {
			sig[y*32+x] = color.GrayModel.Convert(img.At(bounds.Min.X+x*bounds.Dx()/32, bounds.Min.Y+y*bounds.Dy()/18)).(color.Gray).Y
		}
	}
	d.recording.Capture(generation, ms, raw, sig)
}
func recordingRoutes(mux *http.ServeMux) *desktopRecorder {
	d := &desktopRecorder{}
	go func() {
		for {
			began := time.Now()
			state, interval := d.recording.CaptureState()
			if state == "recording" {
				d.capture()
			}
			if interval < 250*time.Millisecond {
				interval = 250 * time.Millisecond
			}
			if remaining := interval - time.Since(began); remaining > 0 {
				time.Sleep(remaining)
			}
		}
	}()
	mux.HandleFunc("POST /recording", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action, ID string
			Limit      int
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
			http.Error(w, "Invalid recording command", 400)
			return
		}
		switch in.Action {
		case "start", "pause", "finish", "discard", "ack", "status":
		default:
			http.Error(w, "Unknown recording command", 400)
			return
		}
		out, err := func() (any, error) {
			// Wait for in-flight input, then take the response snapshot. Release
			// before writing HTTP output so slow transfers cannot block controls.
			// Lock ordering is inputMu, captureMu, then the recorder's mutex.
			if in.Action != "status" {
				d.inputMu.Lock()
				defer d.inputMu.Unlock()
			}
			var out any
			var err error
			switch in.Action {
			case "start":
				err = d.recording.Start(in.ID)
				if err == nil {
					d.capture()
				}
				out = d.recording.Status()
			case "pause":
				d.capture()
				d.recording.Pause()
				d.captureMu.Lock()
				d.captureMu.Unlock()
				out = d.recording.Status()
			case "finish":
				d.capture()
				out, err = d.recording.Finish(in.ID, in.Limit)
			case "discard", "ack":
				err = d.recording.Clear(in.ID)
				out = d.recording.Status()
			case "status":
				out = d.recording.Status()
			}
			return out, err
		}()
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
	return d
}

//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"time"
)

// inputMu serializes requests. Keep the browser connection so JavaScript dialogs
// survive between tool calls. Cancelled operations discard the worker, never
// replay an action that may already have reached the page.
type browserWorker struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Scanner
}

var currentBrowserWorker *browserWorker
var newBrowserCommand = desktopCommand
var awaitBrowserReady = waitForBrowser

// Only browser tools wait for CDP. Desktop viewing and input do not need it.
func waitForBrowser(ctx context.Context, endpoint string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := http.Client{Timeout: time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		timer := time.NewTimer(150 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func stopBrowserWorker() {
	if currentBrowserWorker != nil {
		currentBrowserWorker.input.Close()
		_ = currentBrowserWorker.cmd.Process.Kill()
		_ = currentBrowserWorker.cmd.Wait()
		currentBrowserWorker = nil
	}
}
func browserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /browser", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil || !json.Valid(body) {
			http.Error(w, "Invalid browser request", 400)
			return
		}
		var compact json.RawMessage = body
		body, err = json.Marshal(compact)
		if err != nil {
			http.Error(w, "Invalid browser request", 400)
			return
		}
		if err := awaitBrowserReady(r.Context(), "http://127.0.0.1:"+computerPort(1)+"/json/version"); err != nil {
			http.Error(w, "Browser unavailable", http.StatusBadGateway)
			return
		}
		inputMu.Lock()
		defer inputMu.Unlock()
		if currentBrowserWorker == nil {
			cmd := newBrowserCommand(context.Background(), "node", "/usr/local/lib/openvibely-browser.cjs", "--serve")
			cmd.Env = append(cmd.Environ(), "OPENVIBELY_CDP_PORT="+computerPort(1))
			input, e := cmd.StdinPipe()
			if e != nil {
				http.Error(w, "Browser unavailable", 502)
				return
			}
			output, e := cmd.StdoutPipe()
			if e != nil {
				input.Close()
				http.Error(w, "Browser unavailable", 502)
				return
			}
			if e = cmd.Start(); e != nil {
				input.Close()
				output.Close()
				http.Error(w, "Browser unavailable", 502)
				return
			}
			scanner := bufio.NewScanner(output)
			scanner.Buffer(make([]byte, 4096), 12000001)
			currentBrowserWorker = &browserWorker{cmd: cmd, input: input, output: scanner}
		}
		worker := currentBrowserWorker
		type reply struct {
			body []byte
			err  error
		}
		done := make(chan reply, 1)
		go func() {
			if _, e := worker.input.Write(append(body, '\n')); e != nil {
				done <- reply{err: e}
				return
			}
			if !worker.output.Scan() {
				e := worker.output.Err()
				if e == nil {
					e = io.EOF
				}
				done <- reply{err: e}
				return
			}
			done <- reply{body: append([]byte(nil), worker.output.Bytes()...)}
		}()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		select {
		case response := <-done:
			if response.err != nil || !json.Valid(response.body) {
				stopBrowserWorker()
				http.Error(w, "Browser operation failed", 502)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(response.body)
		case <-ctx.Done():
			stopBrowserWorker()
			<-done
			http.Error(w, "Browser operation cancelled", 504)
		}
	})
}

package handler

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// Chrome's dump-dom mode can hang/crash during macOS teardown after emitting
// the complete document. Own the process lifetime once the dump is complete;
// callers still assert the page's actual pass/fail result in that document.
func runHandlerBrowserProcess(cmd *exec.Cmd) ([]byte, error) {
	dump := &browserDOMDump{complete: make(chan struct{})}
	var stderr bytes.Buffer
	cmd.Stdout = dump
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	if err := startHandlerBrowserProcess(cmd); err != nil {
		return nil, err
	}
	defer killHandlerBrowserProcess(cmd)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-dump.complete:
		killHandlerBrowserProcess(cmd)
		<-done
		return dump.Bytes(), nil
	case err := <-done:
		select {
		case <-dump.complete:
			return dump.Bytes(), nil
		default:
		}
		output := append(append([]byte(nil), dump.Bytes()...), stderr.Bytes()...)
		if err == nil {
			err = fmt.Errorf("Chrome exited without a complete DOM dump")
		}
		return output, err
	}
}

type browserDOMDump struct {
	buffer   bytes.Buffer
	complete chan struct{}
	once     sync.Once
}

func (d *browserDOMDump) Write(p []byte) (int, error) {
	n, err := d.buffer.Write(p)
	if bytes.HasSuffix(d.buffer.Bytes(), []byte("</html>\n")) && completeHTMLDocument(d.buffer.Bytes()) {
		d.once.Do(func() { close(d.complete) })
	}
	return n, err
}

func (d *browserDOMDump) Bytes() []byte { return d.buffer.Bytes() }

func completeHTMLDocument(data []byte) bool {
	tokens := html.NewTokenizer(bytes.NewReader(data))
	started := false
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken:
			name, _ := tokens.TagName()
			if string(name) == "html" {
				started = true
			}
		case html.EndTagToken:
			name, _ := tokens.TagName()
			if started && string(name) == "html" {
				return true
			}
		}
	}
}

func TestBrowserDOMDumpIgnoresClosingTagInScript(t *testing.T) {
	dump := &browserDOMDump{complete: make(chan struct{})}
	_, _ = dump.Write([]byte("<!DOCTYPE html><html><body><script>const template = `</html>\n"))
	select {
	case <-dump.complete:
		t.Fatal("script content mistaken for the end of the document")
	default:
	}
	_, _ = dump.Write([]byte("`;</script></body></html>\n"))
	select {
	case <-dump.complete:
	default:
		t.Fatal("real document end not detected")
	}
}

func TestBrowserDOMDumpCompletion(t *testing.T) {
	dump := &browserDOMDump{complete: make(chan struct{})}
	for _, part := range []string{"<!DOCTYPE html><html><body>partial", "</body></ht", "ml>"} {
		_, _ = dump.Write([]byte(part))
		select {
		case <-dump.complete:
			t.Fatal("incomplete output reported complete")
		default:
		}
	}
	_, _ = dump.Write([]byte("\n"))
	select {
	case <-dump.complete:
	default:
		t.Fatal("complete document was not detected")
	}
}

func TestBrowserDumpProcessLifecycle(t *testing.T) {
	if mode := os.Getenv("OPENVIBELY_BROWSER_DUMP_HELPER"); mode != "" {
		switch mode {
		case "pass", "fail":
			fmt.Printf("<!DOCTYPE html><html><body><div id=\"browser-result\" data-status=\"%s\"></div></body></html>\n", mode)
			// Simulate a browser stuck in teardown after emitting its DOM.
			time.Sleep(time.Minute)
		case "incomplete":
			fmt.Print("<!DOCTYPE html><html><body>truncated")
			os.Exit(2)
		}
		os.Exit(0)
	}
	for _, mode := range []string{"pass", "fail", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBrowserDumpProcessLifecycle$")
			cmd.Env = append(os.Environ(), "OPENVIBELY_BROWSER_DUMP_HELPER="+mode)
			out, err := runHandlerBrowserProcess(cmd)
			if mode == "incomplete" {
				if err == nil {
					t.Fatal("truncated DOM accepted")
				}
				return
			}
			if err != nil || ctx.Err() != nil {
				t.Fatalf("completed DOM did not stop browser: %v, %v", err, ctx.Err())
			}
			if !bytes.Contains(out, []byte(`data-status="`+mode+`"`)) {
				t.Fatalf("page result lost: %s", out)
			}
		})
	}
}

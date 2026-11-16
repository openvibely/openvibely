package handler

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/service"
)

// adaptiveRFB accepts only the client protocol used by our local, passwordless
// RFB 3.8 server. WebSocket boundaries are not RFB message boundaries. Unknown
// protocol extensions fail closed rather than allowing an input-policy bypass.
type adaptiveRFB struct {
	pending []byte
	stage   int
	keys    map[string][]byte
	pointer []byte
}

func (p *adaptiveRFB) consume(data []byte, enabled bool, write func([]byte) error) error {
	if len(p.pending)+len(data) > 1<<20 {
		return errors.New("RFB client message too large")
	}
	p.pending = append(p.pending, data...)
	for len(p.pending) > 0 {
		b := p.pending
		n, input := 0, false
		switch p.stage {
		case 0:
			n = 12
			if len(b) < n {
				return nil
			}
			if !bytes.Equal(b[:n], []byte("RFB 003.008\n")) {
				return errors.New("unsupported RFB version")
			}
		case 1:
			n = 1
			if b[0] != 1 {
				return errors.New("unsupported RFB authentication")
			}
		case 2:
			n = 1
			if b[0] != 1 {
				return errors.New("exclusive RFB sessions are not permitted")
			}
		default:
			switch b[0] {
			case 0:
				n = 20 // SetPixelFormat
			case 2: // SetEncodings
				if len(b) < 4 {
					return nil
				}
				n = 4 + 4*int(binary.BigEndian.Uint16(b[2:4]))
			case 3, 150:
				n = 10 // FramebufferUpdateRequest / EnableContinuousUpdates
			case 4:
				n = 8
				input = true
			case 5:
				if len(b) < 2 {
					return nil
				}
				n = 6
				if b[1]&128 != 0 {
					n = 7
				}
				input = true
			case 6: // ClientCutText, including signed extended-clipboard length
				if len(b) < 8 {
					return nil
				}
				size := int64(int32(binary.BigEndian.Uint32(b[4:8])))
				if size < 0 {
					size = -size
				}
				if size > (1<<20)-8 {
					return errors.New("clipboard too large")
				}
				n = 8 + int(size)
				input = true
			case 248: // Fence is display synchronization, not user input.
				if len(b) < 9 {
					return nil
				}
				if b[8] > 64 {
					return errors.New("invalid fence")
				}
				n = 9 + int(b[8])
			case 251:
				if len(b) < 8 {
					return nil
				}
				n = 8 + 16*int(b[6])
				input = true
			case 255:
				if len(b) < 2 {
					return nil
				}
				if b[1] != 0 {
					return errors.New("unsupported QEMU message")
				}
				n = 12
				input = true
			default:
				return fmt.Errorf("unsupported RFB client message %d", b[0])
			}
		}
		if len(b) < n {
			return nil
		}
		message := b[:n]
		if !input || enabled {
			if input {
				if p.keys == nil {
					p.keys = map[string][]byte{}
				}
				switch b[0] {
				case 4, 255:
					key := string(b[0:1]) + string(b[4:n])
					down := b[1] != 0
					if b[0] == 255 {
						down = binary.BigEndian.Uint16(b[2:4]) != 0
					}
					if down {
						if len(p.keys) >= 256 {
							return errors.New("too many held keys")
						}
						release := bytes.Clone(message)
						release[1] = 0
						if b[0] == 255 {
							release[2] = 0
							release[3] = 0
						}
						p.keys[key] = release
					} else {
						delete(p.keys, key)
					}
				case 5:
					p.pointer = bytes.Clone(message[:6])
					p.pointer[1] = 0
				}
			}
			if err := write(message); err != nil {
				return err
			}
		}
		if p.stage < 3 {
			p.stage++
		}
		p.pending = p.pending[n:]
	}
	p.pending = nil
	return nil
}

func (p *adaptiveRFB) release(write func([]byte) error) error {
	for _, key := range p.keys {
		if err := write(key); err != nil {
			return err
		}
	}
	if p.pointer != nil {
		if err := write(p.pointer); err != nil {
			return err
		}
	}
	p.keys = nil
	p.pointer = nil
	return nil
}

// Input-mode commands travel on the same authenticated socket, in order with
// input. A mode switch cannot turn an earlier partial input message into an
// authorized one. Replies are consumed by the channel adapter, not noVNC.
func forwardAdaptiveComputerInput(ctx context.Context, dst, src *websocket.Conn, svc *service.ProjectComputerService, project, session, token string, handback func()) error {
	var parser adaptiveRFB
	enabled := false
	for {
		kind, data, err := src.Read(ctx)
		if err != nil {
			return err
		}
		write := func(b []byte) error { return dst.Write(ctx, websocket.MessageBinary, b) }
		if kind == websocket.MessageBinary {
			err = parser.consume(data, enabled, func(b []byte) error {
				if enabled {
					return svc.ForwardViewerInput(project, session, token, func() error { return write(b) })
				}
				return write(b)
			})
			if err != nil {
				return err
			}
			continue
		}
		if kind != websocket.MessageText || len(data) > 128 || len(parser.pending) != 0 {
			return errors.New("invalid viewer command")
		}
		var cmd struct {
			Input    *bool  `json:"input"`
			Sequence uint64 `json:"sequence"`
		}
		if json.Unmarshal(data, &cmd) != nil || cmd.Input == nil {
			return errors.New("invalid viewer command")
		}
		want := *cmd.Input
		if enabled && !want {
			if err := parser.release(func(b []byte) error {
				return svc.ForwardViewerInput(project, session, token, func() error { return write(b) })
			}); err != nil {
				return err
			}
		}
		err = svc.SetViewerInput(ctx, project, session, token, want)
		if err == nil {
			wasEnabled := enabled
			enabled = want
			if wasEnabled && !enabled {
				handback()
			}
		} else if !want {
			return err
		} // Never acknowledge a failed disable.
		reply, _ := json.Marshal(struct {
			Input    bool   `json:"input"`
			Sequence uint64 `json:"sequence"`
		}{enabled, cmd.Sequence})
		if err := src.Write(ctx, websocket.MessageText, reply); err != nil {
			return err
		}
	}
}

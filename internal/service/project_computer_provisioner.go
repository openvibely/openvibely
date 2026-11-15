package service

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	computerassets "github.com/openvibely/openvibely"
)

// ComputerConnection is private backend state, never an API response or tool result.
type ComputerConnection struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}
type ComputerProvisioner interface {
	Provider() string
	Resolve(context.Context, string) (ComputerConnection, error)
	Start(context.Context, string) (ComputerConnection, error)
	Stop(context.Context, string) error
}

type LocalComputerProvisioner struct {
	run func(context.Context, io.Reader, ...string) ([]byte, error)
}

func (*LocalComputerProvisioner) Provider() string { return "local" }
func computerContainerName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "openvibely-computer-" + hex.EncodeToString(sum[:12])
}
func (p *LocalComputerProvisioner) docker(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	if p.run != nil {
		return p.run(ctx, stdin, args...)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = stdin
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("Docker operation failed; check Docker is running and the computer image is available: %w", err)
	}
	return out, nil
}

// Current reports whether a running container matches this app's selected image.
// Keep this separate from Resolve so cleanup can still reach older containers.
func (p *LocalComputerProvisioner) Current(ctx context.Context, id string) bool {
	raw, err := p.docker(ctx, nil, "inspect", computerContainerName(id))
	if err != nil {
		return false
	}
	var rows []struct {
		Config struct {
			Image  string
			Labels map[string]string
		}
	}
	if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 {
		return false
	}
	image := os.Getenv("OPENVIBELY_COMPUTER_IMAGE")
	if image == "" {
		image = computerImageTag()
	}
	return rows[0].Config.Image == image && rows[0].Config.Labels["openvibely.project"] == id && rows[0].Config.Labels["openvibely.computer-isolation"] == "1"
}

func (p *LocalComputerProvisioner) Resolve(ctx context.Context, id string) (ComputerConnection, error) {
	return p.resolve(ctx, id, false)
}

// ResolveCurrent validates image and connection from a single Docker inspection.
// Resolve remains usable for cleaning up containers running an older image.
func (p *LocalComputerProvisioner) ResolveCurrent(ctx context.Context, id string) (ComputerConnection, error) {
	return p.resolve(ctx, id, true)
}
func (p *LocalComputerProvisioner) resolve(ctx context.Context, id string, current bool) (ComputerConnection, error) {
	raw, err := p.docker(ctx, nil, "inspect", computerContainerName(id))
	if err != nil {
		return ComputerConnection{}, err
	}
	var rows []struct {
		Config struct {
			Image  string
			Env    []string
			Labels map[string]string
		}
		State           struct{ Running bool }
		NetworkSettings struct {
			Ports map[string][]struct{ HostIP, HostPort string }
		}
	}
	if err = json.Unmarshal(raw, &rows); err != nil {
		return ComputerConnection{}, err
	}
	if len(rows) != 1 || rows[0].Config.Labels["openvibely.project"] != id {
		return ComputerConnection{}, errors.New("computer ownership mismatch")
	}
	if current {
		image := os.Getenv("OPENVIBELY_COMPUTER_IMAGE")
		if image == "" {
			image = computerImageTag()
		}
		if rows[0].Config.Image != image || rows[0].Config.Labels["openvibely.computer-isolation"] != "1" {
			return ComputerConnection{}, errors.New("project computer image is outdated")
		}
	}
	if !rows[0].State.Running {
		return ComputerConnection{}, errors.New("project computer is stopped")
	}
	ports := rows[0].NetworkSettings.Ports["8090/tcp"]
	if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" {
		return ComputerConnection{}, errors.New("project computer must publish only on loopback")
	}
	c := ComputerConnection{Endpoint: "http://127.0.0.1:" + ports[0].HostPort}
	for _, v := range rows[0].Config.Env {
		if strings.HasPrefix(v, "OPENVIBELY_COMPUTER_TOKEN=") {
			c.Token = strings.TrimPrefix(v, "OPENVIBELY_COMPUTER_TOKEN=")
		}
	}
	if c.Token == "" {
		return ComputerConnection{}, errors.New("project computer requires authentication; recreate its container while retaining its volume")
	}
	return c, nil
}
func computerImageTag() string {
	h := sha256.New()
	_ = fs.WalkDir(computerassets.Source, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !strings.HasSuffix(path, "_test.go") {
			data, err := computerassets.Source.ReadFile(path)
			if err != nil {
				return err
			}
			h.Write([]byte(path))
			h.Write(data)
		}
		return nil
	})
	return "openvibely-computer:" + hex.EncodeToString(h.Sum(nil))[:16]
}
func (p *LocalComputerProvisioner) Start(ctx context.Context, id string) (ComputerConnection, error) {
	return p.StartWithTimeZone(ctx, id, "UTC")
}
func (p *LocalComputerProvisioner) StartWithTimeZone(ctx context.Context, id, zone string) (ComputerConnection, error) {
	if err := validateComputerTimeZone(zone); err != nil {
		return ComputerConnection{}, err
	}
	name := computerContainerName(id)
	image := os.Getenv("OPENVIBELY_COMPUTER_IMAGE")
	if image == "" {
		image = computerImageTag()
	}
	replace := false
	if raw, err := p.docker(ctx, nil, "inspect", name); err == nil {
		var rows []struct {
			Config struct {
				Labels map[string]string
				Image  string
				Env    []string
			}
		}
		if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || rows[0].Config.Labels["openvibely.project"] != id {
			return ComputerConnection{}, errors.New("computer ownership mismatch")
		}
		existingZone := "UTC"
		for _, value := range rows[0].Config.Env {
			if strings.HasPrefix(value, "TZ=") {
				existingZone = strings.TrimPrefix(value, "TZ=")
			}
		}
		if rows[0].Config.Image == image && existingZone == zone && rows[0].Config.Labels["openvibely.computer-isolation"] == "1" {
			if _, err = p.docker(ctx, nil, "start", name); err != nil {
				return ComputerConnection{}, err
			}
			return p.Resolve(ctx, id)
		}
		replace = true
	}
	if os.Getenv("OPENVIBELY_COMPUTER_IMAGE") == "" {
		image = computerImageTag()
		if _, err := p.docker(ctx, nil, "image", "inspect", image); err != nil {
			var archive bytes.Buffer
			tw := tar.NewWriter(&archive)
			err = fs.WalkDir(computerassets.Source, ".", func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				data, err := computerassets.Source.ReadFile(path)
				if err != nil {
					return err
				}
				if err = tw.WriteHeader(&tar.Header{Name: path, Mode: 0644, Size: int64(len(data))}); err != nil {
					return err
				}
				_, err = tw.Write(data)
				return err
			})
			if err != nil {
				return ComputerConnection{}, err
			}
			if err = tw.Close(); err != nil {
				return ComputerConnection{}, err
			}
			if _, err = p.docker(ctx, &archive, "build", "-t", image, "-f", "packaging/computer/Dockerfile", "-"); err != nil {
				return ComputerConnection{}, err
			}
		}
	}
	if replace {
		if err := p.Stop(ctx, id); err != nil {
			return ComputerConnection{}, err
		}
		if _, err := p.docker(ctx, nil, "rm", name); err != nil {
			return ComputerConnection{}, err
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return ComputerConnection{}, err
	}
	_, err := p.docker(ctx, nil, "run", "-d", "--name", name, "--label", "openvibely.project="+id, "--label", "openvibely.computer-isolation=1", "--publish", "127.0.0.1::8090", "--mount", "type=volume,src="+name+"-home,dst=/home/ubuntu", "--shm-size", "1g", "--memory", "4g", "--cpus", "2", "--pids-limit", "1024", "--cap-add", "SYS_ADMIN", "--cap-add", "NET_ADMIN", "--device", "/dev/net/tun", "--security-opt", "apparmor=unconfined", "--security-opt", "no-new-privileges:true", "--restart", "unless-stopped", "--env", "OPENVIBELY_COMPUTER_TOKEN="+hex.EncodeToString(secret), "--env", "TZ="+zone, image)
	if err != nil {
		return ComputerConnection{}, err
	}
	return p.Resolve(ctx, id)
}
func (p *LocalComputerProvisioner) Stop(ctx context.Context, id string) error {
	existing, err := p.docker(ctx, nil, "container", "ls", "--all", "--filter", "name=^/"+computerContainerName(id)+"$", "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(existing)) == "" {
		return nil
	}
	raw, err := p.docker(ctx, nil, "inspect", computerContainerName(id))
	if err != nil {
		return err
	}
	var rows []struct {
		Config struct{ Labels map[string]string }
		State  struct{ Running bool }
	}
	if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || rows[0].Config.Labels["openvibely.project"] != id {
		return errors.New("computer ownership mismatch")
	}
	if !rows[0].State.Running {
		return nil
	}
	_, err = p.docker(ctx, nil, "stop", "--time", "20", computerContainerName(id))
	return err
}

// HostedComputerProvisioner talks only to the operator-configured control plane.
// The control plane derives workspace ownership from its bearer credential.
// GET/POST /projects/{id}/computer resolves/starts; POST .../stop stops.
type HostedComputerProvisioner struct {
	URL, Token string
	Client     *http.Client
}

func (*HostedComputerProvisioner) Provider() string { return "hosted" }
func (p *HostedComputerProvisioner) request(ctx context.Context, id, method, suffix string) (ComputerConnection, error) {
	u, err := url.Parse(p.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || p.Token == "" {
		return ComputerConnection{}, errors.New("hosted project computer provisioning is not configured")
	}
	target := strings.TrimRight(p.URL, "/") + "/projects/" + url.PathEscape(id) + "/computer" + suffix
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return ComputerConnection{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, err := client.Do(req)
	if err != nil {
		return ComputerConnection{}, errors.New("hosted computer control plane unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ComputerConnection{}, fmt.Errorf("hosted computer request failed (%d)", res.StatusCode)
	}
	if suffix != "" {
		return ComputerConnection{}, nil
	}
	var conn ComputerConnection
	err = json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&conn)
	if err != nil {
		return conn, err
	}
	endpoint, e := url.Parse(conn.Endpoint)
	if e != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || conn.Token == "" {
		return ComputerConnection{}, errors.New("invalid hosted computer connection")
	}
	return conn, nil
}
func (p *HostedComputerProvisioner) Resolve(ctx context.Context, id string) (ComputerConnection, error) {
	return p.request(ctx, id, http.MethodGet, "")
}
func (p *HostedComputerProvisioner) Start(ctx context.Context, id string) (ComputerConnection, error) {
	return p.request(ctx, id, http.MethodPost, "")
}
func (p *HostedComputerProvisioner) Stop(ctx context.Context, id string) error {
	_, err := p.request(ctx, id, http.MethodPost, "/stop")
	return err
}
func defaultComputerProvisioner() ComputerProvisioner {
	if os.Getenv("OPENVIBELY_HOSTED_INSTANCE_ID") != "" || os.Getenv("OPENVIBELY_COMPUTER_CONTROL_URL") != "" {
		return &HostedComputerProvisioner{URL: os.Getenv("OPENVIBELY_COMPUTER_CONTROL_URL"), Token: os.Getenv("OPENVIBELY_HOSTED_AGENT_TOKEN")}
	}
	return &LocalComputerProvisioner{}
}

// RemoveProject removes resources before project records are deleted. Each part
// is checked independently so retries can finish after a partial removal.
func (p *LocalComputerProvisioner) RemoveProject(ctx context.Context, id string) error {
	name := computerContainerName(id)
	volume := name + "-home"
	existing, err := p.docker(ctx, nil, "container", "ls", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(existing)) != "" {
		raw, err := p.docker(ctx, nil, "inspect", name)
		if err != nil {
			return err
		}
		var rows []struct {
			Config struct{ Labels map[string]string }
			Mounts []struct{ Name, Destination, Type string }
			State  struct{ Running bool }
		}
		if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || rows[0].Config.Labels["openvibely.project"] != id {
			return errors.New("computer ownership mismatch")
		}
		if rows[0].State.Running {
			return errors.New("stop computer before removing project resources")
		}
		owned := false
		for _, m := range rows[0].Mounts {
			if m.Destination == "/home/ubuntu" && m.Type == "volume" && m.Name == volume {
				owned = true
			}
		}
		if !owned {
			return errors.New("computer volume ownership mismatch")
		}
		if _, err := p.docker(ctx, nil, "rm", name); err != nil {
			return err
		}
	}
	// The durable project record and deterministic volume name allow retrying
	// even when the container was already removed. Never scan other volumes.
	volumes, err := p.docker(ctx, nil, "volume", "ls", "--filter", "name=^"+volume+"$", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(volumes)) == "" {
		return nil
	}
	// Docker rejects removal if any container (including stopped ones) uses it.
	_, err = p.docker(ctx, nil, "volume", "rm", volume)
	return err
}

// Check availability before recording provisioning intent. An unavailable daemon
// cannot have created resources for a computer that has never been started.
func (p *LocalComputerProvisioner) CheckAvailable(ctx context.Context) error {
	_, err := p.docker(ctx, nil, "info", "--format", "{{.ServerVersion}}")
	return err
}

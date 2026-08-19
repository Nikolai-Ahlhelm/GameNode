package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrEngineUnavailable = errors.New("container engine is unavailable")

// ContainerEngine is deliberately small and transport-free. Docker is the
// sole v0.3 implementation; the interface exists for lifecycle tests, not as
// a provider/plugin system.
type ContainerEngine interface {
	Available(context.Context) error
	Create(context.Context, ContainerOptions, StartOptions) (string, error)
	Start(context.Context, string) error
	Stop(context.Context, string, time.Duration) error
	Kill(context.Context, string) error
	Inspect(context.Context, string) (containerInspection, error)
	Stats(context.Context, string) (Metrics, error)
	Attach(context.Context, string) (io.ReadWriteCloser, error)
	ImageAvailable(context.Context, string) (bool, error)
	PullImage(context.Context, string) error
}
type containerRemoval interface {
	Remove(context.Context, string) error
}
type containerImageDigest interface {
	ImageDigest(context.Context, string) (string, error)
}
type containerInspection struct {
	Running  bool
	Known    bool
	ExitCode int
	Labels   map[string]string
}

type containerRuntime struct{ engine ContainerEngine }

func NewContainer(engine ContainerEngine) Runtime { return &containerRuntime{engine: engine} }

type containerInstaller struct{ engine ContainerEngine }

// NewContainerInstaller exposes only the controlled one-shot installer
// operation to provisioning. The underlying Engine API remains private to
// runtime and is never passed through HTTP or user input.
func NewContainerInstaller(engine ContainerEngine) ContainerInstaller {
	return &containerInstaller{engine: engine}
}

func (i *containerInstaller) Available(ctx context.Context) error { return i.engine.Available(ctx) }
func (i *containerInstaller) PullImage(ctx context.Context, image string) error {
	return i.engine.PullImage(ctx, image)
}
func (i *containerInstaller) ImageDigest(ctx context.Context, image string) (string, error) {
	resolver, ok := i.engine.(containerImageDigest)
	if !ok {
		return "", errors.New("container image digest is unavailable")
	}
	return resolver.ImageDigest(ctx, image)
}
func (i *containerInstaller) RunInstaller(ctx context.Context, spec ContainerInstallSpec, output io.Writer) (err error) {
	removable, ok := i.engine.(containerRemoval)
	if !ok {
		return errors.New("container installer cleanup is unavailable")
	}
	entrypoint := spec.Entrypoint
	if entrypoint == "" {
		entrypoint = "/bin/sh"
	}
	if entrypoint != "sh" && entrypoint != "bash" && entrypoint != "/bin/sh" && entrypoint != "/bin/bash" {
		return errors.New("container installer entrypoint is not allowed")
	}
	if len(spec.Script) > 64<<10 || strings.ContainsRune(spec.Script, 0) {
		return errors.New("container installer script is too large")
	}
	if spec.MemoryLimitBytes < 16<<20 || spec.CPULimitMillis < 10 || spec.PIDsLimit < 1 || spec.TmpfsSizeBytes < 1 {
		return errors.New("container installer resource limits are invalid")
	}
	command := []string{entrypoint, "-lc", spec.Script}
	options := ContainerOptions{Image: spec.Image, Command: command, MemoryLimitBytes: spec.MemoryLimitBytes, CPULimitMillis: spec.CPULimitMillis, ServerID: spec.ServerID, Generation: spec.Generation, OwnershipToken: spec.OwnershipToken, PIDsLimit: spec.PIDsLimit, TmpfsSizeBytes: spec.TmpfsSizeBytes}
	identity, exits, startErr := (&containerRuntime{engine: i.engine}).Start(ctx, StartOptions{RuntimeType: "container", WorkingDirectory: spec.WorkingDirectory, Environment: spec.Environment, Container: &options, IO: StartIO{Stdout: output, Stderr: output}})
	if startErr != nil {
		return startErr
	}
	id := containerID(identity)
	defer func() {
		if cleanupErr := removable.Remove(context.Background(), id); err == nil && cleanupErr != nil {
			err = errors.New("installer container cleanup failed")
		}
	}()
	select {
	case result, ok := <-exits:
		if !ok {
			return errors.New("installer container exit status unavailable")
		}
		if result.Err != nil {
			return errors.New("installer container failed")
		}
		if result.ExitCode != 0 {
			return errors.New("installer script exited with a non-zero status")
		}
		return nil
	case <-ctx.Done():
		_ = i.engine.Kill(context.Background(), id)
		return ctx.Err()
	}
}

// NewHybrid dispatches by explicit runtime_type. It keeps the native runtime
// intact and makes unsupported Docker installations an honest availability
// failure rather than a shell fallback.
func NewHybrid() Runtime {
	return NewHybridWithEngine(NewDockerEngine())
}

func NewHybridWithEngine(engine ContainerEngine) Runtime {
	return hybridRuntime{native: NewNative(), container: NewContainer(engine)}
}

type hybridRuntime struct{ native, container Runtime }

func (r hybridRuntime) choose(kind string, id Identity) Runtime {
	if kind == "container" || id.ContainerID != "" || strings.HasPrefix(id.StartKey, "container:") {
		return r.container
	}
	return r.native
}
func (r hybridRuntime) Start(c context.Context, o StartOptions) (Identity, <-chan ExitResult, error) {
	return r.choose(o.RuntimeType, Identity{}).Start(c, o)
}
func (r hybridRuntime) Stop(c context.Context, i Identity, t time.Duration) error {
	return r.choose("", i).Stop(c, i, t)
}
func (r hybridRuntime) Interrupt(c context.Context, i Identity) error {
	return r.choose("", i).Interrupt(c, i)
}
func (r hybridRuntime) Kill(c context.Context, i Identity) error { return r.choose("", i).Kill(c, i) }
func (r hybridRuntime) Status(c context.Context, i Identity) (Status, error) {
	return r.choose("", i).Status(c, i)
}
func (r hybridRuntime) Metrics(c context.Context, i Identity) (Metrics, error) {
	return r.choose("", i).Metrics(c, i)
}
func (r hybridRuntime) ImageAvailable(c context.Context, image string) (bool, error) {
	return r.container.(ImageManager).ImageAvailable(c, image)
}
func (r hybridRuntime) PullImage(c context.Context, image string) error {
	return r.container.(ImageManager).PullImage(c, image)
}

func (r *containerRuntime) Start(ctx context.Context, options StartOptions) (Identity, <-chan ExitResult, error) {
	if options.Container == nil {
		return Identity{}, nil, errors.New("container configuration is missing")
	}
	if err := r.engine.Available(ctx); err != nil {
		return Identity{}, nil, ErrEngineUnavailable
	}
	available, err := r.engine.ImageAvailable(ctx, options.Container.Image)
	if err != nil {
		return Identity{}, nil, ErrEngineUnavailable
	}
	if !available {
		return Identity{}, nil, errors.New("container image is missing")
	}
	id, err := r.engine.Create(ctx, *options.Container, options)
	if err != nil {
		return Identity{}, nil, err
	}
	if err = r.engine.Start(ctx, id); err != nil {
		if removable, ok := r.engine.(containerRemoval); ok {
			_ = removable.Remove(context.Background(), id)
		}
		return Identity{}, nil, err
	}
	identity := Identity{PID: 1, StartKey: "container:" + id + ":" + options.Container.ServerID + ":" + options.Container.Generation + ":" + options.Container.OwnershipToken, ContainerID: id}
	inspect, err := r.engine.Inspect(ctx, id)
	if err != nil || !owned(identity, inspect.Labels) {
		_ = r.engine.Kill(context.Background(), id)
		if removable, ok := r.engine.(containerRemoval); ok {
			_ = removable.Remove(context.Background(), id)
		}
		return Identity{}, nil, errors.New("container ownership is invalid")
	}
	attachment, err := r.engine.Attach(ctx, id)
	if err != nil {
		_ = r.engine.Kill(context.Background(), id)
		if removable, ok := r.engine.(containerRemoval); ok {
			_ = removable.Remove(context.Background(), id)
		}
		return Identity{}, nil, errors.New("container console is unavailable")
	}
	if options.IO.Stdin != nil {
		options.IO.Stdin(attachment)
	}
	go copyDockerConsole(attachment, options.IO)
	exits := make(chan ExitResult, 1)
	go func() {
		defer close(exits)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			inspect, e := r.engine.Inspect(context.Background(), id)
			if e != nil {
				exits <- ExitResult{ExitCode: 1, Err: e}
				return
			}
			if !owned(identity, inspect.Labels) {
				exits <- ExitResult{ExitCode: 1, Err: errors.New("container ownership is invalid")}
				return
			}
			if !inspect.Known || !inspect.Running {
				exits <- ExitResult{ExitCode: inspect.ExitCode}
				return
			}
			<-ticker.C
		}
	}()
	return identity, exits, nil
}

// copyDockerConsole decodes Docker's non-TTY eight-byte multiplex framing.
// v0.3 always creates non-TTY containers, so treating an unframed stream as
// output would risk silently attributing malformed data to a console session.
func copyDockerConsole(source io.ReadWriteCloser, output StartIO) {
	defer source.Close()
	reader := bufio.NewReader(source)
	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(reader, header); err != nil {
			return
		}
		if header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return
		}
		size := int(header[4])<<24 | int(header[5])<<16 | int(header[6])<<8 | int(header[7])
		if size < 0 || size > 64<<10 {
			return
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return
		}
		if header[0] == 1 && output.Stdout != nil {
			_, _ = output.Stdout.Write(payload)
		}
		if header[0] == 2 && output.Stderr != nil {
			_, _ = output.Stderr.Write(payload)
		}
	}
}
func (r *containerRuntime) Stop(c context.Context, i Identity, timeout time.Duration) error {
	if err := r.verify(c, i); err != nil {
		return err
	}
	return r.engine.Stop(c, containerID(i), timeout)
}
func (r *containerRuntime) Kill(c context.Context, i Identity) error {
	if err := r.verify(c, i); err != nil {
		return err
	}
	return r.engine.Kill(c, containerID(i))
}
func (r *containerRuntime) Interrupt(context.Context, Identity) error {
	return ErrConsoleInterruptUnsupported
}
func (r *containerRuntime) Status(c context.Context, i Identity) (Status, error) {
	v, e := r.engine.Inspect(c, containerID(i))
	if e == nil && !owned(i, v.Labels) {
		return Status{Known: false}, errors.New("container ownership is invalid")
	}
	return Status{Running: v.Running, Known: v.Known}, e
}
func (r *containerRuntime) Metrics(c context.Context, i Identity) (Metrics, error) {
	if err := r.verify(c, i); err != nil {
		return Metrics{}, err
	}
	return r.engine.Stats(c, containerID(i))
}
func (r *containerRuntime) ImageAvailable(c context.Context, image string) (bool, error) {
	return r.engine.ImageAvailable(c, image)
}
func (r *containerRuntime) PullImage(c context.Context, image string) error {
	if err := r.engine.Available(c); err != nil {
		return ErrEngineUnavailable
	}
	return r.engine.PullImage(c, image)
}
func (r *containerRuntime) verify(ctx context.Context, identity Identity) error {
	value, err := r.engine.Inspect(ctx, containerID(identity))
	if err != nil {
		return err
	}
	if !owned(identity, value.Labels) {
		return errors.New("container ownership is invalid")
	}
	return nil
}
func owned(identity Identity, labels map[string]string) bool {
	parts := strings.Split(strings.TrimPrefix(identity.StartKey, "container:"), ":")
	return len(parts) == 4 && parts[0] == containerID(identity) && labels["io.gamenode.managed"] == "true" && labels["io.gamenode.server_id"] == parts[1] && labels["io.gamenode.instance_generation"] == parts[2] && labels["io.gamenode.ownership_token"] == parts[3]
}
func containerID(i Identity) string {
	if i.ContainerID != "" {
		return i.ContainerID
	}
	return strings.SplitN(strings.TrimPrefix(i.StartKey, "container:"), ":", 2)[0]
}

// dockerEngine talks directly to Docker's Unix-socket Engine API. No Docker
// CLI, shell, or user-controlled endpoint is involved.
type dockerEngine struct {
	socket string
	client *http.Client
}

func NewDockerEngine() ContainerEngine {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}
	return &dockerEngine{socket: "/var/run/docker.sock", client: &http.Client{Transport: tr}}
}
func (e *dockerEngine) Available(ctx context.Context) error {
	if err := e.call(ctx, http.MethodGet, "/_ping", nil, nil); err != nil {
		return ErrEngineUnavailable
	}
	return nil
}
func (e *dockerEngine) Create(ctx context.Context, c ContainerOptions, start StartOptions) (string, error) {
	ports := map[string][]map[string]string{}
	exposed := map[string]map[string]struct{}{}
	for _, p := range c.Ports {
		key := strconv.Itoa(p.ContainerPort) + "/" + p.Protocol
		exposed[key] = map[string]struct{}{}
		host := p.BindAddress
		if host == "" {
			host = "0.0.0.0"
		}
		ports[key] = []map[string]string{{"HostIP": host, "HostPort": strconv.Itoa(p.HostPort)}}
	}
	hostConfig := map[string]any{"Binds": []string{start.WorkingDirectory + ":/home/container:rw"}, "Memory": c.MemoryLimitBytes, "NanoCpus": int64(c.CPULimitMillis) * 1_000_000, "NetworkMode": "bridge", "PortBindings": ports, "Privileged": false, "ReadonlyRootfs": false}
	if c.PIDsLimit > 0 {
		hostConfig["PidsLimit"] = c.PIDsLimit
	}
	if c.TmpfsSizeBytes > 0 {
		hostConfig["Tmpfs"] = map[string]string{"/tmp": "rw,nosuid,nodev,noexec,size=" + strconv.FormatInt(c.TmpfsSizeBytes, 10)}
	}
	body := map[string]any{"Image": c.Image, "Cmd": c.Command, "Env": containerEnvironment(start.Environment), "OpenStdin": true, "StdinOnce": false, "AttachStdout": true, "AttachStderr": true, "Labels": map[string]string{"io.gamenode.managed": "true", "io.gamenode.server_id": c.ServerID, "io.gamenode.instance_generation": c.Generation, "io.gamenode.ownership_token": c.OwnershipToken}, "ExposedPorts": exposed, "HostConfig": hostConfig}
	var out struct {
		ID string `json:"Id"`
	}
	if err := e.call(ctx, http.MethodPost, "/containers/create", body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("engine did not return container identity")
	}
	return out.ID, nil
}
func containerEnvironment(m map[string]string) []string {
	result := make([]string, 0, len(m))
	for k, v := range m {
		result = append(result, k+"="+v)
	}
	return result
}
func (e *dockerEngine) Start(c context.Context, id string) error {
	return e.call(c, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
}
func (e *dockerEngine) Stop(c context.Context, id string, t time.Duration) error {
	return e.call(c, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop?t="+strconv.Itoa(int(t.Seconds())), nil, nil)
}
func (e *dockerEngine) Kill(c context.Context, id string) error {
	return e.call(c, http.MethodPost, "/containers/"+url.PathEscape(id)+"/kill", nil, nil)
}
func (e *dockerEngine) Inspect(c context.Context, id string) (containerInspection, error) {
	var data struct {
		State struct {
			Running  bool `json:"Running"`
			ExitCode int  `json:"ExitCode"`
		} `json:"State"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	err := e.call(c, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, &data)
	if err != nil {
		return containerInspection{}, err
	}
	return containerInspection{Running: data.State.Running, Known: true, ExitCode: data.State.ExitCode, Labels: data.Config.Labels}, nil
}
func (e *dockerEngine) Stats(c context.Context, id string) (Metrics, error) {
	var data struct {
		MemoryStats struct {
			Usage uint64 `json:"usage"`
		} `json:"memory_stats"`
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
		} `json:"cpu_stats"`
	}
	if err := e.call(c, http.MethodGet, "/containers/"+url.PathEscape(id)+"/stats?stream=false", nil, &data); err != nil {
		return Metrics{}, err
	}
	return Metrics{MemoryBytes: data.MemoryStats.Usage, CPUTime: time.Duration(data.CPUStats.CPUUsage.TotalUsage)}, nil
}

type dockerAttach struct {
	net.Conn
	reader *bufio.Reader
}

func (a *dockerAttach) Read(p []byte) (int, error) { return a.reader.Read(p) }
func (e *dockerEngine) Attach(ctx context.Context, id string) (io.ReadWriteCloser, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", e.socket)
	if err != nil {
		return nil, ErrEngineUnavailable
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/containers/"+url.PathEscape(id)+"/attach?stream=1&stdin=1&stdout=1&stderr=1", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "tcp")
	if err = request.Write(conn); err != nil {
		conn.Close()
		return nil, ErrEngineUnavailable
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, errors.New("container console is unavailable")
	}
	return &dockerAttach{Conn: conn, reader: reader}, nil
}
func (e *dockerEngine) ImageAvailable(ctx context.Context, image string) (bool, error) {
	err := e.call(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, nil)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrEngineUnavailable) {
		return false, err
	}
	return false, nil
}
func (e *dockerEngine) ImageDigest(ctx context.Context, image string) (string, error) {
	var data struct {
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := e.call(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, &data); err != nil {
		return "", err
	}
	for _, value := range data.RepoDigests {
		if index := strings.LastIndex(value, "@"); index >= 0 && strings.HasPrefix(value[index+1:], "sha256:") {
			return value[index+1:], nil
		}
	}
	return "", errors.New("container image digest is unavailable")
}
func (e *dockerEngine) PullImage(ctx context.Context, image string) error {
	// Docker's pull response is an unbounded JSON event stream. It is consumed
	// and discarded here; GameNode exposes only a controlled terminal result.
	return e.call(ctx, http.MethodPost, "/images/create?fromImage="+url.QueryEscape(image), nil, nil)
}
func (e *dockerEngine) Remove(ctx context.Context, id string) error {
	return e.call(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=1", nil, nil)
}
func (e *dockerEngine) call(ctx context.Context, method, path string, body any, target any) error {
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, input)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := e.client.Do(req)
	if err != nil {
		return ErrEngineUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return fmt.Errorf("container engine request failed")
	}
	if target != nil {
		if err := json.NewDecoder(res.Body).Decode(target); err != nil && err != io.EOF {
			return err
		}
	} else {
		// Pull and engine responses are untrusted streams. Consume only a
		// bounded amount; the terminal result is all provisioning needs.
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 256<<10))
	}
	return nil
}

var _ = sync.Once{} // keep future attach implementation's synchronization local to this boundary.

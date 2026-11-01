package helper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"vpnmap/apps/agent/internal/sysexec"
	"vpnmap/pkg/protocol"
)

// DockerTemplate makes `docker inspect` print only the six fields the board
// needs, one JSON object per line. Env, Mounts, Labels, Args and everything
// else never leave the docker daemon.
const DockerTemplate = `{"name":{{json .Name}},"state":{{json .State.Status}},` +
	`"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}},` +
	`"started_at":{{json .State.StartedAt}},"restart_count":{{json .RestartCount}},` +
	`"image":{{json .Config.Image}}}`

// StateNotFound is reported for a configured container that does not exist.
const StateNotFound = "not_found"

// dockerPS inspects exactly the containers listed in agent.yaml.
func (s *Server) dockerPS(ctx context.Context) ([]protocol.Container, error) {
	args := append([]string{"inspect", "--type", "container", "--format", DockerTemplate, "--"}, s.Cfg.Containers...)
	res, err := s.Runner.Run(ctx, sysexec.Cmd{
		Name: "docker", Args: args,
		// Keep the docker CLI away from any user's ~/.docker config.
		Env: append(append([]string{}, sysexec.SafeEnv...), "HOME=/nonexistent", "DOCKER_CONFIG=/nonexistent"),
	})
	// docker exits 1 when one of several containers is missing but still
	// prints the others; only fail when nothing at all was printed.
	var ee *sysexec.ExitError
	if err != nil && !(errors.As(err, &ee) && len(bytes.TrimSpace(res.Stdout)) > 0) {
		return nil, err
	}
	return ParseDockerInspect(res.Stdout, s.Cfg.Containers)
}

// ParseDockerInspect parses DockerTemplate lines and returns one entry per
// wanted container, in config order; missing ones get State "not_found".
func ParseDockerInspect(b []byte, wanted []string) ([]protocol.Container, error) {
	type row struct {
		Name         string `json:"name"`
		State        string `json:"state"`
		Health       string `json:"health"`
		StartedAt    string `json:"started_at"`
		RestartCount int    `json:"restart_count"`
		Image        string `json:"image"`
	}
	got := map[string]protocol.Container{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r row
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, errors.New("cannot parse docker inspect output")
		}
		name := strings.TrimPrefix(r.Name, "/")
		got[name] = protocol.Container{
			Name: name, State: r.State, Health: r.Health,
			StartedAt: r.StartedAt, RestartCount: r.RestartCount, Image: r.Image,
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]protocol.Container, 0, len(wanted))
	for _, n := range wanted {
		c, ok := got[n]
		if !ok {
			c = protocol.Container{Name: n, State: StateNotFound}
		}
		out = append(out, c)
	}
	return out, nil
}

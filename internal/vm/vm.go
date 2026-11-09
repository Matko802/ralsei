package vm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

type Client struct {
	URI  string
	Name string
}

type execArgs struct {
	Path    string   `json:"path"`
	Arg     []string `json:"arg"`
	Capture bool     `json:"capture-output"`
}

type request struct {
	Execute   string `json:"execute"`
	Arguments any    `json:"arguments,omitempty"`
}

type execReply struct {
	Return *struct {
		PID int64 `json:"pid"`
	} `json:"return"`
	Error *struct {
		Desc string `json:"desc"`
	} `json:"error"`
}

type statusReply struct {
	Return *struct {
		Exited   bool   `json:"exited"`
		ExitCode int    `json:"exitcode"`
		OutData  string `json:"out-data"`
		ErrData  string `json:"err-data"`
	} `json:"return"`
	Error *struct {
		Desc string `json:"desc"`
	} `json:"error"`
}

func (c Client) run(payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("virsh", "--connect", c.URI, "qemu-agent-command", c.Name, string(body))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("virsh: %v: %s", err, out.String())
	}
	return out.Bytes(), nil
}

func (c Client) ping() error {
	raw, err := c.run(request{Execute: "guest-ping"})
	if err != nil {
		return err
	}
	var reply execReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return err
	}
	if reply.Error != nil {
		return fmt.Errorf("%s", reply.Error.Desc)
	}
	return nil
}

func (c Client) Exec(command string, timeout time.Duration) (string, int, error) {
	if err := c.ping(); err != nil {
		return "", -1, fmt.Errorf("guest agent unreachable: %w", err)
	}
	raw, err := c.run(request{
		Execute: "guest-exec",
		Arguments: execArgs{
			Path:    "/bin/bash",
			Arg:     []string{"-c", command + " 2>&1"},
			Capture: true,
		},
	})
	if err != nil {
		return "", -1, err
	}
	var launched execReply
	if err := json.Unmarshal(raw, &launched); err != nil {
		return "", -1, err
	}
	if launched.Error != nil {
		return "", -1, fmt.Errorf("%s", launched.Error.Desc)
	}
	if launched.Return == nil {
		return "", -1, fmt.Errorf("guest-exec returned no pid")
	}
	pid := launched.Return.PID
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		raw, err := c.run(request{Execute: "guest-exec-status", Arguments: map[string]any{"pid": pid}})
		if err != nil {
			return "", -1, err
		}
		var st statusReply
		if err := json.Unmarshal(raw, &st); err != nil {
			return "", -1, err
		}
		if st.Error != nil {
			return "", -1, fmt.Errorf("%s", st.Error.Desc)
		}
		if st.Return == nil || !st.Return.Exited {
			continue
		}
		var out bytes.Buffer
		if st.Return.OutData != "" {
			if data, err := base64.StdEncoding.DecodeString(st.Return.OutData); err == nil {
				out.Write(data)
			}
		}
		if st.Return.ErrData != "" {
			if data, err := base64.StdEncoding.DecodeString(st.Return.ErrData); err == nil {
				out.Write(data)
			}
		}
		return out.String(), st.Return.ExitCode, nil
	}
	return "", -1, fmt.Errorf("command timed out after %s", timeout)
}

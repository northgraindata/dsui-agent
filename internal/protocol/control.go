package protocol

import "encoding/json"

// Error codes carried by OPEN_ERR frames and control errors.
const (
	CodeNotAllowed   = "not_allowed"
	CodeUnreachable  = "unreachable"
	CodeTimeout      = "timeout"
	CodeTooManyOpen  = "too_many_streams"
	CodeBadTarget    = "bad_target"
	CodeUnauthorized = "unauthorized"
	CodeProtocol     = "protocol"
)

// Control message types (text WebSocket messages, JSON).
const (
	ControlHello     = "hello"
	ControlReady     = "ready"
	ControlHeartbeat = "heartbeat"
	ControlError     = "error"
)

// Control is the envelope of every text message. Unused fields are omitted.
type Control struct {
	Type string `json:"type"`

	// hello (agent -> server)
	Protocol   int      `json:"protocol,omitempty"`
	Version    string   `json:"version,omitempty"`
	Name       string   `json:"name,omitempty"`
	Hostname   string   `json:"hostname,omitempty"`
	OS         string   `json:"os,omitempty"`
	Arch       string   `json:"arch,omitempty"`
	LocalAddrs []string `json:"localAddrs,omitempty"`

	// hello and heartbeat (agent -> server)
	Targets       []string `json:"targets,omitempty"`
	ActiveStreams *int     `json:"activeStreams,omitempty"`

	// ready (server -> agent)
	ObservedIP string `json:"observedIp,omitempty"`
	MaxStreams int    `json:"maxStreams,omitempty"`

	// error (server -> agent)
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ErrorBody is the JSON payload of OPEN_ERR frames.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Marshal returns the JSON text of the control message.
func (c Control) Marshal() []byte {
	b, err := json.Marshal(c)
	if err != nil {
		// Control only holds plain values, so this cannot happen.
		panic(err)
	}
	return b
}

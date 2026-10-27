// Package config loads and saves the WxCat configuration file.
//
// The network section intentionally mirrors NapCat's OneBot 11 schema
// (httpServers / httpClients / websocketServers / websocketClients) so users
// can migrate an existing NapCat setup with minimal edits.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the root document of config/wxcat.json.
type Config struct {
	Backend BackendConfig `json:"backend"`

	// IDMode is "numeric" (default) or "raw". Numeric maps wxid strings to
	// stable int64 ids, which strict OneBot clients require. Raw exposes the
	// wxid directly.
	IDMode string `json:"idMode"`

	ReportSelfMessage   bool     `json:"reportSelfMessage"`
	EnableLocalFile2Url bool     `json:"enableLocalFile2Url"`
	GroupMode           string   `json:"groupMode"` // "at" (default) or "all"
	AtKeywords          []string `json:"atKeywords"`
	MediaBaseURL        string   `json:"mediaBaseURL"`
	LogLevel            string   `json:"logLevel"`

	Network NetworkConfig `json:"network"`
	WebUI   WebUIConfig   `json:"webui"`
}

// BackendConfig selects and configures the WeChat backend.
type BackendConfig struct {
	Type    string     `json:"type"` // wcf | mimicwx | webhook
	WCF     WCFConfig  `json:"wcf"`
	MimicWX RestConfig `json:"mimicwx"`
	Webhook RestConfig `json:"webhook"`
}

// WCFConfig points at the WeChatFerry RPC ports.
type WCFConfig struct {
	CmdAddr string `json:"cmdAddr"`
	MsgAddr string `json:"msgAddr"`
	// DecryptDir is where decrypted media is written before being handed to
	// the media store.
	DecryptDir string `json:"decryptDir"`
	// MediaURL points at the wcf-agent that runs next to WeChat. It is required
	// when the gateway and WeChat are on different machines, because media
	// decryption and upload must happen on the WeChat host.
	MediaURL   string `json:"mediaUrl"`
	MediaToken string `json:"mediaToken"`
}

// RestConfig describes an HTTP bridge such as MimicWX or a generic hook.
type RestConfig struct {
	BaseURL string `json:"baseUrl"`
	Token   string `json:"token"`

	// SelfWxid is used when the bridge does not report the logged-in account.
	// MimicWX's /status does not include it, so set it explicitly there.
	SelfWxid string `json:"selfWxid"`

	// Path overrides let the same adapter speak slightly different dialects.
	StatusPath    string `json:"statusPath"`
	ContactsPath  string `json:"contactsPath"`
	MessagesPath  string `json:"messagesPath"`
	SendTextPath  string `json:"sendTextPath"`
	SendImagePath string `json:"sendImagePath"`
	WSPath        string `json:"wsPath"`
	PollInterval  int    `json:"pollIntervalMs"`
}

// NetworkConfig groups all OneBot transports.
type NetworkConfig struct {
	HTTPServers      []HTTPServer      `json:"httpServers"`
	HTTPClients      []HTTPClient      `json:"httpClients"`
	WebsocketServers []WebsocketServer `json:"websocketServers"`
	WebsocketClients []WebsocketClient `json:"websocketClients"`
}

// HTTPServer exposes the OneBot HTTP API.
type HTTPServer struct {
	Name              string `json:"name"`
	Enable            bool   `json:"enable"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	MessagePostFormat string `json:"messagePostFormat"`
	Token             string `json:"token"`
	Debug             bool   `json:"debug"`
}

// HTTPClient posts events to a webhook URL.
type HTTPClient struct {
	Name              string `json:"name"`
	Enable            bool   `json:"enable"`
	URL               string `json:"url"`
	MessagePostFormat string `json:"messagePostFormat"`
	ReportSelfMessage bool   `json:"reportSelfMessage"`
	Token             string `json:"token"`
	Secret            string `json:"secret"`
	Debug             bool   `json:"debug"`
}

// WebsocketServer is a forward WebSocket endpoint clients dial into.
type WebsocketServer struct {
	Name              string `json:"name"`
	Enable            bool   `json:"enable"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	MessagePostFormat string `json:"messagePostFormat"`
	ReportSelfMessage bool   `json:"reportSelfMessage"`
	Token             string `json:"token"`
	HeartInterval     int    `json:"heartInterval"`
	Debug             bool   `json:"debug"`
}

// WebsocketClient dials a reverse WebSocket endpoint such as AstrBot.
type WebsocketClient struct {
	Name              string `json:"name"`
	Enable            bool   `json:"enable"`
	URL               string `json:"url"`
	MessagePostFormat string `json:"messagePostFormat"`
	ReportSelfMessage bool   `json:"reportSelfMessage"`
	Token             string `json:"token"`
	ReconnectInterval int    `json:"reconnectInterval"`
	HeartInterval     int    `json:"heartInterval"`
	Debug             bool   `json:"debug"`
}

// WebUIConfig is the embedded admin panel.
type WebUIConfig struct {
	Enable bool   `json:"enable"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Token  string `json:"token"`
}

// Default returns a runnable default configuration.
func Default() *Config {
	return &Config{
		Backend: BackendConfig{
			Type: "wcf",
			WCF: WCFConfig{
				CmdAddr:    "tcp://127.0.0.1:10086",
				MsgAddr:    "tcp://127.0.0.1:10087",
				DecryptDir: "",
			},
			MimicWX: RestConfig{
				BaseURL:       "http://127.0.0.1:8899",
				StatusPath:    "/status",
				ContactsPath:  "/contacts",
				MessagesPath:  "/messages/new",
				SendTextPath:  "/send",
				SendImagePath: "/send_image",
				WSPath:        "/ws",
				PollInterval:  2000,
			},
		},
		IDMode:              "numeric",
		ReportSelfMessage:   false,
		EnableLocalFile2Url: true,
		GroupMode:           "at",
		LogLevel:            "info",
		Network: NetworkConfig{
			HTTPServers: []HTTPServer{{
				Name:              "http",
				Enable:            true,
				Host:              "0.0.0.0",
				Port:              3000,
				MessagePostFormat: "array",
			}},
			WebsocketServers: []WebsocketServer{{
				Name:              "ws",
				Enable:            true,
				Host:              "0.0.0.0",
				Port:              3001,
				MessagePostFormat: "array",
				HeartInterval:     30000,
			}},
			WebsocketClients: []WebsocketClient{{
				Name:              "astrbot",
				Enable:            false,
				URL:               "ws://127.0.0.1:6199/ws",
				MessagePostFormat: "array",
				ReconnectInterval: 3000,
				HeartInterval:     30000,
			}},
		},
		WebUI: WebUIConfig{Enable: true, Host: "0.0.0.0", Port: 6099},
	}
}

// Load reads path, filling unspecified fields from Default.
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Decode on top of the defaults so new fields keep sane values.
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.normalize()
	return cfg, nil
}

// ApplyEnv overlays environment variables on top of the file config.
func (c *Config) ApplyEnv() {
	if v := os.Getenv("WXCAT_BACKEND"); v != "" {
		c.Backend.Type = v
	}
	if v := os.Getenv("WXCAT_ID_MODE"); v != "" {
		c.IDMode = v
	}
	if v := os.Getenv("WXCAT_TOKEN"); v != "" {
		// The container-level token is a convenience shortcut: it applies to
		// every OneBot endpoint that has no explicit token.
		for i := range c.Network.HTTPServers {
			if c.Network.HTTPServers[i].Token == "" {
				c.Network.HTTPServers[i].Token = v
			}
		}
		for i := range c.Network.WebsocketServers {
			if c.Network.WebsocketServers[i].Token == "" {
				c.Network.WebsocketServers[i].Token = v
			}
		}
		for i := range c.Network.WebsocketClients {
			if c.Network.WebsocketClients[i].Token == "" {
				c.Network.WebsocketClients[i].Token = v
			}
		}
		for i := range c.Network.HTTPClients {
			if c.Network.HTTPClients[i].Token == "" {
				c.Network.HTTPClients[i].Token = v
			}
		}
	}
	if v := os.Getenv("WXCAT_WEBUI_TOKEN"); v != "" {
		c.WebUI.Token = v
	}
	if v := os.Getenv("WXCAT_MEDIA_BASE_URL"); v != "" {
		c.MediaBaseURL = v
	}
	// Backend addresses are commonly overridden in container deployments,
	// especially when the gateway runs on arm64 and the WeChat hook runs on a
	// separate x86 host.
	if v := os.Getenv("WXCAT_WCF_CMD_ADDR"); v != "" {
		c.Backend.WCF.CmdAddr = v
	}
	if v := os.Getenv("WXCAT_WCF_MSG_ADDR"); v != "" {
		c.Backend.WCF.MsgAddr = v
	}
	if v := os.Getenv("WXCAT_WCF_MEDIA_URL"); v != "" {
		c.Backend.WCF.MediaURL = v
	}
	if v := os.Getenv("WXCAT_WCF_MEDIA_TOKEN"); v != "" {
		c.Backend.WCF.MediaToken = v
	}
	if v := os.Getenv("WXCAT_MIMICWX_BASE_URL"); v != "" {
		c.Backend.MimicWX.BaseURL = v
	}
	if v := os.Getenv("WXCAT_MIMICWX_TOKEN"); v != "" {
		c.Backend.MimicWX.Token = v
	}
	if v := os.Getenv("WXCAT_MIMICWX_SELF_WXID"); v != "" {
		c.Backend.MimicWX.SelfWxid = v
	}
	c.normalize()
}

// Normalize fills empty required fields with safe defaults.
func (c *Config) Normalize() { c.normalize() }

func (c *Config) normalize() {
	if c.IDMode != "raw" {
		c.IDMode = "numeric"
	}
	if c.GroupMode != "all" {
		c.GroupMode = "at"
	}
	if c.Backend.Type == "" {
		c.Backend.Type = "wcf"
	}
	if c.Backend.WCF.CmdAddr == "" {
		c.Backend.WCF.CmdAddr = "tcp://127.0.0.1:10086"
	}
	if c.Backend.WCF.MsgAddr == "" {
		c.Backend.WCF.MsgAddr = "tcp://127.0.0.1:10087"
	}
	for i := range c.Network.HTTPServers {
		if c.Network.HTTPServers[i].MessagePostFormat == "" {
			c.Network.HTTPServers[i].MessagePostFormat = "array"
		}
	}
	for i := range c.Network.WebsocketServers {
		if c.Network.WebsocketServers[i].MessagePostFormat == "" {
			c.Network.WebsocketServers[i].MessagePostFormat = "array"
		}
		if c.Network.WebsocketServers[i].HeartInterval <= 0 {
			c.Network.WebsocketServers[i].HeartInterval = 30000
		}
	}
	for i := range c.Network.WebsocketClients {
		if c.Network.WebsocketClients[i].MessagePostFormat == "" {
			c.Network.WebsocketClients[i].MessagePostFormat = "array"
		}
		if c.Network.WebsocketClients[i].ReconnectInterval <= 0 {
			c.Network.WebsocketClients[i].ReconnectInterval = 3000
		}
		if c.Network.WebsocketClients[i].HeartInterval <= 0 {
			c.Network.WebsocketClients[i].HeartInterval = 30000
		}
	}
}

// Save writes the configuration atomically.
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// IsGroup reports whether a wxid denotes a group chat.
func IsGroup(wxid string) bool {
	return strings.HasSuffix(wxid, "@chatroom")
}

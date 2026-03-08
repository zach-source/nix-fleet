package spire

import "text/template"

const agentConfTemplate = `agent {
    data_dir = "{{ .DataDir }}"
    log_level = "INFO"
    server_address = "{{ .ServerHost }}"
    server_port = "{{ .ServerPort }}"
    socket_path = "{{ .SocketPath }}"
    trust_bundle_path = "{{ .TrustBundlePath }}"
    trust_domain = "{{ .TrustDomain }}"
}

plugins {
    KeyManager "disk" {
        plugin_data {
            directory = "{{ .DataDir }}"
        }
    }

    NodeAttestor "join_token" {
        plugin_data {}
    }

    WorkloadAttestor "unix" {
        plugin_data {}
    }
}
`

const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>ai.stigen.nixfleet.spire-agent</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{ .AgentBinary }}</string>
        <string>run</string>
        <string>-config</string>
        <string>{{ .ConfigPath }}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{ .LogDir }}/spire-agent.log</string>
    <key>StandardErrorPath</key>
    <string>{{ .LogDir }}/spire-agent.err</string>
</dict>
</plist>
`

type AgentConfig struct {
	DataDir         string
	ServerHost      string
	ServerPort      string
	SocketPath      string
	TrustBundlePath string
	TrustDomain     string
	JoinToken       string
}

type LaunchdConfig struct {
	AgentBinary string
	ConfigPath  string
	LogDir      string
}

var agentConfTmpl = template.Must(template.New("agent.conf").Parse(agentConfTemplate))
var launchdPlistTmpl = template.Must(template.New("launchd.plist").Parse(launchdPlistTemplate))

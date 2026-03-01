package agenttui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nixfleet/nixfleet/internal/ssh"
)

const (
	defaultRefreshInterval = 10 * time.Second
	defaultLogLines        = 100
	listPaneWidth          = 24
)

// viewMode controls what the right panel displays.
type viewMode int

const (
	viewGateway viewMode = iota // OpenClaw gateway health/status
	viewLogs                    // raw pod logs
)

// Agent represents a running agent in the cluster.
type Agent struct {
	Name      string
	Namespace string
	PodName   string
	Status    string
	Ready     bool
	Restarts  int
	Age       string
	Health    *GatewayHealth // from openclaw health --json
	Logs      []string       // raw pod logs (fallback)
}

// Config holds configuration for the TUI.
type Config struct {
	SSHClient       *ssh.Client
	Host            string
	RefreshInterval time.Duration
}

// Model is the bubbletea model for the agent monitor TUI.
type Model struct {
	agents          []Agent
	selected        int
	sshClient       *ssh.Client
	host            string
	lastUpdate      time.Time
	refreshInterval time.Duration
	err             error
	width           int
	height          int
	logViewport     viewport.Model
	viewMode        viewMode
	searchQuery     string
	searching       bool
	filterQuery     string
	filtering       bool
	ready           bool
}

// Messages
type tickMsg time.Time
type agentStatusMsg []PodInfo

type agentHealthMsg struct {
	namespace string
	health    *GatewayHealth
}

type agentLogsMsg struct {
	namespace string
	logs      string
}

type errMsg error

// New creates a new TUI model.
func New(cfg Config) Model {
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = defaultRefreshInterval
	}
	vp := viewport.New(0, 0)
	return Model{
		sshClient:       cfg.SSHClient,
		host:            cfg.Host,
		refreshInterval: cfg.RefreshInterval,
		logViewport:     vp,
		viewMode:        viewGateway,
	}
}

// Run starts the bubbletea program.
func Run(cfg Config) error {
	m := New(cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(fetchStatus(m.sshClient), tick(m.refreshInterval))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.searching {
			return m.handleSearchInput(msg)
		}
		if m.filtering {
			return m.handleFilterInput(msg)
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
				cmds = append(cmds, m.fetchSelectedDetail())
			}
		case "down", "j":
			if m.selected < len(m.agents)-1 {
				m.selected++
				cmds = append(cmds, m.fetchSelectedDetail())
			}
		case "r":
			cmds = append(cmds, fetchStatus(m.sshClient))
		case "l":
			// Toggle between gateway view and logs view
			if m.viewMode == viewGateway {
				m.viewMode = viewLogs
			} else {
				m.viewMode = viewGateway
			}
			m.updateViewportContent()
			cmds = append(cmds, m.fetchSelectedDetail())
		case "/":
			m.searching = true
			m.searchQuery = ""
		case "f":
			m.filtering = true
			m.filterQuery = ""
		case "esc":
			m.searchQuery = ""
			m.filterQuery = ""
			m.updateViewportContent()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.logViewport.Width = msg.Width - listPaneWidth - 3
		m.logViewport.Height = msg.Height - 4
		m.ready = true

	case tickMsg:
		cmds = append(cmds, fetchStatus(m.sshClient), tick(m.refreshInterval))

	case agentStatusMsg:
		m.updateAgents([]PodInfo(msg))
		m.lastUpdate = time.Now()
		m.err = nil
		if len(m.agents) > 0 {
			cmds = append(cmds, m.fetchSelectedDetail())
		}

	case agentHealthMsg:
		for i, a := range m.agents {
			if a.Namespace == msg.namespace {
				m.agents[i].Health = msg.health
				if i == m.selected {
					m.updateViewportContent()
				}
				break
			}
		}

	case agentLogsMsg:
		for i, a := range m.agents {
			if a.Namespace == msg.namespace {
				m.agents[i].Logs = strings.Split(strings.TrimSpace(msg.logs), "\n")
				if i == m.selected && m.viewMode == viewLogs {
					m.updateViewportContent()
				}
				break
			}
		}

	case errMsg:
		m.err = msg
	}

	var vpCmd tea.Cmd
	m.logViewport, vpCmd = m.logViewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if !m.ready {
		return "Loading..."
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	selectedStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	greenDot := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("●")
	redX := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("✕")
	yellowDot := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("○")
	borderStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8"))

	// Header
	lastUpdateStr := "never"
	if !m.lastUpdate.IsZero() {
		ago := time.Since(m.lastUpdate).Truncate(time.Second)
		lastUpdateStr = fmt.Sprintf("↻ %s ago", ago)
	}
	modeLabel := "gateway"
	if m.viewMode == viewLogs {
		modeLabel = "logs"
	}
	header := titleStyle.Render("NixFleet Agent Monitor") +
		dimStyle.Render(fmt.Sprintf("  %s  [%s]  %s", m.host, modeLabel, lastUpdateStr))

	// Left panel
	var leftLines []string
	leftLines = append(leftLines, titleStyle.Render("AGENTS"), "")

	for i, a := range m.agents {
		indicator := yellowDot
		if a.Ready {
			indicator = greenDot
		} else if a.Status == "NotFound" || a.Status == "Failed" {
			indicator = redX
		}

		name := ShortName(a.Namespace)
		if len(name) > 12 {
			name = name[:12]
		}

		// Show channel status from gateway health if available
		statusText := a.Status
		if a.Ready {
			statusText = "UP"
			if a.Health != nil && len(a.Health.Channels) > 0 {
				for _, ch := range a.Health.Channels {
					if ch.Name == "slack" {
						if ch.Status == "connected" || ch.Status == "ok" || ch.Status == "running" {
							statusText = "UP ◆slack"
						} else {
							statusText = "UP ◇slack"
						}
						break
					}
				}
			}
		}

		line := fmt.Sprintf(" %s %-12s %s", indicator, name, dimStyle.Render(statusText))
		if i == m.selected {
			line = selectedStyle.Render(fmt.Sprintf("▸%s %-12s", indicator, name)) + " " + dimStyle.Render(statusText)
		}
		leftLines = append(leftLines, line)
	}

	if m.selected < len(m.agents) {
		a := m.agents[m.selected]
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, dimStyle.Render(fmt.Sprintf("Restarts: %d", a.Restarts)))
		leftLines = append(leftLines, dimStyle.Render(fmt.Sprintf("Uptime:   %s", a.Age)))
		if a.Health != nil && a.Health.Gateway.Status != "" {
			leftLines = append(leftLines, dimStyle.Render(fmt.Sprintf("Gateway:  %s", a.Health.Gateway.Status)))
		}
		if a.Health != nil && a.Health.Model != "" {
			leftLines = append(leftLines, dimStyle.Render(fmt.Sprintf("Model:    %s", a.Health.Model)))
		}
	}

	contentHeight := m.height - 4
	for len(leftLines) < contentHeight {
		leftLines = append(leftLines, "")
	}

	leftPanel := borderStyle.Width(listPaneWidth).Height(contentHeight).
		Render(strings.Join(leftLines, "\n"))

	// Right panel
	rightHeader := ""
	if m.selected < len(m.agents) {
		rightHeader = titleStyle.Render(DisplayName(m.agents[m.selected].Namespace))
	}
	rightContent := rightHeader + "\n" + m.logViewport.View()
	rightPanel := borderStyle.Width(m.width - listPaneWidth - 3).Height(contentHeight).
		Render(rightContent)

	// Footer
	footerText := "q: quit  ↑/↓: select  r: refresh  l: toggle logs/gateway  /: search  f: filter"
	if m.searching {
		footerText = fmt.Sprintf("search: %s█  (esc to cancel)", m.searchQuery)
	} else if m.filtering {
		footerText = fmt.Sprintf("filter: %s█  (esc to cancel)", m.filterQuery)
	}
	if m.err != nil {
		footerText = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).
			Render(fmt.Sprintf("Error: %v", m.err)) + "  " + footerText
	}
	footer := dimStyle.Render(footerText)

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, rightPanel)
	return header + "\n" + body + "\n" + footer
}

// updateViewportContent renders the right panel content based on current view mode.
func (m *Model) updateViewportContent() {
	if m.selected >= len(m.agents) {
		return
	}
	a := m.agents[m.selected]

	var content string
	switch m.viewMode {
	case viewGateway:
		content = m.renderGatewayHealth(a)
	case viewLogs:
		content = m.filterLines(a.Logs)
	}

	m.logViewport.SetContent(content)
	m.logViewport.GotoBottom()
}

// renderGatewayHealth formats the gateway health data for display.
func (m Model) renderGatewayHealth(a Agent) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	greenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	redStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

	if a.Health == nil {
		return dimStyle.Render("Fetching gateway health...")
	}

	if a.Health.Error != "" {
		return redStyle.Render(fmt.Sprintf("Health check error: %s", a.Health.Error))
	}

	var lines []string

	// Gateway status
	lines = append(lines, titleStyle.Render("Gateway"))
	gwStatus := a.Health.Gateway.Status
	if gwStatus == "" {
		gwStatus = "unknown"
	}
	statusStyle := greenStyle
	if gwStatus != "ok" && gwStatus != "running" && gwStatus != "healthy" {
		statusStyle = redStyle
	}
	lines = append(lines, fmt.Sprintf("  Status: %s", statusStyle.Render(gwStatus)))
	if a.Health.Gateway.Uptime != "" {
		lines = append(lines, fmt.Sprintf("  Uptime: %s", a.Health.Gateway.Uptime))
	}
	if a.Health.Model != "" {
		lines = append(lines, fmt.Sprintf("  Model:  %s", a.Health.Model))
	}
	lines = append(lines, "")

	// Channels
	if len(a.Health.Channels) > 0 {
		lines = append(lines, titleStyle.Render("Channels"))
		for _, ch := range a.Health.Channels {
			indicator := greenStyle.Render("●")
			if ch.Status != "connected" && ch.Status != "ok" && ch.Status != "running" {
				indicator = redStyle.Render("✕")
			}
			lines = append(lines, fmt.Sprintf("  %s %s: %s", indicator, ch.Name, ch.Status))
		}
		lines = append(lines, "")
	}

	// Sessions
	if len(a.Health.Sessions) > 0 {
		lines = append(lines, titleStyle.Render("Sessions"))
		for _, s := range a.Health.Sessions {
			agentLabel := ""
			if s.Agent != "" {
				agentLabel = fmt.Sprintf(" [%s]", s.Agent)
			}
			msgLabel := ""
			if s.Messages > 0 {
				msgLabel = fmt.Sprintf(" (%d msgs)", s.Messages)
			}
			lastLabel := ""
			if s.LastActive != "" {
				lastLabel = fmt.Sprintf(" %s", dimStyle.Render(s.LastActive))
			}
			lines = append(lines, fmt.Sprintf("  %s%s%s%s", s.Key, agentLabel, msgLabel, lastLabel))
		}
		lines = append(lines, "")
	}

	// Raw JSON fields we haven't specifically parsed
	if a.Health.Raw != nil {
		lines = append(lines, titleStyle.Render("Details"))
		for k, v := range a.Health.Raw {
			if k == "gateway" || k == "channels" || k == "sessions" || k == "model" || k == "agents" {
				continue
			}
			lines = append(lines, fmt.Sprintf("  %s: %v", k, v))
		}
	}

	result := strings.Join(lines, "\n")
	return m.applyFilter(result)
}

// filterLines applies search/filter to log lines.
func (m Model) filterLines(logs []string) string {
	var filtered []string
	for _, line := range logs {
		if m.searchQuery != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(m.searchQuery)) {
			continue
		}
		if m.filterQuery != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(m.filterQuery)) {
			continue
		}
		filtered = append(filtered, line)
	}
	if len(filtered) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("(no logs)")
	}
	return strings.Join(filtered, "\n")
}

// applyFilter filters rendered content by search/filter query (line-level).
func (m Model) applyFilter(content string) string {
	if m.searchQuery == "" && m.filterQuery == "" {
		return content
	}
	lines := strings.Split(content, "\n")
	var filtered []string
	for _, line := range lines {
		plain := strings.ToLower(line)
		if m.searchQuery != "" && !strings.Contains(plain, strings.ToLower(m.searchQuery)) {
			continue
		}
		if m.filterQuery != "" && !strings.Contains(plain, strings.ToLower(m.filterQuery)) {
			continue
		}
		filtered = append(filtered, line)
	}
	if len(filtered) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("(no matches)")
	}
	return strings.Join(filtered, "\n")
}

func (m *Model) updateAgents(pods []PodInfo) {
	// Preserve existing health/logs data
	existingHealth := make(map[string]*GatewayHealth)
	existingLogs := make(map[string][]string)
	for _, a := range m.agents {
		existingHealth[a.Namespace] = a.Health
		existingLogs[a.Namespace] = a.Logs
	}

	m.agents = nil
	for _, ns := range AgentNamespaces {
		a := Agent{
			Namespace: ns,
			PodName:   ns + "-0",
			Status:    "NotFound",
		}
		for _, p := range pods {
			if p.Namespace == ns {
				a.PodName = p.Name
				a.Status = p.Status
				a.Ready = p.Ready
				a.Restarts = p.Restarts
				a.Age = p.Age
				break
			}
		}
		a.Health = existingHealth[ns]
		a.Logs = existingLogs[ns]
		m.agents = append(m.agents, a)
	}

	if m.selected >= len(m.agents) {
		m.selected = len(m.agents) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
}

// fetchSelectedDetail returns the appropriate fetch command for the selected agent.
func (m Model) fetchSelectedDetail() tea.Cmd {
	if m.selected >= len(m.agents) {
		return nil
	}
	a := m.agents[m.selected]
	switch m.viewMode {
	case viewGateway:
		return fetchHealth(m.sshClient, a.Namespace, a.PodName)
	case viewLogs:
		return fetchLogs(m.sshClient, a.Namespace, a.PodName)
	}
	return nil
}

func (m Model) handleSearchInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searching = false
		m.searchQuery = ""
		m.updateViewportContent()
	case "enter":
		m.searching = false
		m.updateViewportContent()
	case "backspace":
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
		}
		m.updateViewportContent()
	default:
		if len(msg.String()) == 1 {
			m.searchQuery += msg.String()
			m.updateViewportContent()
		}
	}
	return m, nil
}

func (m Model) handleFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filtering = false
		m.filterQuery = ""
		m.updateViewportContent()
	case "enter":
		m.filtering = false
		m.updateViewportContent()
	case "backspace":
		if len(m.filterQuery) > 0 {
			m.filterQuery = m.filterQuery[:len(m.filterQuery)-1]
		}
		m.updateViewportContent()
	default:
		if len(msg.String()) == 1 {
			m.filterQuery += msg.String()
			m.updateViewportContent()
		}
	}
	return m, nil
}

// Commands

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func fetchStatus(client *ssh.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		pods, err := GetPods(ctx, client)
		if err != nil {
			return errMsg(err)
		}
		return agentStatusMsg(pods)
	}
}

func fetchHealth(client *ssh.Client, namespace, podName string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		health, err := GetGatewayHealth(ctx, client, namespace, podName)
		if err != nil {
			return agentHealthMsg{namespace: namespace, health: &GatewayHealth{
				Namespace: namespace,
				Error:     err.Error(),
			}}
		}
		return agentHealthMsg{namespace: namespace, health: health}
	}
}

func fetchLogs(client *ssh.Client, namespace, podName string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		logs, err := GetLogs(ctx, client, namespace, podName, defaultLogLines)
		if err != nil {
			return agentLogsMsg{namespace: namespace, logs: fmt.Sprintf("Error: %v", err)}
		}
		return agentLogsMsg{namespace: namespace, logs: logs}
	}
}

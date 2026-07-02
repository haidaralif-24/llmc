package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"llmc/internal/config"
	"llmc/internal/provider"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type chatMessage struct {
	role    string
	content string
}

type model struct {
	cfg      *config.Config
	prov     provider.Provider
	provName string
	model    string

	messages []chatMessage
	viewport viewport.Model
	input    textinput.Model
	ready    bool

	streaming bool
	cancel    context.CancelFunc
	pending   string

	program *tea.Program
}

type streamMsg struct {
	tok provider.Token
}

func initialModel(cfg *config.Config, prov provider.Provider, provName, modelName string) *model {
	vp := viewport.New(80, 20)
	vp.Style = lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("8"))

	ti := textinput.New()
	ti.Placeholder = "Type a message..."
	ti.Focus()
	ti.Width = 80

	return &model{
		cfg:      cfg,
		prov:     prov,
		provName: provName,
		model:    modelName,
		viewport: vp,
		input:    ti,
	}
}

func Run(cfg *config.Config) error {
	if cfg.DefaultProvider == "" {
		return fmt.Errorf("no default_provider set in config.toml")
	}
	pc, ok := cfg.Providers[cfg.DefaultProvider]
	if !ok {
		return fmt.Errorf("default_provider %q has no matching [providers.%s] section", cfg.DefaultProvider, cfg.DefaultProvider)
	}
	key, err := cfg.ResolveKey(cfg.DefaultProvider)
	if err != nil {
		return err
	}
	prov, err := provider.FromType(pc.Type, cfg.DefaultProvider, pc.Endpoint, key)
	if err != nil {
		return err
	}

	m := initialModel(cfg, prov, cfg.DefaultProvider, cfg.DefaultModel)
	p := tea.NewProgram(m, tea.WithAltScreen())
	m.program = p
	_, err = p.Run()
	return err
}

func (m *model) Init() tea.Cmd {
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		return m.handleResize(msg)
	case streamMsg:
		return m.handleStream(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.viewport, _ = m.viewport.Update(msg)
	return m, cmd
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.streaming {
		switch msg.String() {
		case "esc":
			if m.cancel != nil {
				m.cancel()
			}
			return m, nil
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		return m.submitMessage()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	headerHeight := 0
	footerHeight := 1

	if !m.ready {
		m.viewport = viewport.New(msg.Width, msg.Height-footerHeight)
		m.viewport.YPosition = headerHeight
		m.input.Width = msg.Width
		m.ready = true
	} else {
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - footerHeight
		m.input.Width = msg.Width
	}

	m.updateViewport()
	return m, nil
}

func (m *model) handleStream(msg streamMsg) (tea.Model, tea.Cmd) {
	tok := msg.tok
	if tok.Err != nil {
		if errors.Is(tok.Err, context.Canceled) {
			if m.pending != "" {
				m.messages = append(m.messages, chatMessage{role: "assistant", content: m.pending})
			}
		} else {
			m.messages = append(m.messages, chatMessage{role: "assistant", content: "(error: " + tok.Err.Error() + ")"})
		}
		m.streaming = false
		m.pending = ""
		m.cancel = nil
		m.updateViewport()
		m.viewport.GotoBottom()
		m.input.Focus()
		return m, textinput.Blink
	}
	if tok.Done {
		m.messages = append(m.messages, chatMessage{role: "assistant", content: m.pending})
		m.streaming = false
		m.pending = ""
		m.cancel = nil
		m.updateViewport()
		m.viewport.GotoBottom()
		m.input.Focus()
		return m, textinput.Blink
	}
	if tok.Text != "" {
		m.pending += tok.Text
		m.updateViewport()
		m.viewport.GotoBottom()
	}
	return m, nil
}

func (m *model) submitMessage() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	m.input.SetValue("")
	m.input.Blur()

	m.messages = append(m.messages, chatMessage{role: "user", content: text})
	m.updateViewport()
	m.viewport.GotoBottom()

	ctx, cancel := context.WithCancel(context.Background())
	m.streaming = true
	m.cancel = cancel
	m.pending = ""

	msgs := toProviderMessages(m.messages)
	go func() {
		tokens, err := m.prov.Stream(ctx, m.model, msgs)
		if err != nil {
			m.program.Send(streamMsg{tok: provider.Token{Err: err}})
			return
		}
		for tok := range tokens {
			m.program.Send(streamMsg{tok: tok})
		}
	}()

	return m, nil
}

func toProviderMessages(msgs []chatMessage) []provider.Message {
	pmsgs := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.content == "" {
			continue
		}
		pmsgs = append(pmsgs, provider.Message{Role: m.role, Content: m.content})
	}
	return pmsgs
}

func (m *model) updateViewport() {
	var b strings.Builder
	for _, msg := range m.messages {
		label := msg.role
		if label == "user" {
			label = "you"
		}
		b.WriteString(fmt.Sprintf("[%s] %s\n\n", label, msg.content))
	}
	if m.pending != "" {
		b.WriteString(fmt.Sprintf("[assistant] %s", m.pending))
	} else if m.streaming {
		b.WriteString("[assistant] ...")
	}
	m.viewport.SetContent(b.String())
}

func (m *model) View() string {
	if !m.ready {
		return "\n  Initializing..."
	}
	return fmt.Sprintf("%s\n%s", m.viewport.View(), m.input.View())
}

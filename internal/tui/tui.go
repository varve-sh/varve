// Package tui implements a terminal UI for browsing and managing memories.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
)

// Browse launches the interactive TUI browser and blocks until the user exits.
func Browse(k *kernel.MemoryKernel) error {
	memories, err := k.List(types.ListOptions{Limit: 500})
	if err != nil {
		return fmt.Errorf("loading memories: %w", err)
	}

	m := newModel(k, memories)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// --- styles ---

var (
	styleApp = lipgloss.NewStyle().Padding(0, 1)

	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	styleStatus = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	styleDetail = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1)

	styleTag = lipgloss.NewStyle().
			Background(lipgloss.Color("62")).
			Foreground(lipgloss.Color("230")).
			Padding(0, 1).
			MarginRight(1)

	styleBadgeDecision   = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))
	styleBadgeConvention = lipgloss.NewStyle().Foreground(lipgloss.Color("35"))
	styleBadgeFact       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleBadgeEvent      = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleBadgeStale      = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	styleConfirmBar = lipgloss.NewStyle().
			Background(lipgloss.Color("196")).
			Foreground(lipgloss.Color("230")).
			Padding(0, 1).
			Bold(true)

	styleHelp = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

// --- view state ---

type viewState int

const (
	viewList viewState = iota
	viewDetail
	viewConfirmDelete
)

// --- list item ---

type memoryItem struct {
	m types.Memory
}

func (i memoryItem) FilterValue() string {
	return i.m.Content + " " + strings.Join(i.m.Tags, " ")
}

func (i memoryItem) Title() string {
	content := i.m.Content
	if len([]rune(content)) > 72 {
		content = string([]rune(content)[:69]) + "..."
	}
	badge := typeBadge(i.m)
	return badge + " " + content
}

func (i memoryItem) Description() string {
	parts := []string{formatAge(i.m.CreatedAt)}
	if len(i.m.Tags) > 0 {
		parts = append(parts, "tags: "+strings.Join(i.m.Tags, ", "))
	}
	if len(i.m.FilePaths) > 0 {
		parts = append(parts, "files: "+strings.Join(i.m.FilePaths, ", "))
	}
	return strings.Join(parts, "  ·  ")
}

func typeBadge(m types.Memory) string {
	label := fmt.Sprintf("[%-10s]", string(m.Type))
	if m.Status == types.MemoryStatusStale {
		return styleBadgeStale.Render("[stale     ]")
	}
	switch m.Type {
	case types.MemoryTypeDecision:
		return styleBadgeDecision.Render(label)
	case types.MemoryTypeConvention:
		return styleBadgeConvention.Render(label)
	case types.MemoryTypeNote:
		return styleBadgeFact.Render(label)
	default:
		return styleBadgeFact.Render(label)
	}
}

// --- model ---

type deletedMsg struct{ id string }
type editorDoneMsg struct{ id string }
type errMsg struct{ err error }

type model struct {
	kernel   *kernel.MemoryKernel
	memories []types.Memory
	list     list.Model
	detail   viewport.Model
	state    viewState
	selected *types.Memory
	width    int
	height   int
	err      error
}

func newModel(k *kernel.MemoryKernel, memories []types.Memory) model {
	items := make([]list.Item, len(memories))
	for i, m := range memories {
		items[i] = memoryItem{m}
	}

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = true

	l := list.New(items, delegate, 0, 0)
	l.Title = "memtrace"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.Styles.Title = styleTitle
	l.SetStatusBarItemName("memory", "memories")

	return model{
		kernel:   k,
		memories: memories,
		list:     l,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h, v := styleApp.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v-1) // -1 for help bar
		m.detail = viewport.New(msg.Width-h-4, msg.Height-v-6)
		m.detail.Style = styleDetail

	case tea.KeyMsg:
		// Global quit
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.handleKey(msg)

	case deletedMsg:
		// Remove from list
		m.memories = removeByID(m.memories, msg.id)
		items := make([]list.Item, len(m.memories))
		for i, mem := range m.memories {
			items[i] = memoryItem{mem}
		}
		m.list.SetItems(items)
		m.state = viewList

	case editorDoneMsg:
		// Reload the edited memory
		if updated, err := m.kernel.Get(msg.id); err == nil && updated != nil {
			m.selected = updated
			m.detail.SetContent(renderDetail(updated))
		}

	case errMsg:
		m.err = msg.err
	}

	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case viewList:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "enter":
			if item, ok := m.list.SelectedItem().(memoryItem); ok {
				mem := item.m
				m.selected = &mem
				m.detail.SetContent(renderDetail(&mem))
				m.detail.GotoTop()
				m.state = viewDetail
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd

	case viewDetail:
		switch msg.String() {
		case "q", "esc":
			m.state = viewList
			return m, nil
		case "d":
			m.state = viewConfirmDelete
			return m, nil
		case "e":
			return m, m.openEditor()
		}
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd

	case viewConfirmDelete:
		switch msg.String() {
		case "y", "Y":
			if m.selected != nil {
				id := m.selected.ID
				outcome, err := m.kernel.Forget(id, types.ActorHuman)
				if err != nil {
					return m, func() tea.Msg { return errMsg{err} }
				}
				if outcome == kernel.DisposalNothing {
					// Nothing happened — an already-terminal decision has
					// nothing to forget. Reporting it as removed dropped the row
					// from the list until the next reload brought it back (F29).
					m.state = viewDetail
					return m, nil
				}
				return m, func() tea.Msg { return deletedMsg{id} }
			}
		case "n", "N", "esc":
			m.state = viewDetail
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.err != nil {
		return "Error: " + m.err.Error() + "\n\nPress q to quit."
	}

	switch m.state {
	case viewList:
		help := styleHelp.Render("enter: view  /: filter  q: quit")
		return styleApp.Render(m.list.View()) + "\n" + help

	case viewDetail:
		if m.selected == nil {
			return ""
		}
		help := styleHelp.Render("↑/↓: scroll  e: edit  d: forget  esc: back  q: quit")
		return styleApp.Render(m.detail.View()) + "\n" + help

	case viewConfirmDelete:
		confirm := styleConfirmBar.Render(disposalPrompt(m.selected))
		preview := ""
		if m.selected != nil {
			preview = styleDetail.Render(truncate(m.selected.Content, 80))
		}
		return styleApp.Render(m.detail.View()) + "\n" + confirm + "  " + preview
	}
	return ""
}

// disposalPrompt says what pressing y will actually do.
//
// `d` calls kernel.Delete, which for a decision is not a delete at all: a
// proposal is *rejected* and a binding decision is *reverted*, both terminal
// and both irreversible (§D3 — a decision with history is never hard-deleted,
// so "forget" maps onto the lifecycle). Prompting "Delete this memory?" for
// those hid an irreversible governance action behind a word that described
// neither.
func disposalPrompt(mem *types.Memory) string {
	if mem == nil {
		return "Forget this memory? (y/n)"
	}
	if !mem.Type.IsDecision() {
		return "Delete this note? This removes it permanently. (y/n)"
	}
	switch types.DecisionStatus(mem.Status) {
	case types.StatusProposed:
		return "Reject this proposal? It is recorded as rejected, not deleted, and cannot be accepted afterwards. (y/n)"
	case types.StatusActive, types.StatusViolated:
		return "Revert this decision? This is terminal: re-adopting it later means a new decision. (y/n)"
	default:
		return "This decision is already terminal — nothing to forget. (esc to go back)"
	}
}

func (m model) openEditor() tea.Cmd {
	if m.selected == nil {
		return nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	// Write content to a temp file.
	tmp, err := os.CreateTemp("", "memtrace-*.txt")
	if err != nil {
		return func() tea.Msg { return errMsg{err} }
	}
	tmp.WriteString(m.selected.Content)
	tmp.Close()
	tmpPath := tmp.Name()
	id := m.selected.ID

	return tea.ExecProcess(exec.Command(editor, tmpPath), func(err error) tea.Msg {
		defer os.Remove(tmpPath)
		if err != nil {
			return errMsg{err}
		}
		data, readErr := os.ReadFile(tmpPath)
		if readErr != nil {
			return errMsg{readErr}
		}
		newContent := strings.TrimSpace(string(data))
		if newContent == "" || newContent == m.selected.Content {
			return editorDoneMsg{id}
		}
		_, saveErr := m.kernel.Update(id, types.MemoryUpdateInput{Content: &newContent})
		if saveErr != nil {
			return errMsg{saveErr}
		}
		return editorDoneMsg{id}
	})
}

// --- render helpers ---

func renderDetail(m *types.Memory) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s  %s\n", typeBadge(*m), styleStatus.Render(formatAge(m.CreatedAt)))
	fmt.Fprintf(&b, "%s\n\n", styleTitle.Render("ID: "+m.ID))
	fmt.Fprintf(&b, "%s\n\n", m.Content)

	if len(m.Tags) > 0 {
		for _, tag := range m.Tags {
			b.WriteString(styleTag.Render(tag))
		}
		b.WriteString("\n\n")
	}

	if len(m.FilePaths) > 0 {
		fmt.Fprintf(&b, "Files: %s\n", strings.Join(m.FilePaths, ", "))
	}

	fmt.Fprintf(&b, "Confidence: %.2f", m.Confidence)
	if m.AccessCount > 0 {
		fmt.Fprintf(&b, "  ·  accessed %d times", m.AccessCount)
	}
	b.WriteString("\n")

	return b.String()
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	}
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-3]) + "..."
}

func removeByID(ms []types.Memory, id string) []types.Memory {
	out := ms[:0]
	for _, m := range ms {
		if m.ID != id {
			out = append(out, m)
		}
	}
	return out
}

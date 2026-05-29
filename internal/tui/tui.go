package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/scheduler"
	"github.com/zhh2001/rote/internal/store"
)

const historyLimit = 50

type viewMode int

const (
	listView viewMode = iota
	detailView
)

type focusArea int

const (
	focusHistory focusArea = iota
	focusOutput
)

var (
	titleStyle     = lipgloss.NewStyle().Bold(true)
	headerBarStyle = lipgloss.NewStyle().Padding(0, 1)
	subtitleStyle  = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	sectionStyle   = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	dimStyle       = lipgloss.NewStyle().Faint(true).Padding(0, 1)
	footerStyle    = lipgloss.NewStyle().Padding(0, 1)
)

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

type model struct {
	ctx    context.Context
	store  *store.Store
	jobs   []config.Job
	scheds map[string]scheduler.Schedule

	now    time.Time
	mode   viewMode
	width  int
	height int

	listTbl table.Model
	latest  map[string]store.RunMeta

	jobIdx   int
	history  []store.RunMeta
	histTbl  table.Model
	vp       viewport.Model
	focus    focusArea
	outputID int64
	output   store.Output
	outputOK bool

	help help.Model
	keys keyMap

	loadErr error
}

// Run renders the dashboard until the user quits or ctx is canceled. It only
// reads from the store; it never executes jobs.
func Run(ctx context.Context, jobs []config.Job, st *store.Store) error {
	p := tea.NewProgram(newModel(ctx, jobs, st), tea.WithAltScreen(), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, tea.ErrProgramKilled) {
			return nil
		}
		return err
	}
	return nil
}

func newModel(ctx context.Context, jobs []config.Job, st *store.Store) model {
	scheds := make(map[string]scheduler.Schedule, len(jobs))
	for _, j := range jobs {
		if s, err := scheduler.Parse(j.Schedule); err == nil {
			scheds[j.Name] = s
		}
	}
	m := model{
		ctx:    ctx,
		store:  st,
		jobs:   jobs,
		scheds: scheds,
		now:    time.Now(),
		mode:   listView,
		help:   help.New(),
		keys:   defaultKeys(),
	}
	m.listTbl = newTable(listColumns(), true)
	m.histTbl = newTable(historyColumns(), false)
	m.vp = viewport.New(0, 0)
	m.refreshLatest()
	m.rebuildListRows()
	return m
}

func listColumns() []table.Column {
	return []table.Column{
		{Title: "NAME", Width: 18},
		{Title: "SCHEDULE", Width: 22},
		{Title: "NEXT", Width: 14},
		{Title: "LAST", Width: 26},
	}
}

func historyColumns() []table.Column {
	return []table.Column{
		{Title: "TIME", Width: 19},
		{Title: "STATUS", Width: 7},
		{Title: "EXIT", Width: 6},
		{Title: "DURATION", Width: 10},
	}
}

func newTable(cols []table.Column, focused bool) table.Model {
	t := table.New(
		table.WithColumns(cols),
		table.WithFocused(focused),
		table.WithHeight(10),
	)
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true)
	s.Selected = s.Selected.Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6"))
	t.SetStyles(s)
	return t
}

func (m model) Init() tea.Cmd {
	return tickCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
		m.refresh()
		return m, tickCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// refresh re-reads the store for whichever view is active.
func (m *model) refresh() {
	if m.mode == listView {
		m.refreshLatest()
		m.rebuildListRows()
		return
	}
	m.loadHistory()
	m.refreshOutput()
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Refresh):
		m.now = time.Now()
		m.refresh()
		return m, nil
	}

	if m.mode == listView {
		return m.handleListKey(msg)
	}
	return m.handleDetailKey(msg)
}

func (m model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Enter) {
		if len(m.jobs) == 0 {
			return m, nil
		}
		m.jobIdx = m.listTbl.Cursor()
		m.mode = detailView
		m.focus = focusHistory
		m.listTbl.Blur()
		m.histTbl.Focus()
		m.histTbl.SetCursor(0)
		m.outputID = 0
		m.loadHistory()
		m.refreshOutput()
		m.layout()
		return m, nil
	}

	var cmd tea.Cmd
	m.listTbl, cmd = m.listTbl.Update(msg)
	return m, cmd
}

func (m model) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		m.mode = listView
		m.histTbl.Blur()
		m.listTbl.Focus()
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Tab):
		if m.focus == focusHistory {
			m.focus = focusOutput
			m.histTbl.Blur()
		} else {
			m.focus = focusHistory
			m.histTbl.Focus()
		}
		return m, nil
	}

	if m.focus == focusHistory {
		prev := m.histTbl.Cursor()
		var cmd tea.Cmd
		m.histTbl, cmd = m.histTbl.Update(msg)
		if m.histTbl.Cursor() != prev {
			m.refreshOutput()
		}
		return m, cmd
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m *model) refreshLatest() {
	latest, err := m.store.LatestMetaPerJob(m.ctx)
	if err != nil {
		m.loadErr = err
		return
	}
	m.latest = latest
}

func (m *model) rebuildListRows() {
	rows := make([]table.Row, len(m.jobs))
	for i, j := range m.jobs {
		meta, ok := m.latest[j.Name]
		rows[i] = table.Row{
			j.Name,
			j.Schedule,
			formatNext(m.scheds[j.Name], m.now),
			formatLast(meta, ok, m.now),
		}
	}
	cur := clamp(m.listTbl.Cursor(), 0, len(rows)-1)
	m.listTbl.SetRows(rows)
	m.listTbl.SetCursor(cur)
}

func (m *model) loadHistory() {
	if m.jobIdx < 0 || m.jobIdx >= len(m.jobs) {
		return
	}
	runs, err := m.store.RecentRunsMeta(m.ctx, m.jobs[m.jobIdx].Name, historyLimit)
	if err != nil {
		m.loadErr = err
		return
	}
	m.history = runs
	rows := make([]table.Row, len(runs))
	for i, r := range runs {
		rows[i] = table.Row{
			r.StartedAt.Local().Format("2006-01-02 15:04:05"),
			statusSymbol(r.Success),
			fmt.Sprintf("%d", r.ExitCode),
			durShort(r.Duration),
		}
	}
	cur := clamp(m.histTbl.Cursor(), 0, len(rows)-1)
	m.histTbl.SetRows(rows)
	m.histTbl.SetCursor(cur)
}

// refreshOutput loads the selected run's output, but only when the selection
// has changed since the last load.
func (m *model) refreshOutput() {
	if len(m.history) == 0 {
		m.outputID = 0
		m.outputOK = false
		m.output = store.Output{}
		m.vp.SetContent("no runs yet")
		return
	}
	idx := clamp(m.histTbl.Cursor(), 0, len(m.history)-1)
	id := m.history[idx].ID
	if id == m.outputID {
		return
	}

	out, ok, err := m.store.RunOutput(m.ctx, id)
	m.outputID = id
	if err != nil {
		m.loadErr = err
		m.outputOK = false
		m.vp.SetContent("error loading output: " + err.Error())
		return
	}
	m.outputOK = ok
	if !ok {
		m.output = store.Output{}
		m.vp.SetContent("(no output)")
		return
	}
	m.output = out
	m.vp.SetContent(renderOutput(out))
	m.vp.GotoTop()
}

func (m *model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	footerH := 1
	if m.help.ShowAll {
		footerH = 3
	}
	const headerH = 1

	listH := m.height - headerH - footerH - 1
	if listH < 1 {
		listH = 1
	}
	m.listTbl.SetHeight(listH)
	m.listTbl.SetWidth(m.width)

	contentH := m.height - headerH - footerH - 2
	if contentH < 2 {
		contentH = 2
	}
	histH := contentH / 2
	if histH < 1 {
		histH = 1
	}
	vpH := contentH - histH
	if vpH < 1 {
		vpH = 1
	}
	m.histTbl.SetHeight(histH)
	m.histTbl.SetWidth(m.width)
	m.vp.Width = m.width
	m.vp.Height = vpH
	m.help.Width = m.width
}

func (m model) View() string {
	header := m.headerView()
	footer := footerStyle.Render(m.help.View(m.keys))

	var body string
	if m.mode == listView {
		body = m.listTbl.View()
	} else {
		body = m.detailView()
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m model) headerView() string {
	info := fmt.Sprintf("%d jobs · %s", len(m.jobs), m.now.Format("15:04:05"))
	return headerBarStyle.Render(titleStyle.Render("rote") + "  " + info)
}

func (m model) detailView() string {
	name := ""
	if m.jobIdx >= 0 && m.jobIdx < len(m.jobs) {
		name = m.jobs[m.jobIdx].Name
	}
	title := subtitleStyle.Render("job: " + name)
	if len(m.history) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, title, dimStyle.Render("no runs yet"))
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		title,
		m.histTbl.View(),
		sectionStyle.Render("output"),
		m.vp.View(),
	)
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

package main

import (
	"sync"

	log "github.com/Sirupsen/logrus"
)

const (
	hvBranchViewWidth = 35
)

type viewOrientation int

const (
	voDefault viewOrientation = iota
	voColumn
	voCount
)

type viewPosition int

const (
	vp1 viewPosition = iota
	vp2
	vp3
)

// HistoryView manages the history view and it's child views
type HistoryView struct {
	channels             *Channels
	refView              WindowView
	commitView           WindowView
	diffView             WindowView
	gitStatusView        WindowView
	views                []WindowView
	viewWins             map[WindowView]*Window
	layout               map[viewPosition]WindowView
	activeViewPos        uint
	active               bool
	fullScreenActiveView bool
	orientation          viewOrientation
	lock                 sync.Mutex
}

type viewLayout struct {
	viewDimension ViewDimension
	startRow      uint
	startCol      uint
}

// NewHistoryView creates a new instance of the history view
func NewHistoryView(repoData RepoData, channels *Channels, config Config) *HistoryView {
	refView := NewRefView(repoData, channels)
	commitView := NewCommitView(repoData, channels)
	diffView := NewDiffView(repoData, channels)
	gitStatusView := NewGitStatusView(repoData, channels)

	refViewWin := NewWindow("refView", config)
	commitViewWin := NewWindow("commitView", config)
	diffViewWin := NewWindow("diffView", config)
	gitStatusViewWin := NewWindow("gitStatusView", config)

	refView.RegisterRefListener(commitView)
	commitView.RegisterCommitListner(diffView)

	historyView := &HistoryView{
		channels:      channels,
		refView:       refView,
		commitView:    commitView,
		diffView:      diffView,
		gitStatusView: gitStatusView,
		views:         []WindowView{refView, commitView, diffView},
		orientation:   voDefault,
		viewWins: map[WindowView]*Window{
			refView:       refViewWin,
			commitView:    commitViewWin,
			diffView:      diffViewWin,
			gitStatusView: gitStatusViewWin,
		},
		layout: map[viewPosition]WindowView{
			vp1: refView,
			vp2: commitView,
			vp3: diffView,
		},
		activeViewPos: 1,
	}

	commitView.RegisterCommitListner(historyView)
	commitView.RegisterStatusSelectedListener(historyView)

	return historyView
}

// Initialise sets up the history view and calls initialise on its child views
func (historyView *HistoryView) Initialise() (err error) {
	log.Info("Initialising HistoryView")

	if err = historyView.refView.Initialise(); err != nil {
		return
	}
	if err = historyView.commitView.Initialise(); err != nil {
		return
	}
	if err = historyView.diffView.Initialise(); err != nil {
		return
	}
	if err = historyView.gitStatusView.Initialise(); err != nil {
		return
	}

	return
}

// Render generates the history view and returns windows (one for each child view) representing the view as a whole
func (historyView *HistoryView) Render(viewDimension ViewDimension) (wins []*Window, err error) {
	log.Debug("Rendering HistoryView")
	historyView.lock.Lock()
	defer historyView.lock.Unlock()

	if historyView.fullScreenActiveView {
		return historyView.renderActiveViewFullScreen(viewDimension)
	}

	viewLayouts := historyView.determineViewDimensions(viewDimension)

	for vp, viewLayout := range viewLayouts {
		view := historyView.layout[vp]
		win := historyView.viewWins[view]
		win.Resize(viewLayout.viewDimension)
		win.Clear()
		win.SetPosition(viewLayout.startRow, viewLayout.startCol)

		if err = view.Render(win); err != nil {
			return
		}

		wins = append(wins, win)
	}

	return
}

func (historyView *HistoryView) determineViewDimensions(viewDimension ViewDimension) map[viewPosition]viewLayout {
	vp1Layout := viewLayout{viewDimension: viewDimension}
	vp2Layout := viewLayout{viewDimension: viewDimension}
	vp3Layout := viewLayout{viewDimension: viewDimension}

	vp1Layout.viewDimension.cols = Min(hvBranchViewWidth, viewDimension.cols/2)

	if historyView.orientation == voColumn {
		remainingCols := viewDimension.cols - vp1Layout.viewDimension.cols

		vp2Layout.viewDimension.cols = remainingCols / 2
		vp2Layout.startCol = vp1Layout.viewDimension.cols

		vp3Layout.viewDimension.cols = remainingCols - vp2Layout.viewDimension.cols
		vp3Layout.startCol = viewDimension.cols - vp3Layout.viewDimension.cols
	} else {
		vp2Layout.viewDimension.cols = viewDimension.cols - vp1Layout.viewDimension.cols
		vp2Layout.viewDimension.rows = viewDimension.rows / 2
		vp2Layout.startCol = vp1Layout.viewDimension.cols

		vp3Layout.viewDimension.cols = viewDimension.cols - vp1Layout.viewDimension.cols
		vp3Layout.viewDimension.rows = viewDimension.rows - vp2Layout.viewDimension.rows
		vp3Layout.startRow = vp2Layout.viewDimension.rows
		vp3Layout.startCol = vp1Layout.viewDimension.cols
	}

	return map[viewPosition]viewLayout{
		vp1: vp1Layout,
		vp2: vp2Layout,
		vp3: vp3Layout,
	}
}

func (historyView *HistoryView) renderActiveViewFullScreen(viewDimension ViewDimension) (wins []*Window, err error) {
	view := historyView.views[historyView.activeViewPos]
	win := historyView.viewWins[view]

	win.Resize(viewDimension)
	win.Clear()
	win.SetPosition(0, 0)

	if err = view.Render(win); err != nil {
		return
	}

	wins = append(wins, win)

	return
}

// RenderHelpBar renders key binding help info for the history view
func (historyView *HistoryView) RenderHelpBar(lineBuilder *LineBuilder) (err error) {
	RenderKeyBindingHelp(historyView.ViewID(), lineBuilder, []ActionMessage{
		{action: ActionNextView, message: "Next View"},
		{action: ActionPrevView, message: "Previous View"},
		{action: ActionFullScreenView, message: "Toggle Full Screen"},
		{action: ActionToggleViewLayout, message: "Toggle Layout"},
	})

	return
}

// HandleKeyPress passes the keypress onto the active child view
func (historyView *HistoryView) HandleKeyPress(keystring string) (err error) {
	log.Debugf("HistoryView handling keys %v", keystring)
	activeChildView := historyView.ActiveView()
	return activeChildView.HandleKeyPress(keystring)
}

// HandleAction handles the provided action if the history view supports it or passes it down to the active child view
func (historyView *HistoryView) HandleAction(action Action) (err error) {
	log.Debugf("HistoryView handling action %v", action)

	switch action.ActionType {
	case ActionNextView:
		historyView.lock.Lock()
		historyView.activeViewPos++
		historyView.activeViewPos %= uint(len(historyView.views))
		historyView.lock.Unlock()
		historyView.OnActiveChange(true)
		historyView.channels.UpdateDisplay()
		return
	case ActionPrevView:
		historyView.lock.Lock()

		if historyView.activeViewPos == 0 {
			historyView.activeViewPos = uint(len(historyView.views)) - 1
		} else {
			historyView.activeViewPos--
		}

		historyView.lock.Unlock()
		historyView.OnActiveChange(true)
		historyView.channels.UpdateDisplay()
		return
	case ActionFullScreenView:
		historyView.lock.Lock()
		defer historyView.lock.Unlock()

		historyView.fullScreenActiveView = !historyView.fullScreenActiveView
		historyView.channels.UpdateDisplay()
		return
	case ActionToggleViewLayout:
		historyView.lock.Lock()
		defer historyView.lock.Unlock()

		historyView.orientation = (historyView.orientation + 1) % voCount
		historyView.channels.UpdateDisplay()
		return
	}

	activeChildView := historyView.ActiveView()
	return activeChildView.HandleAction(action)
}

// OnActiveChange updates whether this view (and it's active child view) are active
func (historyView *HistoryView) OnActiveChange(active bool) {
	log.Debugf("History active set to %v", active)
	historyView.lock.Lock()
	defer historyView.lock.Unlock()

	historyView.active = active

	for viewPos := uint(0); viewPos < uint(len(historyView.views)); viewPos++ {
		if viewPos == historyView.activeViewPos {
			historyView.views[viewPos].OnActiveChange(active)
		} else {
			historyView.views[viewPos].OnActiveChange(false)
		}
	}
}

// ViewID returns the view ID for the history view
func (historyView *HistoryView) ViewID() ViewID {
	return ViewHistory
}

// ActiveView returns the active child view
func (historyView *HistoryView) ActiveView() AbstractView {
	historyView.lock.Lock()
	defer historyView.lock.Unlock()

	return historyView.views[historyView.activeViewPos]
}

// OnCommitSelect ensures the diff view is visible
func (historyView *HistoryView) OnCommitSelect(commit *Commit) (err error) {
	historyView.lock.Lock()
	defer historyView.lock.Unlock()

	log.Debugf("Replacing GitStatusView with DiffView")
	historyView.setChildViewAtPosition(historyView.diffView, vp3)

	return
}

// OnStatusSelected ensures the git status view is visible
func (historyView *HistoryView) OnStatusSelected(status *Status) (err error) {
	historyView.lock.Lock()
	defer historyView.lock.Unlock()

	log.Debugf("Replacing DiffView with GitStatusView")
	historyView.setChildViewAtPosition(historyView.gitStatusView, vp3)

	return
}

func (historyView *HistoryView) setChildViewAtPosition(view WindowView, vp viewPosition) {
	if historyView.views[vp] != view {
		historyView.views[vp].OnActiveChange(false)

		historyView.views[vp] = view
		historyView.layout[vp] = view

		view.OnActiveChange(historyView.activeViewPos == uint(vp))

		historyView.channels.UpdateDisplay()
	}
}

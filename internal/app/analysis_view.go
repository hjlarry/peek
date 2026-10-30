package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func (a *app) analysisPane(c *ui.Context) {
	t := c.Theme()
	run := a.analysis.run
	ui.Column(c.Key("analysis-pane")).Fill().MinWidth(0).Children(func() {
		ui.Column(c).FillWidth().Shrink(0).Gap(t.Space(2)).Padding(t.Space(3), t.Space(4)).Children(func() {
			if run == nil {
				ui.Text(c, "Group these changes by feature.").FontSize(smallSize).TextColor(t.TextMuted).FillWidth()
				a.analysisStartButton(c, "Analyze Features")
				return
			}
			if run.running {
				phase := run.phase
				if phase == "" {
					phase = "Grouping changes…"
				}
				ui.Row(c).FillWidth().Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(12, 12).Shrink(0)
					ui.Text(c, phase).FontSize(smallSize).Grow(1).MinWidth(0)
				})
				ui.Button(c.Key("analysis-cancel"), "Cancel Analysis").OnClick(run.stop)
			} else {
				a.analysisStartButton(c, "Reanalyze")
			}
			if run.err != "" && (!run.stale || run.err != "Changes have changed. Reanalyze to update the groups.") {
				ui.Text(c, run.err).FontSize(smallSize).TextColor(system(c, systemOrange)).Selectable().FillWidth()
			}
			if run.stale {
				ui.Text(c, "Changes have changed. Reanalyze to update the groups.").FontSize(smallSize).TextColor(system(c, systemOrange)).FillWidth()
			} else if run.result != nil && !a.analysisMatches(run) {
				ui.Text(c, "These changes have not been analyzed.").FontSize(smallSize).TextColor(t.TextMuted).FillWidth()
			}
		})
		ui.Scroll(c.Key("analysis-scroll")).Grow(1).FillWidth().MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Gap(t.Space(1)).Padding(0, t.Space(2), t.Space(3), t.Space(2)).Children(func() {
				matches := a.analysisMatches(run)
				a.analysisGroupRow(c, -1, "All Changes", len(a.diffs), a.analysis.selected < 0 || !matches, true)
				if run == nil || run.result == nil {
					return
				}
				for i, item := range run.result.Items {
					selected := matches && a.analysis.selected == i
					a.analysisGroupRow(c, i, item.Title, len(item.Files), selected, matches)
					if !selected {
						continue
					}
					for _, file := range item.Files {
						ui.ButtonBase(c.Key("analysis-file:"+file)).FillWidth().MinWidth(0).
							Padding(t.Space(1), t.Space(2), t.Space(1), t.Space(4)).Radius(t.Radius).
							Label(file).Tooltip(file).OnClick(func() { a.selectAnalysisFile(file) }).Children(func() {
							ui.Text(c, file).FontSize(smallSize).TextColor(t.TextMuted).SingleLine().Ellipsis("…").FillWidth().MinWidth(0)
						})
					}
				}
			})
		})
	})
}

func (a *app) analysisStartButton(c *ui.Context, label string) {
	_, source, _ := a.answerable()
	ui.Button(c.Key("analysis-start"), label).Disabled(source == "" || a.codexRunning() || a.changing()).OnClick(a.startAnalysis)
}

func (a *app) analysisGroupRow(c *ui.Context, index int, title string, files int, selected, enabled bool) {
	t := c.Theme()
	text, muted := t.Text, t.TextMuted
	b := ui.ButtonBase(c.Key(fmt.Sprintf("analysis-group-%d", index))).FillWidth().MinWidth(0).
		Padding(t.Space(2)).Radius(t.Radius).Label(title).Tooltip(title).Selected(selected).Disabled(!enabled).
		OnClick(func() { a.selectAnalysisItem(index) })
	if selected {
		b.Background(t.Accent)
		text, muted = t.AccentText, t.AccentText.Alpha(0.75)
	}
	b.Children(func() {
		ui.Row(c).FillWidth().Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(smallSize).TextColor(text).Grow(1).MinWidth(0)
			ui.Text(c, fmt.Sprintf("%d", files)).FontSize(smallSize).TextColor(muted).Shrink(0)
		})
	})
}

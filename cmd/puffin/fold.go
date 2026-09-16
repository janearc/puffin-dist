package main

import (
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"strings"
)

// The cluster screen's hierarchy. A real cluster does not fit on a screen,
// and a list that runs off the top is a list nobody can read: kube-system is
// a dozen pods you are not looking at while you watch one service come back.
//
// So the pods fold, along the two hierarchies their names already carry -- the
// namespace they live in, and the prefix they share. The rule for the second:
// pods that are `word-`, and there are more than two of them, fold into
// `word-`.
//
// Two is left alone deliberately; a fold that saves one line costs more
// keystrokes than it returns.

type rowKind int

const (
	rowNS rowKind = iota
	rowGroup
	rowPod
)

// kubeRow is one line on the cluster screen: a namespace, a folded group, or
// a pod. The screen renders rows and the cursor indexes rows, so what folds
// away is genuinely gone -- not merely hidden from view while still counting
// against j and k.
type kubeRow struct {
	Kind  rowKind
	Key   string // fold key: "ns" for a namespace, "ns/prefix" for a group
	NS    string
	Label string
	Pod   *KubePod // rowPod only
	Count int      // rowNS and rowGroup: pods underneath
	// up, waiting, broken, failed, done -- every pod is in one
	Health [5]int
	Folded bool
	Depth  int
}

// Foldable says whether space does anything on this row.
func (r kubeRow) Foldable() bool { return r.Kind != rowPod }

// foldPrefix is the pod name up to its first dash. Empty when the name has
// no dash: a pod called `kafka` groups with nothing.
func foldPrefix(name string) string {
	if i := strings.Index(name, "-"); i > 0 {
		return name[:i]
	}
	return ""
}

// The pod states the screen counts. Succeeded and Failed are BOTH terminal
// and they are not the same news: a cronjob whose last three runs completed
// is fine, and one whose last three runs errored is a finding.
//
// The first cut folded them together as "finished, not failing" -- which is
// true of Succeeded and false of Failed -- and the result was three failed
// kestrel jobs sitting invisibly behind a fold that reported nothing at all
// about them. That is precisely the rule the fold is supposed to keep.
const (
	podUp = iota
	podWaiting
	podBroken
	podFailed // ran and failed: terminal, and still a finding
	podDone   // ran and succeeded: terminal, and not news
)

// podHealth ranks a pod so a fold can carry the worst thing under it. A
// header that hides a broken pod without saying so is the failure this
// ordering exists to prevent.
func podHealth(p KubePod) int {
	switch {
	case p.Phase == "Failed":
		return podFailed
	case p.Terminal:
		return podDone
	case p.Phase == "Running" && p.AllReady:
		return podUp
	case p.Phase == "Pending" || !p.AllReady:
		return podWaiting
	default:
		return podBroken
	}
}

// kubeRows flattens the pods into the rows the screen shows, honouring the
// fold set. Pods arrive sorted by namespace then name (parsePods sorts), so
// same-prefix pods are contiguous and one walk finds every group.
func kubeRows(pods []KubePod, folded map[string]bool) []kubeRow {
	var rows []kubeRow
	for i := 0; i < len(pods); {
		ns := pods[i].Namespace
		j := i
		for j < len(pods) && pods[j].Namespace == ns {
			j++
		}
		nsPods := pods[i:j]
		rows = append(rows, kubeRow{
			Kind: rowNS, Key: ns, NS: ns, Label: ns,
			Count: len(nsPods), Health: rollup(nsPods),
			Folded: folded[ns], Depth: 0,
		})
		if !folded[ns] {
			rows = append(rows, groupRows(nsPods, folded)...)
		}
		i = j
	}
	return rows
}

// groupRows folds one namespace's pods by shared prefix.
func groupRows(pods []KubePod, folded map[string]bool) []kubeRow {
	var rows []kubeRow
	for i := 0; i < len(pods); {
		pre := foldPrefix(pods[i].Name)
		j := i
		if pre != "" {
			for j < len(pods) && foldPrefix(pods[j].Name) == pre {
				j++
			}
		} else {
			j = i + 1
		}
		run := pods[i:j]
		if len(run) > 2 {
			key := pods[i].Namespace + "/" + pre
			rows = append(rows, kubeRow{
				Kind:  rowGroup,
				Key:   key,
				NS:    pods[i].Namespace,
				Label: pre + "-",
				Count: len(run), Health: rollup(run),
				Folded: folded[key], Depth: 1,
			})
			if !folded[key] {
				rows = append(rows, podRows(run, 2)...)
			}
		} else {
			rows = append(rows, podRows(run, 1)...)
		}
		i = j
	}
	return rows
}

// podRows flattens the namespace and prefix hierarchy into the rows the
// screen scrolls, so the cursor indexes what is drawn rather than what
// exists.
func podRows(pods []KubePod, depth int) []kubeRow {
	rows := make([]kubeRow, 0, len(pods))
	for k := range pods {
		p := pods[k]
		rows = append(rows, kubeRow{
			Kind:  rowPod,
			Key:   p.Namespace + "/" + p.Name,
			NS:    p.Namespace,
			Label: p.Name, Pod: &p, Count: 1, Depth: depth,
		})
	}
	return rows
}

// rollup counts every pod under a header into exactly one bucket. Every
// pod, because a header that accounts for four of six leaves the other two
// to the reader's imagination -- which is what "kestrel- 6" with no states
// after it actually did.
func rollup(pods []KubePod) [5]int {
	var h [5]int
	for _, p := range pods {
		h[podHealth(p)]++
	}
	return h
}

// defaultFolds is what the screen opens as: every prefix group of more than
// two folded, every namespace open. The groups are the noise -- six replicas
// of one deployment say one thing six times -- and a folded namespace hides
// the thing you came to look at.
func defaultFolds(pods []KubePod) map[string]bool {
	f := map[string]bool{}
	for _, r := range kubeRows(pods, map[string]bool{}) {
		if r.Kind == rowGroup {
			f[r.Key] = true
		}
	}
	return f
}

// parentKey is the row that h collapses into: a pod's group (or namespace),
// a group's namespace. A namespace has no parent and collapses itself.
func parentKey(rows []kubeRow, i int) string {
	if i < 0 || i >= len(rows) {
		return ""
	}
	for j := i - 1; j >= 0; j-- {
		if rows[j].Depth < rows[i].Depth {
			return rows[j].Key
		}
	}
	return rows[i].Key
}

// windowOffset keeps the cursor on screen and otherwise leaves the list
// alone: the offset moves only when the cursor would walk off an edge, so
// the rows do not swim under a cursor that is merely moving one line.
func windowOffset(n, cursor, size, offset int) int {
	if size <= 0 || n <= size {
		return 0
	}
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+size {
		offset = cursor - size + 1
	}
	if offset > n-size {
		offset = n - size
	}
	if offset < 0 {
		offset = 0
	}
	return offset
}

// kubeVisible is the cluster screen's rows under the current fold set.
// Cheap enough to recompute on every keystroke -- it is a walk over a few
// hundred pods -- and recomputing means the fold set is the only state.
func (m model) kubeVisible() []kubeRow {
	return kubeRows(
		m.kube.Pods,
		m.kubeFold,
	)
}

// kubeWindow is how many rows fit under the header and above the help. A
// zero height is a test or a terminal that has not reported yet: show
// everything rather than nothing.
func (m model) kubeWindow() int {
	if m.height <= 0 {
		return 1 << 30
	}
	if n := m.height - 11; n > 3 {
		return n
	}
	return 3
}

// kubePodAt resolves the cursor to a pod, or says it is not on one. The
// lifecycle keys need a pod; a namespace header is not a thing to restart.
func (m model) kubePodAt(i int) (KubePod, bool) {
	rows := m.kubeVisible()
	if i < 0 || i >= len(rows) || rows[i].Kind != rowPod {
		return KubePod{}, false
	}
	return *rows[i].Pod, true
}

// rowIndex finds a fold key in the rows, or 0 when it has gone away.
func rowIndex(rows []kubeRow, key string) int {
	for i, r := range rows {
		if r.Key == key {
			return i
		}
	}
	return 0
}

// firstPodRow is where the cursor lands when the screen opens. A namespace
// header is a fine thing to sit on, but it is not a thing you can restart,
// and an operator arriving on the cluster screen came for a pod.
func firstPodRow(rows []kubeRow) int {
	for i, r := range rows {
		if r.Kind == rowPod {
			return i
		}
	}
	return 0
}

// The contract screen has the same problem the cluster screen had, arriving
// from the other direction: kingfisher publishes enough methods to run off
// the top of a terminal, and the services growing here only get larger. The
// rows are flattened the same way so one window helper serves both.

type detailRow struct {
	Service string // set on a service header
	Method  APIMethod
	Index   int // method index, -1 on a header
}

// detailRows flattens services and methods into renderable lines.
func detailRows(api *API) []detailRow {
	if api == nil {
		return nil
	}
	var rows []detailRow
	i := 0
	for _, svc := range api.Services {
		rows = append(rows, detailRow{Service: svc.Name, Index: -1})
		for _, meth := range svc.Methods {
			rows = append(rows, detailRow{Method: meth, Index: i})
			i++
		}
	}
	return rows
}

// detailRowOf finds the row holding a method, so the window follows the
// cursor rather than the other way round.
func detailRowOf(rows []detailRow, method int) int {
	for i, r := range rows {
		if r.Index == method {
			return i
		}
	}
	return 0
}

// detailWindow is how many contract lines fit above the example packet.
func (m model) detailWindow() int {
	if m.height <= 0 {
		return 1 << 30
	}
	if n := m.height - 18; n > 4 {
		return n
	}
	return 4
}

// scrollbar draws the track down the right of a windowed list.
//
// A window with no bar is a list that silently ends: the contract screen
// showed forty methods as fifteen with nothing to say the other twenty-five
// existed, and "scrolling doesn't work" is what that looks like from the
// outside even when it does.
//
// It returns one string per visible row, so a caller appends one to each
// line it draws. Nothing is drawn when nothing is hidden -- a track that
// cannot move is furniture.
func scrollbar(total, shown, off int) []string {
	out := make([]string, shown)
	if total <= shown || shown <= 0 {
		for i := range out {
			out[i] = " "
		}
		return out
	}
	thumb := maxInt(1, shown*shown/total)
	pos := 0
	if total > shown {
		pos = off * (shown - thumb) / (total - shown)
	}
	for i := range out {
		if i >= pos && i < pos+thumb {
			out[i] = "\u2588"
		} else {
			out[i] = "\u2502"
		}
	}
	return out
}

// scrollNote says what is off the ends in words, for the screens where a
// bar would crowd the columns. Numbers, because "more below" does not tell
// you whether it is two more or two hundred.
func scrollNote(total, shown, off int) string {
	if total <= shown {
		return ""
	}
	above, below := off, total-off-shown
	switch {
	case above > 0 && below > 0:
		return fmt.Sprintf("%d above · %d below", above, below)
	case above > 0:
		return fmt.Sprintf("%d above", above)
	default:
		return fmt.Sprintf("%d below", below)
	}
}

// trackAt puts a scrollbar cell in a fixed column, whatever the row did.
//
// The bar used to be appended after padding to a column, which works only while
// every row is shorter than that column.
//
// The cursor row is not: it carries "space folds", and a long pod name carries
// itself, so those rows pushed the track right and the bar came out as two
// ragged columns of blocks rather than one line down the edge, which reads as a
// rendering fault rather than as a scrollbar.
//
// A row that would reach the track is truncated instead. The bar is a
// position indicator and the last few characters of an over-long row are
// not worth trading it for -- and a bar that wanders is not an indicator of
// anything.
func trackAt(line string, col int, track string) string {
	w := ansi.StringWidth(line)
	if w > col {
		line = ansi.Truncate(line, col, "")
		w = ansi.StringWidth(line)
	}
	return line + strings.Repeat(" ", maxInt(0, col-w)) + track
}

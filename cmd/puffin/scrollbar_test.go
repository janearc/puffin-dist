package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The scrollbar has to stand in ONE column.
//
// It did not.
//
// Pod rows appended the track after their own padding, landing at 77; header
// rows went through trackAt at 74. The result was two ragged columns of blocks
// down the right of the cluster screen.
//
// trackAt was written to fix exactly this once before -- its own comment says
// so -- and the repair reached the header rows only.
//
// So the test is not "the constant is 77". It is "every row agrees", which
// is the property that was actually violated and the one that a third call
// site added next year would violate again.

// trackColumnsIn returns the visible column each scrollbar cell was drawn
// at, one per row that has one.
func trackColumnsIn(frame string) []int {
	var cols []int
	for _, line := range strings.Split(frame, "\n") {
		plain := ansi.Strip(line)
		i := strings.LastIndexAny(plain, "█│")
		if i < 0 {
			continue
		}
		cols = append(cols, ansi.StringWidth(plain[:i]))
	}
	return cols
}

// TestClusterScrollbarStandsInOneColumn renders a cluster with both kinds
// of row -- namespace headers and pods -- and enough of them to force the
// window to scroll, which is the only condition under which a track is
// drawn at all.
func TestClusterScrollbarStandsInOneColumn(t *testing.T) {
	var pods []KubePod
	for _, ns := range []string{"local", "kube-system", "flipr"} {
		for i := 0; i < 12; i++ {
			pods = append(pods, KubePod{
				Namespace: ns,
				Name: strings.Repeat(
					"x",
					10+i,
				) + "-" + string(
					rune('a'+i),
				),
				Phase: "Running", Ready: "1/1", AllReady: true,
				Restarts: i, Age: "3d",
			})
		}
	}

	m := newModel("test", "")
	m.width, m.height = 160, 40
	m.scr = screenKube
	m.kube = KubeView{Context: homeContext(), Pods: pods}

	frame := kubeView(m)
	cols := trackColumnsIn(frame)
	if len(cols) < 5 {
		t.Fatalf(
			"only %d rows carried a scrollbar cell; the fixture "+
				"did not scroll",
			len(cols),
		)
	}
	first := cols[0]
	for i, c := range cols {
		if c != first {
			t.Fatalf(
				"scrollbar wanders: row 0 draws at column "+
					"%d, row %d at column %d.\n"+
					"That is the two-ragged-columns bug "+
					"returning; every row must "+
					"go through trackAt.",
				first,
				i,
				c,
			)
		}
	}
}

// TestClusterScrollbarSurvivesALongPodName is the specific input that
// pushed the track sideways: a row longer than the track column. It must be
// truncated rather than allowed to shove the bar right, because a bar that
// moves is not an indicator of anything.
func TestClusterScrollbarSurvivesALongPodName(t *testing.T) {
	var pods []KubePod
	for i := 0; i < 30; i++ {
		pods = append(pods, KubePod{
			Namespace: "local",
			Name:      strings.Repeat("very-long-pod-name-", 5),
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
			Age:       "1d",
		})
	}
	m := newModel("test", "")
	m.width, m.height = 160, 30
	m.scr = screenKube
	m.kube = KubeView{Context: homeContext(), Pods: pods}

	cols := trackColumnsIn(kubeView(m))
	if len(cols) == 0 {
		t.Fatal("no scrollbar drawn")
	}
	for i, c := range cols {
		if c != cols[0] {
			t.Fatalf(
				"a long pod name moved the track: row 0 at "+
					"%d, row %d at %d",
				cols[0],
				i,
				c,
			)
		}
	}
}

// TestTrackAtTruncatesRatherThanShoves pins the helper itself.
func TestTrackAtTruncatesRatherThanShoves(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := trackAt(long, 20, "|")
	if w := ansi.StringWidth(ansi.Strip(got)); w != 21 {
		t.Errorf("width %d, want 21 (20 plus the track)", w)
	}
	short := trackAt("ab", 20, "|")
	if w := ansi.StringWidth(ansi.Strip(short)); w != 21 {
		t.Errorf("short row width %d, want 21", w)
	}
}

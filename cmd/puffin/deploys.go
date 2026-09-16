package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The deploys pane: who is running which build, and whether anyone agrees.
//
// The first cut of this compared the running image against flipr, on the belief
// that flipr's newest namespace per service was keyed by a deploy commit. It is
// not. Flipr's namespace versions are flag namespaces -- v1, main, dev,
// dodo-dev, and a bare underscore for services that have never had one.
//
// Comparing an image tag to "v1" and reporting the result was a whole column of
// noise dressed as a finding.
//
// The service itself knows. Every member answers /health with its own build:
// flipr says 435fd50, gaggle says f9cc8c1. That is the binary reporting what
// it actually is, from inside the container, and it is the only source here
// that cannot be stale about itself.
//
// So the comparison is between what the binary says it is and what image the
// cluster says it is running. They disagree when a tag was moved without a
// redeploy, when a rollout is half done, or when two replicas are on
// different builds -- and each of those is worth seeing.
//
// One thing makes this harder than it sounds: an image tagged `:dev` carries no
// build id at all.
//
// So there are three answers and not two. "agrees", "disagrees" and "the tag
// cannot say" are different facts, and the third one is a finding about the
// build pipeline rather than a gap to be papered over with a hopeful green.

// AgreeState is what the reconciliation concluded.
type AgreeState int

const (
	// the tag carries no build id to compare
	AgreeUnknowable AgreeState = iota
	AgreeYes
	AgreeNo
)

// Deploy is one workload's deployed identity, reconciled against flipr.
type Deploy struct {
	Service   string
	Namespace string
	Image     string // repository:tag, as the pod declares it
	Tag       string
	// sha256:... from the running container, the only hard fact
	Digest   string
	BuildID  string // a commit-shaped token in the tag, "" when it has none
	Pods     int
	Ready    int
	Reported string // the version the service reports from /health
	Stamped  string // a commit stamped into the pod spec's environment
	// The flag namespace: which scope of flipr this service reads. A
	// service without a namespace, or with the wrong one, is a process
	// reading nobody's flags.
	//
	// So this is a configuration check, nothing to do with builds -- the
	// mistake this pane made when it treated a namespace as a commit.
	UsesFlipr  bool   // the pod points at flipr at all
	DeclaredNS string // the namespace its environment names, if any
	FliprNS    string // the namespace flipr actually holds for it
	NSState    string // ok | undeclared | missing | placeholder | mismatch
	Health     string // whether it answered at all
	Agree      AgreeState
	SharedWith []string // other services running the identical digest
}

// DeployView is the pane's data.
type DeployView struct {
	Context  string
	Rows     []Deploy
	Warnings []string
}

// buildID finds a commit-shaped token: flipr's namespaces are short hex.
// A scanner rather than a pattern -- "is every character in this run a lower
// hex digit, and is the run between 7 and 40 long" is a sentence, and this
// is that sentence.
func buildID(s string) string {
	i := 0
	for i < len(s) {
		if !isHexDigit(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isHexDigit(s[j]) {
			j++
		}
		// the run must be the whole token: 8080 inside a port is not a
		// commit, and neither is the hex half of a longer word
		startOK := i == 0 || !isWordByte(s[i-1])
		endOK := j == len(s) || !isWordByte(s[j])
		if startOK && endOK && j-i >= 7 && j-i <= 40 {
			return s[i:j]
		}
		i = j
	}
	return ""
}

// isHexDigit is how a build id is recognised without a regex: a tag that
// is all hex of the right length is a commit, and anything else is a name.
func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
}

// isWordByte marks the bytes a tag may carry inside a word, so a scan can
// find where one ends without a pattern language.
func isWordByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' ||
		c >= 'A' && c <= 'Z' ||
		c == '_'
}

// fetchDeploys reconciles the cluster against flipr. Either source may fail
// on its own; a failure is a warning and the other half still renders,
// because "flipr is down" and "nothing is deployed" are different answers.
func fetchDeploys(kubeCtx, domain string) DeployView {
	v := DeployView{Context: kubeCtx}
	out, err := exec.Command("kubectl", "--context", kubeCtx,
		"get", "pods", "-A", "-o", "json").Output()
	if err != nil {
		v.Warnings = append(v.Warnings, "kubectl: "+err.Error())
		return v
	}
	rows, perr := reduceDeploys(out)
	if perr != "" {
		v.Warnings = append(v.Warnings, perr)
	}
	// what flipr actually holds, per service. Read directly rather than
	// through the flags reducer: this needs every namespace a service has,
	// not the newest one.
	held, ferr := namespacesHeld(fmt.Sprintf("http://flipr.%s", domain))
	if ferr != nil {
		v.Warnings = append(v.Warnings, "flipr: "+ferr.Error())
	}
	for i := range rows {
		rows[i].FliprNS, rows[i].NSState = checkNamespace(rows[i], held)
	}

	// ask each service what it thinks it is. By name, one at a time, and
	// only for services that have a route -- a daemon with no ingress
	// cannot be asked and says so rather than being reported as silent.
	for i := range rows {
		rows[i].Reported, rows[i].Health = askVersion(
			rows[i].Service,
			domain,
		)
		rows[i].Agree = reconcile(rows[i].BuildID, rows[i].Reported)
	}
	v.Rows = rows
	return v
}

// askVersion reads a service's own /health and returns the build it claims.
func askVersion(service, domain string) (string, string) {
	resp, err := client.Get(
		fmt.Sprintf("http://%s.%s/health", service, domain),
	)
	if err != nil {
		return "", "no route"
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Sprintf("http %d", resp.StatusCode)
	}
	var h struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&h) != nil {
		return "", "unreadable"
	}
	if h.Version == "" {
		return "", "no version"
	}
	return h.Version, "ok"
}

// reconcile is deliberately three-valued, and the rule binds BOTH sources. An
// image tag with no build id in it cannot agree or disagree with anything --
// and neither can a flipr namespace that is not a build id either.
//
// The live estate has hm on namespace `_` and peacock on `peacock-dev`;
// comparing those to a commit and shouting disagrees is the same error as a
// hopeful green, just louder. A false alarm on this screen costs more than a
// blank, because the screen exists to be believed.
func reconcile(imageBuild, reported string) AgreeState {
	if imageBuild == "" || reported == "" {
		return AgreeUnknowable
	}
	if buildID(reported) == "" {
		return AgreeUnknowable
	}
	if strings.HasPrefix(imageBuild, reported) ||
		strings.HasPrefix(reported, imageBuild) {
		return AgreeYes
	}
	return AgreeNo
}

// reduceDeploys turns kubectl's pod JSON into one row per service per
// namespace, folding replicas together: six pods of one deployment are one
// deployed thing, and listing them six times is how the real answer hides.
func reduceDeploys(raw []byte) ([]Deploy, string) {
	var doc struct {
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Env []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
					// kept for the flipr configuration
					// check below
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				ContainerStatuses []struct {
					Ready   bool   `json:"ready"`
					Image   string `json:"image"`
					ImageID string `json:"imageID"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, "unreadable pod json: " + err.Error()
	}
	byKey := map[string]*Deploy{}
	for _, it := range doc.Items {
		name := it.Metadata.Labels["app.kubernetes.io/name"]
		if name == "" {
			name = it.Metadata.Labels["app"]
		}
		if name == "" {
			name = foldPrefix(it.Metadata.Name)
		}
		if name == "" {
			name = it.Metadata.Name
		}
		if len(it.Status.ContainerStatuses) == 0 {
			continue
		}
		// the first container is the service; sidecars are not the
		// deploy
		c := it.Status.ContainerStatuses[0]
		stamped, usesFlipr, declared := "", false, ""
		if len(it.Spec.Containers) > 0 {
			env := it.Spec.Containers[0].Env
			stamped = buildRev(env)
			for _, e := range env {
				switch {
				case strings.HasSuffix(e.Name, "_FLIPR_URL"):
					usesFlipr = true
				case strings.HasSuffix(
					e.Name,
					"_FLIPR_VERSION",
				):
					declared = e.Value
				case strings.HasSuffix(
					e.Name,
					"_VERSION",
				) && buildID(e.Value) == "" &&
					e.Value != "" && e.Value != "None" &&
					declared == "":
					// PEACOCK_VERSION is a namespace;
					//
					// the suffix is not a convention anyone
					// agreed on, so it is accepted only
					// when it is NOT commit-shaped and
					// nothing better said
					declared = e.Value
				}
			}
		}
		key := it.Metadata.Namespace + "/" + name
		d, ok := byKey[key]
		if !ok {
			tag := imageTag(c.Image)
			d = &Deploy{
				Service: name, Namespace: it.Metadata.Namespace,
				Image:   c.Image,
				Tag:     tag,
				Digest:  shortDigest(c.ImageID),
				BuildID: buildID(tag), Stamped: stamped,
				UsesFlipr: usesFlipr, DeclaredNS: declared,
			}
			// a commit stamped into the manifest beats a tag that
			// says nothing: it is the same fact, recorded where
			// every language in the estate can be read from without
			// running any of them
			if d.BuildID == "" {
				d.BuildID = stamped
			}
			byKey[key] = d
		}
		d.Pods++
		if c.Ready {
			d.Ready++
		}
	}
	rows := make([]Deploy, 0, len(byKey))
	for _, d := range byKey {
		rows = append(rows, *d)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Service < rows[j].Service
	})
	// two services on one digest is the same binary wearing two names --
	// worth saying, because a redeploy of one is a redeploy of both
	same := map[string][]string{}
	for _, d := range rows {
		if d.Digest != "" {
			same[d.Digest] = append(same[d.Digest], d.Service)
		}
	}
	for i, d := range rows {
		for _, other := range same[d.Digest] {
			if other != d.Service {
				rows[i].SharedWith = append(
					rows[i].SharedWith,
					other,
				)
			}
		}
	}
	return rows, ""
}

// imageTag is the tag half of repo:tag, digest-pinned images included.
func imageTag(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	slash := strings.LastIndex(image, "/")
	if c := strings.LastIndex(image, ":"); c > slash {
		return image[c+1:]
	}
	// an untagged image is :latest, and that is worth seeing
	return "latest"
}

// shortDigest keeps enough hex to compare by eye.
func shortDigest(imageID string) string {
	i := strings.Index(imageID, "sha256:")
	if i < 0 {
		return ""
	}
	d := imageID[i+len("sha256:"):]
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

// deployFetched carries the reconciliation back into the loop.
type deployFetched struct{ v DeployView }

// deployPane is the pane.
type deployPane struct {
	v      DeployView
	cursor int
	offset int
	sortBy int
	filter string // show one tag only: ":dev" is most of the estate
	sel    string // the service under the cursor, so sorting keeps it
}

// deploySorts are the orders worth having. Service first because it is the
// one you scan for a name; tag second, because "show me everything on
// :dev" is the question being asked; then the verdict, which puts the
// wrong builds at the top where they belong.
var deploySorts = []struct {
	name string
	less func(a, b Deploy) bool
}{
	{"service", func(a, b Deploy) bool { return a.Service < b.Service }},
	{"tag", func(a, b Deploy) bool { return a.Tag < b.Tag }},
	{
		"is it the build flipr recorded?",
		func(a, b Deploy) bool { return a.Agree > b.Agree },
	},
	{"pods", func(a, b Deploy) bool { return a.Pods > b.Pods }},
}

// rows is the list after filtering. Everything that indexes the list goes
// through it, so a filter cannot leave the cursor on a row nobody can see.
func (p *deployPane) rows() []Deploy {
	if p.filter == "" {
		return p.v.Rows
	}
	out := make([]Deploy, 0, len(p.v.Rows))
	for _, d := range p.v.Rows {
		if d.Tag == p.filter {
			out = append(out, d)
		}
	}
	return out
}

// tags are the distinct tags in play, in a stable order, so f cycles the
// same way every time.
func (p *deployPane) tags() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range p.v.Rows {
		if !seen[d.Tag] {
			seen[d.Tag] = true
			out = append(out, d.Tag)
		}
	}
	sort.Strings(out)
	return out
}

// sortRows orders the list and keeps the cursor on its service. Ties fall
// back to namespace and name so the order is total: a self-refreshing screen
// whose tied rows swap places is unreadable.
func (p *deployPane) sortRows() {
	srt := deploySorts[p.sortBy%len(deploySorts)]
	sort.SliceStable(p.v.Rows, func(i, j int) bool {
		a, b := p.v.Rows[i], p.v.Rows[j]
		if srt.less(a, b) != srt.less(b, a) {
			return srt.less(a, b)
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Service < b.Service
	})
	if p.sel != "" {
		for i, d := range p.rows() {
			if d.Service == p.sel {
				p.cursor = i
				return
			}
		}
	}
	if n := len(p.rows()); p.cursor >= n {
		p.cursor = maxInt(0, n-1)
	}
}

// Key opens the deploys pane from the roster.
func (p *deployPane) Key() string { return "d" }

// Title names the pane, and keys its refresh gate.
func (p *deployPane) Title() string { return "deploys" }

// Help is the footer: the sorts, and how to read a verdict.
func (p *deployPane) Help() string {
	return "j/k: move · f: filter by tag · s: sort · r: refresh · q: " +
		"back\n" +
		"a service is confirmed only when the running image and " +
		"flipr name the same commit -- a :dev tag names " +
		"none, so most cannot be checked"
}

// Load asks each service what build it thinks it is and asks the cluster
// what image it is running. The domain decides which names are probed.
func (p *deployPane) Load(domain string) tea.Cmd {
	ctx, base := kubeContext(), fmt.Sprintf("http://flipr.%s", domain)
	return func() tea.Msg { return deployFetched{fetchDeploys(ctx, base)} }
}

// Update folds in an answer or a sort key. Sorting is over the rows in
// hand, never a refetch: the data has not changed, only the order.
func (p *deployPane) Update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case deployFetched:
		p.v = msg.v
		p.sortRows()
		if n := len(p.rows()); p.cursor >= n {
			p.cursor = max(0, n-1)
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "down", "j":
			if p.cursor < len(p.rows())-1 {
				p.cursor++
			}
			p.sel = p.at()
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
			p.sel = p.at()
		case "s":
			p.sortBy = (p.sortBy + 1) % len(deploySorts)
			p.sortRows()
		case "f":
			// cycle: everything, then each tag in turn. Most of
			// this estate is on :dev, so "just the dev ones" is one
			// keystroke.
			tags := p.tags()
			at := -1
			for i, t := range tags {
				if t == p.filter {
					at = i
				}
			}
			if at+1 >= len(tags) {
				p.filter = ""
			} else {
				p.filter = tags[at+1]
			}
			p.cursor = 0
			p.sel = p.at()
		case "esc":
			p.filter, p.cursor = "", 0
			p.sel = p.at()
		}
	}
	return p, nil
}

// at is the service under the cursor.
func (p *deployPane) at() string {
	r := p.rows()
	if p.cursor < 0 || p.cursor >= len(r) {
		return ""
	}
	return r[p.cursor].Service
}

// HandlesEsc claims esc while a filter is on, so it clears the filter rather
// than leaving the pane.
func (p *deployPane) HandlesEsc() bool { return p.filter != "" }

// View draws the three-valued verdict column. "cannot say" is drawn as
// its own thing, never as a pale agreement.
func (p *deployPane) View(s Styles, width, height int) string {
	var b strings.Builder
	for _, w := range p.v.Warnings {
		b.WriteString(s.Caution.Render("! "+w) + "\n")
	}
	if len(p.v.Rows) == 0 {
		b.WriteString(
			s.Dim.Render("asking kubectl and flipr...") + "\n",
		)
		return b.String()
	}
	if len(p.rows()) == 0 {
		b.WriteString(
			s.Dim.Render(
				"nothing is running tag "+
					":"+p.filter+" · f cycles, esc clears",
			) + "\n",
		)
		return b.String()
	}
	disagree := 0
	unknowable := 0
	for _, d := range p.v.Rows {
		switch d.Agree {
		case AgreeNo:
			disagree++
		case AgreeUnknowable:
			unknowable++
		}
	}
	// say what the screen is doing before showing its verdicts. "agrees"
	// and "cannot say" are only meaningful once you know WHAT is being
	// compared, and a column of verdicts you have to decode is a column
	// people learn to skip.
	b.WriteString(s.Dim.Render(
		"flipr records the commit each service was deployed at; the "+
			"pod says which image it runs. This compares them.",
	) + "\n")
	// counting WHY, not just how many. "31 uncheckable" is a number to
	// shrug at; "24 report no version" is a ticket.
	var noVersion, noRoute, unstamped int
	for _, d := range p.v.Rows {
		switch {
		case d.Health == "no version":
			noVersion++
		case d.Reported == "" && d.Health != "ok":
			noRoute++
		case d.BuildID == "":
			unstamped++
		}
	}
	b.WriteString(
		s.Warn.Render(
			fmt.Sprintf("%d wrong build", disagree),
		) + s.Dim.Render(
			" · ",
		) +
			s.Caution.Render(
				fmt.Sprintf("%d uncheckable", unknowable),
			) + s.Dim.Render(
			" · ",
		) +
			s.Ok.Render(
				fmt.Sprintf(
					"%d confirmed",
					len(p.v.Rows)-disagree-unknowable,
				),
			) + "\n",
	)
	var nsBad, nsUndeclared int
	for _, d := range p.v.Rows {
		switch d.NSState {
		case "missing", "mismatch", "not onboarded":
			nsBad++
		case "undeclared":
			nsUndeclared++
		}
	}
	if nsBad+nsUndeclared > 0 {
		b.WriteString(
			s.Warn.Render(
				fmt.Sprintf(
					"%d have no usable flag namespace",
					nsBad,
				),
			) +
				s.Dim.Render(
					fmt.Sprintf(
						" · %d declare none and fall "+
							"back to whatever "+
							"they default to",
						nsUndeclared,
					),
				) + "\n",
		)
	}
	if noVersion+noRoute+unstamped > 0 {
		b.WriteString(s.Dim.Render(fmt.Sprintf(
			"why: %d answer /health with no version · %d have no "+
				"route to ask · %d run an image whose tag "+
				"names no commit",
			noVersion,
			noRoute,
			unstamped,
		)) + "\n")
	}
	b.WriteString("\n")
	if p.filter != "" {
		b.WriteString(s.AccentAlt.Render("tag :"+p.filter+" only") +
			s.Dim.Render(
				fmt.Sprintf(
					"  (%d of %d) · f cycles tags, esc "+
						"clears",
					len(p.rows()),
					len(p.v.Rows),
				),
			) + "\n")
	}
	sorted := deploySorts[p.sortBy%len(deploySorts)].name
	mark := func(name string) string {
		if name == sorted {
			return name + "\u25be"
		}
		return name
	}
	b.WriteString(
		s.Header.Render(
			pad(
				mark("service"),
				16,
			)+pad(
				"ns",
				10,
			)+pad(
				mark("tag"),
				12,
			)+
				pad(
					"digest",
					14,
				)+pad(
				"reports",
				10,
			)+pad(
				"flag namespace",
				20,
			)+
				pad(
					mark("pods"),
					7,
				)+mark(
				"build",
			),
		) + "\n",
	)

	rows := p.rows()
	size := height - 12
	if height <= 0 {
		size = len(rows)
	} else if size < 3 {
		size = 3
	}
	off := windowOffset(len(rows), p.cursor, size, p.offset)
	p.offset = off
	last := off + size
	if last > len(rows) {
		last = len(rows)
	}
	if off > 0 {
		b.WriteString(
			s.Dim.Render(
				fmt.Sprintf("  %d more above", off),
			) + "\n",
		)
	}
	for i := off; i < last; i++ {
		d := rows[i]
		on := i == p.cursor
		// the verdict is padded out to the row width so the cursor's
		// ground runs the whole line: a highlight that stops halfway is
		// a smudge
		var verdict string
		switch d.Agree {
		case AgreeYes:
			verdict = s.On(s.Ok, on).
				Render(pad("yes, "+d.BuildID, 52))
		case AgreeNo:
			verdict = s.On(s.Warn, on).Render(pad(
				"NO -- image is "+
					d.BuildID+", it reports "+d.Reported,
				52,
			))
		default:
			verdict = s.On(s.Dim, on).
				Render(pad("cannot tell: "+cannotSay(d), 52))
		}
		ready := fmt.Sprintf("%d/%d", d.Ready, d.Pods)
		readyStyle := s.Row
		if d.Ready < d.Pods {
			readyStyle = s.Caution
		}
		marker := s.On(s.Row, on).Render("  ")
		if on {
			marker = s.On(s.RowSel, on).Render("▸ ")
		}
		b.WriteString(
			marker + s.On(s.Row, on).Render(pad(d.Service, 16)) +
				s.On(s.Dim, on).Render(pad(d.Namespace, 10)) +
				s.On(s.Accent, on).Render(pad(d.Tag, 12)) +
				s.On(s.Dim, on).
					Render(pad(orDash(d.Digest), 14)) +
				s.On(reportStyle(s, d), on).
					Render(pad(orDash(d.Reported), 10)) +
				s.On(nsStyle(s, d.NSState), on).
					Render(pad(nsCell(d), 20)) +
				s.On(readyStyle, on).
					Render(pad(ready, 7)) +
				verdict + "\n",
		)
	}
	if last < len(rows) {
		b.WriteString(
			s.Dim.Render(
				fmt.Sprintf("  %d more below", len(rows)-last),
			) + "\n",
		)
	}
	if d := rows[p.cursor]; len(d.SharedWith) > 0 {
		b.WriteString(
			"\n" + s.Dim.Render(
				"  "+d.Service+" runs the same image as "+
					strings.Join(
						d.SharedWith,
						", ",
					)+" -- one build, two names",
			) + "\n",
		)
	}
	return b.String()
}

// cannotSay names which half of the comparison could not answer, because
// "the image is not stamped" and "the service cannot be reached" are two
// different things to go and fix.
func cannotSay(d Deploy) string {
	switch {
	case d.Reported == "" && d.Health != "" && d.Health != "ok":
		// a daemon with no route cannot be asked. That is a fact about
		// the estate's shape rather than a gap in the answer, and it is
		// the same gap DESIGN.md already names as an open seam.
		return d.Health + ": it cannot be asked what it is running"
	case d.Reported == "":
		return d.Service + " answers /health without a version"
	case d.BuildID == "" && buildID(d.Reported) == "":
		return "tag is :" + d.Tag + " and it reports " + d.Reported +
			", neither is a commit"
	case d.BuildID == "":
		// the common case, and NOT a dead end: the tag says nothing but
		// the binary does, so the build IS known -- just not confirmed
		// twice
		return "the tag :" + d.Tag + " says nothing, but it reports " +
			"" + d.Reported
	default:
		return "it reports " + d.Reported + ", which is not a commit"
	}
}

// reportStyle colours what a service says about itself: a build it names is
// a fact worth seeing, and silence should not blend into the row.
func reportStyle(s Styles, d Deploy) lipgloss.Style {
	if d.Reported != "" {
		return s.AccentAlt
	}
	return s.Dim
}

// buildRevKeys are the environment variables a deploy might stamp a commit
// into. Read from the POD SPEC, which is the one place that works for every
// language in this estate at once -- and it is about half typescript, so a
// Go-only answer like runtime/debug.ReadBuildInfo covers a minority of it.
//
// The value must be commit-shaped to be believed. This estate already has
// DODO_FLIPR_VERSION=dodo-dev and PEACOCK_VERSION=peacock-dev sitting in pod
// specs, and both are flipr namespaces rather than builds.
//
// The word "version" means three different things here -- the proto contract
// (v1), the flag namespace (dodo-dev), and the build (435fd50) -- and only the
// third one answers "what is deployed". Accepting any of them because the key
// contained version is exactly the mistake this pane already made once.
var buildRevKeys = []string{
	"BUILD_REV", "BUILD_COMMIT", "GIT_SHA", "GIT_COMMIT",
	"VCS_REVISION", "SOURCE_COMMIT", "IMAGE_REVISION",
}

// buildRev finds a commit stamped into a container's environment.
func buildRev(env []struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}) string {
	for _, want := range buildRevKeys {
		for _, e := range env {
			if e.Name == want && buildID(e.Value) != "" {
				return buildID(e.Value)
			}
		}
	}
	return ""
}

// namespacesHeld reads every namespace flipr holds, keyed by service.
// Distinct from enclave.go's fliprNamespaces, which counts flags in the
// newest namespace: this one needs the namespace names, all of them.
func namespacesHeld(base string) (map[string][]string, error) {
	namespaces, err := listNamespaces(base)
	if err != nil {
		return nil, err
	}
	held := map[string][]string{}
	for _, n := range namespaces {
		held[n.GetService()] = append(
			held[n.GetService()],
			n.GetVersion(),
		)
	}
	return held, nil
}

// checkNamespace answers whether this service's flag scope is sound.
//
// A service without a flipr namespace, or with the wrong one, is not a
// service -- it is a process reading nobody's flags. So the states
// are about configuration and each names a different thing to go and fix:
//
//	ok          it declares a namespace and flipr holds exactly that
//	undeclared  it consumes flipr but names no namespace, so whatever it
//	            defaults to internally is the real answer and nothing here
//	            can see it
//	missing     it declares one flipr has never heard of
//	placeholder flipr holds "_" for it: a namespace-shaped hole
//	mismatch    it declares one thing, flipr holds another
func checkNamespace(d Deploy, held map[string][]string) (string, string) {
	have := held[d.Service]
	joined := strings.Join(have, ", ")
	switch {
	case !d.UsesFlipr && len(have) == 0:
		return "", "" // not a flipr consumer at all: nothing to say
	case len(have) == 1 && have[0] == "_":
		// "_" is a real namespace, not a hole. flipr's own oplog shows
		// edge and shell reading from it and finding their flags --
		// service=edge version=_ key=auth.sso found=1 -- so calling it
		// a finding was an alarm raised on a working service.
		//
		// It is the unversioned namespace, and the only thing worth
		// saying is that there is one scope rather than one per deploy.
		return "_", "unversioned"
	case d.UsesFlipr && len(have) == 0:
		// it reads flipr and flipr has never heard of it. This is the
		// sharpest case on the screen: the process is running and
		// reading nobody's flags.
		return "", "not onboarded"
	case d.DeclaredNS == "":
		return joined, "undeclared"
	case len(have) == 0:
		return "", "missing"
	}
	for _, h := range have {
		if h == d.DeclaredNS {
			return d.DeclaredNS, "ok"
		}
	}
	return joined, "mismatch"
}

// nsStyle colours the configuration check. A placeholder and a mismatch are
// both "this service is not reading the flags anyone thinks it is".
func nsStyle(s Styles, state string) lipgloss.Style {
	switch state {
	case "ok":
		return s.Ok
	case "mismatch", "missing", "not onboarded":
		return s.Warn
	case "undeclared":
		return s.Caution
	case "unversioned":
		return s.Dim
	}
	return s.Dim
}

// nsCell says what a service's flag scope is, and when it is wrong, what is
// wrong with it. "undeclared" is the common case here and is not an error on
// its own: the service defaults to something internally, and nothing outside
// it can see what -- which is itself worth knowing.
func nsCell(d Deploy) string {
	switch d.NSState {
	case "":
		return "-"
	case "ok":
		return d.FliprNS
	case "undeclared":
		return d.FliprNS + " (default)"
	case "unversioned":
		return "_ (unversioned)"
	case "not onboarded":
		return "flipr has no record"
	case "missing":
		return "declares " + d.DeclaredNS + ", flipr has none"
	case "mismatch":
		return "declares " + d.DeclaredNS + " not " + d.FliprNS
	}
	return d.FliprNS
}
